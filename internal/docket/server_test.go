package docket

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type fixture struct {
	store   *Store
	handler http.Handler
	path    string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docket.sqlite")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewServer(store, "http://docket.test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return &fixture{store: store, handler: handler, path: path}
}

func call(handler http.Handler, method, path, body string, version int64) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://docket.test"+path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if version > 0 {
		request.Header.Set("If-Match", fmt.Sprintf("\"%d\"", version))
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func requireStatus(t *testing.T, response *httptest.ResponseRecorder, expected int) {
	t.Helper()
	if response.Code != expected {
		t.Fatalf("status %d, want %d: %s", response.Code, expected, response.Body.String())
	}
}

func projectForTest(t *testing.T, fixture *fixture, key string) Project {
	t.Helper()
	response := call(fixture.handler, "POST", "/api/projects", fmt.Sprintf(`{"name":"Project %s","key":"%s"}`, key, key), 0)
	requireStatus(t, response, 201)
	var project Project
	if err := json.Unmarshal(response.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	if project.ID <= 0 || len(project.Statuses) == 0 || project.Version != 1 || project.Archived {
		t.Fatalf("invalid created project: %+v", project)
	}
	return project
}

func ticketForTest(t *testing.T, fixture *fixture, projectID int64) Ticket {
	t.Helper()
	response := call(fixture.handler, "POST", fmt.Sprintf("/api/projects/%d/tickets", projectID), `{"title":"Build the small thing","description":"Acceptance criteria"}`, 0)
	requireStatus(t, response, 201)
	var ticket Ticket
	if err := json.Unmarshal(response.Body.Bytes(), &ticket); err != nil {
		t.Fatal(err)
	}
	if ticket.ID <= 0 || ticket.ProjectID != projectID || ticket.Version != 1 || ticket.Archived || ticket.Assignee != "" {
		t.Fatalf("invalid created ticket: %+v", ticket)
	}
	return ticket
}

func TestLifecycleAndDurableHistory(t *testing.T) {
	fixture := newFixture(t)
	project := projectForTest(t, fixture, "WEB")
	ticket := ticketForTest(t, fixture, project.ID)
	base := fmt.Sprintf("/api/projects/%d/tickets/%d", project.ID, ticket.ID)
	response := call(fixture.handler, "GET", base, "", 0)
	requireStatus(t, response, 200)
	if response.Header().Get("ETag") != `"1"` {
		t.Fatal("missing version ETag")
	}
	requireStatus(t, call(fixture.handler, "PATCH", base, `{"status":"In Progress"}`, 0), 428)
	requireStatus(t, call(fixture.handler, "PATCH", base, `{"status":"In Progress"}`, 1), 200)
	requireStatus(t, call(fixture.handler, "PATCH", base, `{"title":"Stale overwrite"}`, 1), 409)
	response = call(fixture.handler, "POST", base+"/comments", `{"body":"Ready for a look"}`, 2)
	requireStatus(t, response, 201)
	var posted struct {
		Comment Comment `json:"comment"`
		Version int64   `json:"ticket_version"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &posted); err != nil {
		t.Fatal(err)
	}
	if posted.Version != 3 || posted.Comment.Actor != "Owner" {
		t.Fatalf("invalid comment response: %+v", posted)
	}
	requireStatus(t, call(fixture.handler, "POST", base+"/comments", `{"body":"Stale retry"}`, 2), 409)
	response = call(fixture.handler, "GET", base+"/history", "", 0)
	requireStatus(t, response, 200)
	var history struct {
		Items  []Event `json:"items"`
		Cursor int64   `json:"next_cursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Items) != 3 {
		t.Fatalf("history has %d events, want ticket creation, update, comment", len(history.Items))
	}
	for _, event := range history.Items {
		if event.TicketID == nil || *event.TicketID != ticket.ID || event.ProjectID != project.ID {
			t.Fatalf("wrong event scope: %+v", event)
		}
	}
	response = call(fixture.handler, "GET", fmt.Sprintf("/api/projects/%d/events?after=%d", project.ID, history.Cursor), "", 0)
	requireStatus(t, response, 200)
	var empty struct {
		Items  []Event `json:"items"`
		Cursor int64   `json:"next_cursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	if len(empty.Items) != 0 || empty.Cursor != history.Cursor {
		t.Fatal("empty feed must preserve consumer cursor")
	}
}

func TestConcurrentVersionCheck(t *testing.T) {
	fixture := newFixture(t)
	project := projectForTest(t, fixture, "RACE")
	ticket := ticketForTest(t, fixture, project.ID)
	const writers = 8
	statuses := make(chan int, writers)
	var waiting sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < writers; i++ {
		waiting.Add(1)
		go func(index int) {
			defer waiting.Done()
			<-start
			response := call(fixture.handler, "PATCH", fmt.Sprintf("/api/projects/%d/tickets/%d", project.ID, ticket.ID), fmt.Sprintf(`{"title":"Writer %d"}`, index), 1)
			statuses <- response.Code
		}(i)
	}
	close(start)
	waiting.Wait()
	close(statuses)
	accepted, rejected := 0, 0
	for status := range statuses {
		switch status {
		case 200:
			accepted++
		case 409:
			rejected++
		default:
			t.Fatalf("unexpected concurrent write status %d", status)
		}
	}
	if accepted != 1 || rejected != writers-1 {
		t.Fatalf("accepted=%d rejected=%d", accepted, rejected)
	}
	var events int
	if err := fixture.store.database.QueryRow("SELECT COUNT(*) FROM events WHERE kind='ticket.updated'").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("committed %d update events", events)
	}
}

func TestCrossProjectReferencesRejected(t *testing.T) {
	fixture := newFixture(t)
	first := projectForTest(t, fixture, "FIRST")
	second := projectForTest(t, fixture, "SECOND")
	ticket := ticketForTest(t, fixture, first.ID)
	base := fmt.Sprintf("/api/projects/%d/tickets/%d", second.ID, ticket.ID)
	cases := []struct{ method, path, body string }{
		{"GET", base, ""}, {"PATCH", base, `{"title":"Wrong board"}`},
		{"POST", base + "/comments", `{"body":"Wrong board"}`},
		{"GET", base + "/comments", ""}, {"GET", base + "/history", ""},
	}
	for _, test := range cases {
		requireStatus(t, call(fixture.handler, test.method, test.path, test.body, 1), 404)
	}
	requireStatus(t, call(fixture.handler, "POST", fmt.Sprintf("/api/projects/%d/tickets", second.ID), `{"title":"Unknown status","status":"not configured"}`, 0), 400)
	response := call(fixture.handler, "GET", fmt.Sprintf("/api/projects/%d/events", second.ID), "", 0)
	if strings.Contains(response.Body.String(), "Wrong board") {
		t.Fatal("cross-board attempt leaked into events")
	}
}

func TestEventFailureRollsBackEverything(t *testing.T) {
	fixture := newFixture(t)
	project := projectForTest(t, fixture, "ATOMIC")
	ticket := ticketForTest(t, fixture, project.ID)
	if _, err := fixture.store.database.Exec(`CREATE TRIGGER fail_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT,'forced event failure'); END;`); err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("/api/projects/%d/tickets/%d", project.ID, ticket.ID)
	// The synthetic trigger is a constraint failure, so the API deliberately returns a conflict.
	requireStatus(t, call(fixture.handler, "PATCH", base, `{"title":"Must roll back"}`, 1), 409)
	requireStatus(t, call(fixture.handler, "POST", base+"/comments", `{"body":"Must roll back"}`, 1), 409)
	requireStatus(t, call(fixture.handler, "POST", fmt.Sprintf("/api/projects/%d/tickets", project.ID), `{"title":"Must roll back"}`, 0), 409)
	var title string
	var version, comments, tickets, events int
	if err := fixture.store.database.QueryRow("SELECT title,version FROM tickets WHERE id=?", ticket.ID).Scan(&title, &version); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.database.QueryRow("SELECT COUNT(*) FROM comments").Scan(&comments); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.database.QueryRow("SELECT COUNT(*) FROM tickets").Scan(&tickets); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.database.QueryRow("SELECT COUNT(*) FROM events").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if title != ticket.Title || version != 1 || comments != 0 || tickets != 1 || events != 2 {
		t.Fatalf("partial mutation: title=%q version=%d comments=%d tickets=%d events=%d", title, version, comments, tickets, events)
	}
}

func TestProjectStatusConfiguration(t *testing.T) {
	fixture := newFixture(t)
	project := projectForTest(t, fixture, "FLOW")
	ticketForTest(t, fixture, project.ID)
	path := fmt.Sprintf("/api/projects/%d", project.ID)
	requireStatus(t, call(fixture.handler, "PATCH", path, `{"name":"Renamed","statuses":["Only"]}`, 1), 409)
	requireStatus(t, call(fixture.handler, "PATCH", path, `{"name":"Renamed","statuses":["To Do","Only"]}`, 1), 200)
	requireStatus(t, call(fixture.handler, "PATCH", path, `{"name":"Stale","statuses":["To Do"]}`, 1), 409)
	requireStatus(t, call(fixture.handler, "PATCH", path, `{"name":"Duplicate","statuses":["To Do","To Do"]}`, 2), 400)
}

func TestRestartReplayAndBackup(t *testing.T) {
	fixture := newFixture(t)
	project := projectForTest(t, fixture, "KEEP")
	ticket := ticketForTest(t, fixture, project.ID)
	feed := fmt.Sprintf("/api/projects/%d/events?after=1", project.ID)
	before := call(fixture.handler, "GET", feed, "", 0)
	requireStatus(t, before, 200)
	backup := filepath.Join(t.TempDir(), "backup.sqlite")
	if err := fixture.store.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Backup(context.Background(), backup); err == nil {
		t.Fatal("backup overwrote existing target")
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fixture.path, backup} {
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		handler, err := NewServer(store, "http://docket.test")
		if err != nil {
			t.Fatal(err)
		}
		after := call(handler, "GET", feed, "", 0)
		requireStatus(t, after, 200)
		if after.Body.String() != before.Body.String() {
			t.Fatal("replayed event changed after reopen/backup")
		}
		requireStatus(t, call(handler, "GET", fmt.Sprintf("/api/projects/%d/tickets/%d", project.ID, ticket.ID), "", 0), 200)
		var integrity string
		if err := store.database.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
			t.Fatal(err)
		}
		if integrity != "ok" {
			t.Fatalf("integrity check: %s", integrity)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCapacityFailsWithoutEviction(t *testing.T) {
	fixture := newFixture(t)
	project := projectForTest(t, fixture, "FULL")
	if _, err := fixture.store.database.Exec("INSERT INTO events(id,project_id,kind,actor,data,created_at) VALUES(?,?,'test','test','{}','test')", EventsMax, project.ID); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, call(fixture.handler, "POST", fmt.Sprintf("/api/projects/%d/tickets", project.ID), `{"title":"No room for event"}`, 0), 503)
	var tickets, events int
	if err := fixture.store.database.QueryRow("SELECT COUNT(*) FROM tickets").Scan(&tickets); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.database.QueryRow("SELECT COUNT(*) FROM events").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if tickets != 0 || events != 2 {
		t.Fatal("capacity check lost history or partially created a ticket")
	}
}

func TestValidationAndBrowserBoundary(t *testing.T) {
	fixture := newFixture(t)
	project := projectForTest(t, fixture, "SAFE")
	cases := []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/projects", `null`, 400},
		{"POST", "/api/projects", `{"name":"Test","key":"TEST","unknown":true}`, 400},
		{"POST", "/api/projects", `{"name":"Test","key":"TEST"} {}`, 400},
		{"POST", "/api/projects", `{"name":"Test","key":"TEST","statuses":[]}`, 400},
		{"POST", fmt.Sprintf("/api/projects/%d/tickets", project.ID), `{"title":"   "}`, 400},
		{"POST", fmt.Sprintf("/api/projects/%d/tickets", project.ID), `{"title":"` + strings.Repeat("x", requestBytesMax) + `"}`, 400},
		{"GET", "/api/projects?limit=101", "", 400}, {"GET", "/api/projects?after=-1", "", 400},
		{"GET", "/api/projects/nope/tickets", "", 400}, {"GET", "/api/projects/999/tickets", "", 404},
		{"GET", "/api/tickets", "", 404}, {"GET", "/projects/nope", "", 404},
	}
	for _, test := range cases {
		requireStatus(t, call(fixture.handler, test.method, test.path, test.body, 0), test.status)
	}
	for _, header := range []string{"Origin", "Sec-Fetch-Site", "Host"} {
		request := httptest.NewRequest("POST", "http://docket.test/api/projects", strings.NewReader(`{"name":"Bad","key":"BAD"}`))
		request.Header.Set("Content-Type", "application/json")
		switch header {
		case "Origin":
			request.Header.Set(header, "http://hostile.test")
		case "Sec-Fetch-Site":
			request.Header.Set(header, "cross-site")
		case "Host":
			request.Host = "hostile.test"
		}
		response := httptest.NewRecorder()
		fixture.handler.ServeHTTP(response, request)
		requireStatus(t, response, 403)
	}
	request := httptest.NewRequest("POST", "http://docket.test/api/projects", strings.NewReader(`{"name":"Bad","key":"BAD"}`))
	request.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	requireStatus(t, response, 415)
	response = call(fixture.handler, "GET", "/projects/1", "", 0)
	requireStatus(t, response, 200)
	if !strings.Contains(response.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("missing framing protection")
	}
	if !strings.Contains(response.Body.String(), "/assets/app.js") {
		t.Fatal("UI not embedded")
	}
}

func TestBoundValuesAreLiteral(t *testing.T) {
	fixture := newFixture(t)
	project := projectForTest(t, fixture, "TEXT")
	const hostile = "'); DROP TABLE tickets; -- <script>alert(1)</script>"
	body, err := json.Marshal(map[string]string{"title": hostile, "description": hostile})
	if err != nil {
		t.Fatal(err)
	}
	response := call(fixture.handler, "POST", fmt.Sprintf("/api/projects/%d/tickets", project.ID), string(body), 0)
	requireStatus(t, response, 201)
	var ticket Ticket
	if err := json.Unmarshal(response.Body.Bytes(), &ticket); err != nil {
		t.Fatal(err)
	}
	if ticket.Title != hostile || ticket.Description != hostile {
		t.Fatal("bound text changed")
	}
	requireStatus(t, call(fixture.handler, "GET", fmt.Sprintf("/api/projects/%d/tickets", project.ID), "", 0), 200)
}

func TestPaginationAndProjectEventIsolation(t *testing.T) {
	fixture := newFixture(t)
	first := projectForTest(t, fixture, "PAGE")
	second := projectForTest(t, fixture, "OTHER")
	one := ticketForTest(t, fixture, first.ID)
	ticketForTest(t, fixture, second.ID)
	two := ticketForTest(t, fixture, first.ID)
	response := call(fixture.handler, "GET", fmt.Sprintf("/api/projects/%d/tickets?limit=1", first.ID), "", 0)
	var page struct {
		Items  []Ticket `json:"items"`
		Cursor int64    `json:"next_cursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != one.ID || page.Cursor != one.ID {
		t.Fatalf("invalid first page: %+v", page)
	}
	response = call(fixture.handler, "GET", fmt.Sprintf("/api/projects/%d/tickets?limit=1&after=%d", first.ID, page.Cursor), "", 0)
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != two.ID {
		t.Fatal("pagination skipped or leaked a ticket")
	}
	var events struct {
		Items []Event `json:"items"`
	}
	response = call(fixture.handler, "GET", fmt.Sprintf("/api/projects/%d/events", first.ID), "", 0)
	if err := json.Unmarshal(response.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	if len(events.Items) != 3 {
		t.Fatal("project feed has unexpected events")
	}
	for _, event := range events.Items {
		if event.ProjectID != first.ID {
			t.Fatal("feed crossed project scope")
		}
	}
}

func TestRateLimitAndRequestDeadline(t *testing.T) {
	fixture := newFixture(t)
	connection, err := fixture.store.database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest("GET", "http://docket.test/healthz", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	requireStatus(t, response, 503)
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < requestsPerMinuteMax; i++ {
		requireStatus(t, call(fixture.handler, "GET", "/healthz", "", 0), 200)
	}
	response = call(fixture.handler, "GET", "/healthz", "", 0)
	requireStatus(t, response, 429)
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("rate limit omitted retry hint")
	}
}

func TestTwoConnectionsCannotBothWin(t *testing.T) {
	fixture := newFixture(t)
	project := projectForTest(t, fixture, "MULTI")
	ticket := ticketForTest(t, fixture, project.ID)
	second, err := Open(fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := second.Close(); err != nil {
			t.Error(err)
		}
	}()
	other, err := NewServer(second, "http://docket.test")
	if err != nil {
		t.Fatal(err)
	}
	responses := make(chan int, 2)
	var waiting sync.WaitGroup
	start := make(chan struct{})
	for _, handler := range []http.Handler{fixture.handler, other} {
		waiting.Add(1)
		go func(handler http.Handler) {
			defer waiting.Done()
			<-start
			responses <- call(handler, "PATCH", fmt.Sprintf("/api/projects/%d/tickets/%d", project.ID, ticket.ID), `{"status":"In Progress"}`, 1).Code
		}(handler)
	}
	close(start)
	waiting.Wait()
	close(responses)
	accepted, rejected := 0, 0
	for status := range responses {
		if status == 200 {
			accepted++
		} else if status == 409 {
			rejected++
		} else {
			t.Fatalf("unexpected status %d", status)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("independent connections accepted=%d rejected=%d", accepted, rejected)
	}
}

func TestInvalidOriginsAndNewerSchema(t *testing.T) {
	fixture := newFixture(t)
	for _, origin := range []string{"", "ftp://example.com", "http://user:password@example.com", "http://example.com/path", "http://example.com?query=1", "http://example.com/#fragment"} {
		if _, err := NewServer(fixture.store, origin); err == nil {
			t.Fatalf("accepted invalid public URL %q", origin)
		}
	}
	if _, err := fixture.store.database.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(fixture.path); err == nil {
		store.Close()
		t.Fatal("opened unsupported future schema")
	}
}

func TestInvalidUTF8IsRejectedBeforeJSONReplacement(t *testing.T) {
	fixture := newFixture(t)
	project := projectForTest(t, fixture, "UTF8")
	body := "{\"title\":\"" + string([]byte{0xff}) + "\"}"
	requireStatus(t, call(fixture.handler, "POST", fmt.Sprintf("/api/projects/%d/tickets", project.ID), body, 0), 400)
}

func TestProjectArchivePreservesRecordsAndVersions(t *testing.T) {
	fixture := newFixture(t)
	project := projectForTest(t, fixture, "ARCH")
	ticket := ticketForTest(t, fixture, project.ID)
	path := fmt.Sprintf("/api/projects/%d", project.ID)
	ticketPath := fmt.Sprintf("%s/tickets/%d", path, ticket.ID)
	requireStatus(t, call(fixture.handler, "POST", ticketPath+"/comments", `{"body":"Keep this discussion"}`, ticket.Version), 201)
	preserved := make(map[string]string)
	for _, suffix := range []string{"", "/comments", "/history"} {
		response := call(fixture.handler, "GET", ticketPath+suffix, "", 0)
		requireStatus(t, response, 200)
		preserved[suffix] = response.Body.String()
	}
	response := call(fixture.handler, "PATCH", path, `{"archived":true}`, project.Version)
	requireStatus(t, response, 200)
	var archived Project
	if err := json.Unmarshal(response.Body.Bytes(), &archived); err != nil {
		t.Fatal(err)
	}
	if !archived.Archived || archived.Version != project.Version+1 || archived.Name != project.Name || strings.Join(archived.Statuses, "|") != strings.Join(project.Statuses, "|") {
		t.Fatalf("invalid archived project: %+v", archived)
	}
	for suffix, before := range preserved {
		response := call(fixture.handler, "GET", ticketPath+suffix, "", 0)
		requireStatus(t, response, 200)
		if response.Body.String() != before {
			t.Fatalf("archiving changed ticket records at %s", suffix)
		}
	}
	requireStatus(t, call(fixture.handler, "PATCH", path, `{"archived":false}`, 0), 428)
	requireStatus(t, call(fixture.handler, "PATCH", path, `{"archived":false}`, project.Version), 409)
	for _, body := range []string{`{}`, `{"archived":null}`, `{"archived":"true"}`, `{"name":"Partial"}`, `{"statuses":["Only"]}`} {
		requireStatus(t, call(fixture.handler, "PATCH", path, body, archived.Version), 400)
	}
	response = call(fixture.handler, "GET", "/api/projects", "", 0)
	requireStatus(t, response, 200)
	if !strings.Contains(response.Body.String(), `"archived":true`) {
		t.Fatal("API project listing must retain archived projects")
	}
	response = call(fixture.handler, "GET", path+"/events", "", 0)
	requireStatus(t, response, 200)
	var events struct {
		Items []Event `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	if len(events.Items) != 4 {
		t.Fatalf("expected four retained events, got %d", len(events.Items))
	}
	last := events.Items[len(events.Items)-1]
	if last.Kind != "project.updated" || !strings.Contains(string(last.Data), `"archived":true`) {
		t.Fatal("archive must publish a committed project snapshot")
	}
	response = call(fixture.handler, "PATCH", path, `{"archived":false}`, archived.Version)
	requireStatus(t, response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &archived); err != nil {
		t.Fatal(err)
	}
	if archived.Archived || archived.Version != project.Version+2 {
		t.Fatal("restore did not advance project version")
	}
	if _, err := fixture.store.database.Exec(`CREATE TRIGGER fail_archive_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT,'forced event failure'); END;`); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, call(fixture.handler, "PATCH", path, `{"archived":true}`, archived.Version), 409)
	response = call(fixture.handler, "GET", path, "", 0)
	requireStatus(t, response, 200)
	var unchanged struct {
		Archived bool  `json:"archived"`
		Version  int64 `json:"version"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged.Archived || unchanged.Version != archived.Version {
		t.Fatal("event failure partially archived a project")
	}
}

func TestArchiveMigrationAndBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	legacy, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	// These are the released v1 columns, including records whose event payloads predate archiving.
	_, setupError := legacy.Exec(`
CREATE TABLE projects (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, key TEXT NOT NULL UNIQUE, statuses TEXT NOT NULL CHECK(json_valid(statuses)), version INTEGER NOT NULL DEFAULT 1 CHECK(version>0), created_at TEXT NOT NULL);
CREATE TABLE tickets (id INTEGER PRIMARY KEY AUTOINCREMENT, project_id INTEGER NOT NULL REFERENCES projects(id), title TEXT NOT NULL, description TEXT NOT NULL, status TEXT NOT NULL, version INTEGER NOT NULL DEFAULT 1 CHECK(version>0), created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE INDEX tickets_project ON tickets(project_id,id);
CREATE TABLE comments (id INTEGER PRIMARY KEY AUTOINCREMENT, ticket_id INTEGER NOT NULL REFERENCES tickets(id), body TEXT NOT NULL, actor TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE INDEX comments_ticket ON comments(ticket_id,id);
CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, project_id INTEGER NOT NULL REFERENCES projects(id), ticket_id INTEGER REFERENCES tickets(id), kind TEXT NOT NULL, actor TEXT NOT NULL, data TEXT NOT NULL CHECK(json_valid(data)), created_at TEXT NOT NULL);
CREATE INDEX events_project ON events(project_id,id);
CREATE INDEX events_ticket ON events(ticket_id,id);
INSERT INTO projects VALUES(1,'Legacy','OLD','["To Do"]',1,'2026-01-01T00:00:00Z');
INSERT INTO tickets VALUES(1,1,'Keep title','Keep description','To Do',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
INSERT INTO comments VALUES(1,1,'Keep comment','Owner','2026-01-01T00:00:00Z');
INSERT INTO events VALUES(7,1,1,'ticket.created','Owner','{"id":1,"status":"To Do"}','2026-01-01T00:00:00Z');
PRAGMA user_version=1;`)
	closeError := legacy.Close()
	if setupError != nil {
		t.Fatal(setupError)
	}
	if closeError != nil {
		t.Fatal(closeError)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	handler, err := NewServer(store, "http://docket.test")
	if err != nil {
		t.Fatal(err)
	}
	response := call(handler, "GET", "/api/projects/1", "", 0)
	requireStatus(t, response, 200)
	var project Project
	if err := json.Unmarshal(response.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	if project.Archived || project.Version != 1 || project.Name != "Legacy" {
		t.Fatal("migration changed an existing project")
	}
	var version int
	if err := store.database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version %d, want %d", version, schemaVersion)
	}
	records := make(map[string]string)
	for _, route := range []string{"/api/projects/1/tickets/1", "/api/projects/1/tickets/1/comments", "/api/projects/1/tickets/1/history"} {
		response := call(handler, "GET", route, "", 0)
		requireStatus(t, response, 200)
		records[route] = response.Body.String()
	}
	if !strings.Contains(records["/api/projects/1/tickets/1"], "Keep description") || !strings.Contains(records["/api/projects/1/tickets/1/comments"], "Keep comment") || !strings.Contains(records["/api/projects/1/tickets/1/history"], `"id":7`) {
		t.Fatal("migration lost legacy records or cursor")
	}
	var legacyTicket Ticket
	if err := json.Unmarshal([]byte(records["/api/projects/1/tickets/1"]), &legacyTicket); err != nil {
		t.Fatal(err)
	}
	if legacyTicket.Archived || legacyTicket.Version != 1 || legacyTicket.Assignee != "" {
		t.Fatal("migration changed existing ticket state")
	}
	requireStatus(t, call(handler, "PATCH", "/api/projects/1", `{"archived":true}`, 1), 200)
	requireStatus(t, call(handler, "PATCH", "/api/projects/1/tickets/1", `{"assignee":"Legacy owner"}`, 1), 200)
	requireStatus(t, call(handler, "PATCH", "/api/projects/1/tickets/1", `{"archived":true}`, 2), 200)
	for route := range records {
		response := call(handler, "GET", route, "", 0)
		requireStatus(t, response, 200)
		records[route] = response.Body.String()
	}
	if !strings.Contains(records["/api/projects/1/tickets/1"], `"archived":true`) || !strings.Contains(records["/api/projects/1/tickets/1"], `"assignee":"Legacy owner"`) {
		t.Fatal("legacy ticket was not archived")
	}
	backup := filepath.Join(t.TempDir(), "archive-backup.sqlite")
	if err := store.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{path, backup} {
		reopened, err := Open(filename)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := reopened.Close(); err != nil {
				t.Error(err)
			}
		})
		reopenedHandler, err := NewServer(reopened, "http://docket.test")
		if err != nil {
			t.Fatal(err)
		}
		response := call(reopenedHandler, "GET", "/api/projects/1", "", 0)
		requireStatus(t, response, 200)
		if err := json.Unmarshal(response.Body.Bytes(), &project); err != nil {
			t.Fatal(err)
		}
		if !project.Archived || project.Version != 2 {
			t.Fatal("archive state lost on reopen or backup")
		}
		for route, before := range records {
			response := call(reopenedHandler, "GET", route, "", 0)
			requireStatus(t, response, 200)
			if response.Body.String() != before {
				t.Fatalf("record changed on reopen or backup: %s", route)
			}
		}
	}
}

func TestProjectEventTailBootstrap(t *testing.T) {
	fixture := newFixture(t)
	first := projectForTest(t, fixture, "TAIL")
	second := projectForTest(t, fixture, "ELSE")
	ticketForTest(t, fixture, first.ID)
	ticketForTest(t, fixture, second.ID)
	response := call(fixture.handler, "GET", fmt.Sprintf("/api/projects/%d/events?tail=true&limit=1", first.ID), "", 0)
	requireStatus(t, response, 200)
	var page struct {
		Items  []Event `json:"items"`
		Cursor int64   `json:"next_cursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != 3 || page.Cursor != 3 || page.Items[0].ProjectID != first.ID {
		t.Fatalf("tail returned wrong project cursor: %+v", page)
	}
	requireStatus(t, call(fixture.handler, "PATCH", fmt.Sprintf("/api/projects/%d", first.ID), `{"archived":true}`, first.Version), 200)
	response = call(fixture.handler, "GET", fmt.Sprintf("/api/projects/%d/events?after=%d", first.ID, page.Cursor), "", 0)
	requireStatus(t, response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != 5 || page.Cursor != 5 {
		t.Fatal("bootstrap cursor skipped a subsequent mutation")
	}
	response = call(fixture.handler, "GET", fmt.Sprintf("/api/projects/%d/events?tail=true&limit=2", first.ID), "", 0)
	requireStatus(t, response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].ID != 3 || page.Items[1].ID != 5 {
		t.Fatal("tail page must retain ascending event order")
	}
	for _, route := range []string{fmt.Sprintf("/api/projects/%d/events?tail=invalid", first.ID), fmt.Sprintf("/api/projects/%d/events?tail=true&after=1", first.ID), fmt.Sprintf("/api/projects/%d/tickets/1/history?tail=true", first.ID)} {
		requireStatus(t, call(fixture.handler, "GET", route, "", 0), 400)
	}
}

func TestTicketArchivePreservesDiscussionAndFiltersLists(t *testing.T) {
	fixture := newFixture(t)
	project := projectForTest(t, fixture, "TARCH")
	ticket := ticketForTest(t, fixture, project.ID)
	base := fmt.Sprintf("/api/projects/%d/tickets", project.ID)
	path := fmt.Sprintf("%s/%d", base, ticket.ID)
	requireStatus(t, call(fixture.handler, "POST", path+"/comments", `{"body":"Keep ticket discussion"}`, ticket.Version), 201)
	beforeComments := call(fixture.handler, "GET", path+"/comments", "", 0)
	requireStatus(t, beforeComments, 200)
	response := call(fixture.handler, "PATCH", path, `{"archived":true}`, ticket.Version+1)
	requireStatus(t, response, 200)
	var archived Ticket
	if err := json.Unmarshal(response.Body.Bytes(), &archived); err != nil {
		t.Fatal(err)
	}
	if !archived.Archived || archived.Version != ticket.Version+2 || archived.Title != ticket.Title || archived.Description != ticket.Description || archived.Status != ticket.Status {
		t.Fatalf("archive changed ticket contents: %+v", archived)
	}
	requireStatus(t, call(fixture.handler, "PATCH", path, `{"archived":false}`, 0), 428)
	requireStatus(t, call(fixture.handler, "PATCH", path, `{"archived":false}`, ticket.Version+1), 409)
	for _, query := range []string{"", "?archived=true", "?archived=false", "?archived="} {
		response := call(fixture.handler, "GET", base+query, "", 0)
		requireStatus(t, response, 200)
		var page struct {
			Items []Ticket `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if query != "?archived=true" {
			if len(page.Items) != 0 {
				t.Fatal("active list included archived ticket")
			}
		} else if len(page.Items) != 1 || page.Items[0].ID != ticket.ID {
			t.Fatal("archive list lost ticket")
		}
	}
	requireStatus(t, call(fixture.handler, "GET", base+"?archived=invalid", "", 0), 400)
	response = call(fixture.handler, "GET", path+"/comments", "", 0)
	requireStatus(t, response, 200)
	if response.Body.String() != beforeComments.Body.String() {
		t.Fatal("ticket archive changed discussion")
	}
	response = call(fixture.handler, "GET", path+"/history", "", 0)
	requireStatus(t, response, 200)
	var history struct {
		Items []Event `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Items) != 3 || !strings.Contains(string(history.Items[2].Data), `"archived":true`) {
		t.Fatal("ticket archive did not retain and append history")
	}
	response = call(fixture.handler, "PATCH", path, `{"archived":false}`, archived.Version)
	requireStatus(t, response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &archived); err != nil {
		t.Fatal(err)
	}
	if archived.Archived || archived.Version != ticket.Version+3 {
		t.Fatal("restore did not advance ticket version")
	}
	if _, err := fixture.store.database.Exec(`CREATE TRIGGER fail_ticket_archive_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT,'forced event failure'); END;`); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, call(fixture.handler, "PATCH", path, `{"archived":true}`, archived.Version), 409)
	response = call(fixture.handler, "GET", path, "", 0)
	requireStatus(t, response, 200)
	var unchanged struct {
		Archived bool  `json:"archived"`
		Version  int64 `json:"version"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged.Archived || unchanged.Version != archived.Version {
		t.Fatal("event failure partially archived a ticket")
	}
}

func TestArchiveMigrationFailureRollsBack(t *testing.T) {
	fixture := newFixture(t)
	projectForTest(t, fixture, "MFAIL")
	// A conflicting ticket column forces the second ALTER to fail after the first succeeds.
	if _, err := fixture.store.database.Exec("ALTER TABLE projects DROP COLUMN archived; PRAGMA user_version=1;"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(fixture.path); err == nil {
		if closeError := reopened.Close(); closeError != nil {
			t.Error(closeError)
		}
		t.Fatal("inconsistent legacy schema unexpectedly migrated")
	}
	database, err := sql.Open("sqlite3", fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	var version, columns, projects int
	if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name='archived'").Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM projects WHERE key='MFAIL'").Scan(&projects); err != nil {
		t.Fatal(err)
	}
	if version != 1 || columns != 0 || projects != 1 {
		t.Fatalf("partial migration: version=%d columns=%d projects=%d", version, columns, projects)
	}
}

func TestTicketAssigneeIsOptionalVersionedText(t *testing.T) {
	fixture := newFixture(t)
	project := projectForTest(t, fixture, "ASSIGN")
	ticket := ticketForTest(t, fixture, project.ID)
	base := fmt.Sprintf("/api/projects/%d/tickets", project.ID)
	path := fmt.Sprintf("%s/%d", base, ticket.ID)
	response := call(fixture.handler, "PATCH", path, `{"assignee":"  Morgan  "}`, ticket.Version)
	requireStatus(t, response, 200)
	var assigned Ticket
	if err := json.Unmarshal(response.Body.Bytes(), &assigned); err != nil {
		t.Fatal(err)
	}
	if assigned.Assignee != "Morgan" || assigned.Version != ticket.Version+1 || assigned.Title != ticket.Title {
		t.Fatalf("invalid assigned ticket: %+v", assigned)
	}
	requireStatus(t, call(fixture.handler, "PATCH", path, `{"assignee":"Other"}`, 0), 428)
	requireStatus(t, call(fixture.handler, "PATCH", path, `{"assignee":"Other"}`, ticket.Version), 409)
	for _, body := range []string{`{"assignee":42}`, `{"assignee":"\u0000"}`, `{"assignee":"` + strings.Repeat("é", 51) + `"}`} {
		requireStatus(t, call(fixture.handler, "PATCH", path, body, assigned.Version), 400)
	}
	response = call(fixture.handler, "PATCH", path, `{"assignee":""}`, assigned.Version)
	requireStatus(t, response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &assigned); err != nil {
		t.Fatal(err)
	}
	if assigned.Assignee != "" || assigned.Version != ticket.Version+2 {
		t.Fatal("clearing assignee did not unassign the ticket")
	}
	response = call(fixture.handler, "POST", base, `{"title":"Assigned on create","assignee":"  Casey  "}`, 0)
	requireStatus(t, response, 201)
	if err := json.Unmarshal(response.Body.Bytes(), &assigned); err != nil {
		t.Fatal(err)
	}
	if assigned.Assignee != "Casey" || assigned.Version != 1 {
		t.Fatal("creation lost assignee")
	}
	response = call(fixture.handler, "GET", fmt.Sprintf("%s/%d/history", base, ticket.ID), "", 0)
	requireStatus(t, response, 200)
	var history struct {
		Items []Event `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Items) != 3 || !strings.Contains(string(history.Items[1].Data), `"assignee":"Morgan"`) || !strings.Contains(string(history.Items[2].Data), `"assignee":""`) {
		t.Fatal("assignee changes missing from durable history")
	}
	if _, err := fixture.store.database.Exec(`CREATE TRIGGER fail_assignment_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT,'forced event failure'); END;`); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, call(fixture.handler, "PATCH", path, `{"assignee":"Must roll back"}`, ticket.Version+2), 409)
	response = call(fixture.handler, "GET", path, "", 0)
	requireStatus(t, response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &assigned); err != nil {
		t.Fatal(err)
	}
	if assigned.Assignee != "" || assigned.Version != ticket.Version+2 {
		t.Fatal("event failure partially changed assignment")
	}
}

func TestTicketListsExcludeArchivesByDefault(t *testing.T) {
	fixture := newFixture(t)
	project := projectForTest(t, fixture, "ACTIVE")
	other := projectForTest(t, fixture, "OTHER")
	archivedFirst := ticketForTest(t, fixture, project.ID)
	activeFirst := ticketForTest(t, fixture, project.ID)
	archivedNext := ticketForTest(t, fixture, project.ID)
	activeNext := ticketForTest(t, fixture, project.ID)
	ticketForTest(t, fixture, other.ID)
	base := fmt.Sprintf("/api/projects/%d/tickets", project.ID)
	for _, ticket := range []Ticket{archivedFirst, archivedNext} {
		path := fmt.Sprintf("%s/%d", base, ticket.ID)
		requireStatus(t, call(fixture.handler, "PATCH", path, `{"archived":true,"title":"Archived needle"}`, ticket.Version), 200)
		requireStatus(t, call(fixture.handler, "GET", path, "", 0), 200)
	}
	cases := []struct {
		query  string
		ids    []int64
		cursor int64
	}{
		{"", []int64{activeFirst.ID, activeNext.ID}, activeNext.ID},
		{"?archived=false", []int64{activeFirst.ID, activeNext.ID}, activeNext.ID},
		{"?archived=", []int64{activeFirst.ID, activeNext.ID}, activeNext.ID},
		{"?archived=true", []int64{archivedFirst.ID, archivedNext.ID}, archivedNext.ID},
		{"?q=Archived", nil, 0},
		{"?q=Archived&archived=true", []int64{archivedFirst.ID, archivedNext.ID}, archivedNext.ID},
		{"?q=Build&limit=1", []int64{activeFirst.ID}, activeFirst.ID},
		{fmt.Sprintf("?q=Build&limit=1&after=%d", activeFirst.ID), []int64{activeNext.ID}, activeNext.ID},
		{fmt.Sprintf("?q=Build&limit=1&after=%d", activeNext.ID), nil, activeNext.ID},
		{fmt.Sprintf("?archived=true&limit=1&after=%d", archivedFirst.ID), []int64{archivedNext.ID}, archivedNext.ID},
	}
	for _, test := range cases {
		response := call(fixture.handler, "GET", base+test.query, "", 0)
		requireStatus(t, response, 200)
		var page struct {
			Items  []Ticket `json:"items"`
			Cursor int64    `json:"next_cursor"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != len(test.ids) || page.Cursor != test.cursor {
			t.Fatalf("query %q: got %d items and cursor %d, want %d items and cursor %d", test.query, len(page.Items), page.Cursor, len(test.ids), test.cursor)
		}
		for index, ticket := range page.Items {
			if ticket.ID != test.ids[index] || ticket.ProjectID != project.ID {
				t.Fatalf("query %q returned wrong ticket scope or order", test.query)
			}
		}
	}
	requireStatus(t, call(fixture.handler, "GET", base+"?archived=all", "", 0), 400)
}

func TestSubticketsStayFlatAndProjectScoped(t *testing.T) {
	fixture := newFixture(t)
	project := projectForTest(t, fixture, "FAMILY")
	other := projectForTest(t, fixture, "OTHER")
	parent := ticketForTest(t, fixture, project.ID)
	collection := fmt.Sprintf("/api/projects/%d/tickets", project.ID)
	parentPath := fmt.Sprintf("%s/%d", collection, parent.ID)
	before := call(fixture.handler, "GET", parentPath, "", 0).Body.String()
	if !strings.Contains(before, `"parent_id":null`) {
		t.Fatal("Root tickets must expose a null parent")
	}
	response := call(fixture.handler, "POST", collection, fmt.Sprintf(`{"title":"First child","parent_id":%d,"assignee":"Morgan"}`, parent.ID), 0)
	requireStatus(t, response, 201)
	var child struct {
		ID       int64  `json:"id"`
		Version  int64  `json:"version"`
		ParentID *int64 `json:"parent_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &child); err != nil {
		t.Fatal(err)
	}
	if child.ParentID == nil || *child.ParentID != parent.ID || child.ID == parent.ID || child.Version != 1 {
		t.Fatalf("invalid subticket: %s", response.Body.String())
	}
	for _, body := range []string{`{"title":"Second child"}`, `{"title":"Third child"}`} {
		body = strings.TrimSuffix(body, "}") + fmt.Sprintf(`,"parent_id":%d}`, parent.ID)
		requireStatus(t, call(fixture.handler, "POST", collection, body, 0), 201)
	}
	requireStatus(t, call(fixture.handler, "POST", collection, fmt.Sprintf(`{"title":"Too deep","parent_id":%d}`, child.ID), 0), 400)
	requireStatus(t, call(fixture.handler, "POST", fmt.Sprintf("/api/projects/%d/tickets", other.ID), fmt.Sprintf(`{"title":"Wrong board","parent_id":%d}`, parent.ID), 0), 404)
	for _, value := range []string{"0", "-1", `"1"`, "1.5", "true", "{}"} {
		requireStatus(t, call(fixture.handler, "POST", collection, `{"title":"Invalid parent","parent_id":`+value+`}`, 0), 400)
	}
	childPath := fmt.Sprintf("%s/%d", collection, child.ID)
	for _, value := range []string{"null", fmt.Sprint(parent.ID)} {
		requireStatus(t, call(fixture.handler, "PATCH", childPath, `{"title":"Do not change","parent_id":`+value+`}`, child.Version), 400)
	}
	response = call(fixture.handler, "GET", collection, "", 0)
	requireStatus(t, response, 200)
	var page struct {
		Items      []Ticket `json:"items"`
		NextCursor int64    `json:"next_cursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 4 {
		t.Fatalf("Main board must include parent and all children: %s", response.Body.String())
	}
	filter := fmt.Sprintf("%s?parent_id=%d&limit=1", collection, parent.ID)
	response = call(fixture.handler, "GET", filter, "", 0)
	requireStatus(t, response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != child.ID {
		t.Fatal("Parent filter must apply before pagination")
	}
	requireStatus(t, call(fixture.handler, "PATCH", childPath, `{"archived":true,"status":"Done"}`, child.Version), 200)
	requireStatus(t, call(fixture.handler, "PATCH", childPath, `{"status":"To Do"}`, child.Version), 409)
	if got := call(fixture.handler, "GET", parentPath, "", 0).Body.String(); got != before {
		t.Fatal("Creating or editing children changed the parent")
	}
	response = call(fixture.handler, "GET", filter+"&archived=true", "", 0)
	requireStatus(t, response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != child.ID {
		t.Fatal("Archived children must remain queryable")
	}
	for _, value := range []string{"", "0", "-1", "bad", "1.5", fmt.Sprintf("%d&parent_id=%d", parent.ID, parent.ID)} {
		requireStatus(t, call(fixture.handler, "GET", collection+"?parent_id="+value, "", 0), 400)
	}
	requireStatus(t, call(fixture.handler, "GET", fmt.Sprintf("/api/projects/%d/tickets?parent_id=%d", other.ID, parent.ID), "", 0), 404)
	requireStatus(t, call(fixture.handler, "PATCH", parentPath, `{"archived":true}`, parent.Version), 200)
	response = call(fixture.handler, "GET", fmt.Sprintf("%s?parent_id=%d", collection, parent.ID), "", 0)
	requireStatus(t, response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatal("Archiving parent must not archive its remaining children")
	}
	response = call(fixture.handler, "GET", childPath+"/history", "", 0)
	requireStatus(t, response, 200)
	if !strings.Contains(response.Body.String(), fmt.Sprintf(`"parent_id":%d`, parent.ID)) {
		t.Fatal("Child history lost its parent reference")
	}
}

func TestSubticketMigrationFromV2AndRollback(t *testing.T) {
	for _, obstructed := range []bool{false, true} {
		t.Run(fmt.Sprintf("obstructed=%t", obstructed), func(t *testing.T) {
			fixture := newFixture(t)
			project := projectForTest(t, fixture, "UPGRADE")
			ticket := ticketForTest(t, fixture, project.ID)
			path := fmt.Sprintf("/api/projects/%d/tickets/%d", project.ID, ticket.ID)
			requireStatus(t, call(fixture.handler, "PATCH", path, `{"assignee":"Preserved owner","archived":true}`, ticket.Version), 200)
			before := call(fixture.handler, "GET", path, "", 0).Body.String()
			history := call(fixture.handler, "GET", path+"/history", "", 0).Body.String()
			if _, err := fixture.store.database.Exec("DROP INDEX tickets_parent; ALTER TABLE tickets DROP COLUMN parent_id; PRAGMA user_version=2;"); err != nil {
				t.Fatal(err)
			}
			if obstructed {
				if _, err := fixture.store.database.Exec("CREATE INDEX tickets_parent ON tickets(id)"); err != nil {
					t.Fatal(err)
				}
			}
			if err := fixture.store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err := Open(fixture.path)
			if obstructed {
				if err == nil {
					if closeError := store.Close(); closeError != nil {
						t.Error(closeError)
					}
					t.Fatal("Obstructed migration unexpectedly succeeded")
				}
				database, err := sql.Open("sqlite3", fixture.path)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := database.Close(); err != nil {
						t.Error(err)
					}
				}()
				var version, columns int
				if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
					t.Fatal(err)
				}
				if err := database.QueryRow("SELECT COUNT(*) FROM pragma_table_info('tickets') WHERE name='parent_id'").Scan(&columns); err != nil {
					t.Fatal(err)
				}
				if version != 2 || columns != 0 {
					t.Fatal("Failed migration left a partial schema")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			}()
			handler, err := NewServer(store, "http://docket.test")
			if err != nil {
				t.Fatal(err)
			}
			if response := call(handler, "GET", path, "", 0); response.Code != 200 || response.Body.String() != before {
				t.Fatal("Migration changed existing ticket fields or version")
			}
			if response := call(handler, "GET", path+"/history", "", 0); response.Code != 200 || response.Body.String() != history {
				t.Fatal("Migration rewrote history or event cursors")
			}
			response := call(handler, "POST", fmt.Sprintf("/api/projects/%d/tickets", project.ID), fmt.Sprintf(`{"title":"Preserve this relationship","parent_id":%d}`, ticket.ID), 0)
			requireStatus(t, response, 201)
			var child Ticket
			if err := json.Unmarshal(response.Body.Bytes(), &child); err != nil {
				t.Fatal(err)
			}
			backup := filepath.Join(t.TempDir(), "subticket-backup.sqlite")
			if err := store.Backup(context.Background(), backup); err != nil {
				t.Fatal(err)
			}
			restored, err := Open(backup)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := restored.Close(); err != nil {
					t.Error(err)
				}
			}()
			kept, err := readTicket(restored.database.QueryRow("SELECT "+ticketColumns+" FROM tickets WHERE id=?", child.ID))
			if err != nil || kept.ParentID == nil || *kept.ParentID != ticket.ID {
				t.Fatalf("Backup lost relationship: %v", err)
			}
			var integrity string
			if err := restored.database.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
				t.Fatalf("Backup integrity: %q, %v", integrity, err)
			}
		})
	}
}

func TestProjectStatsIncludesArchivedAndSubtickets(t *testing.T) {
	fixture := newFixture(t)
	project := projectForTest(t, fixture, "COUNTS")
	other := projectForTest(t, fixture, "OTHER")
	parent := ticketForTest(t, fixture, project.ID)
	path := fmt.Sprintf("/api/projects/%d", project.ID)
	response := call(fixture.handler, "GET", path+"/stats", "", 0)
	requireStatus(t, response, 200)
	if response.Body.String() != "{\"total\":1,\"active\":1,\"archived\":0}\n" {
		t.Fatalf("Bad initial counts: %s", response.Body.String())
	}
	response = call(fixture.handler, "POST", path+"/tickets", fmt.Sprintf(`{"title":"Child","parent_id":%d}`, parent.ID), 0)
	requireStatus(t, response, 201)
	var child Ticket
	if err := json.Unmarshal(response.Body.Bytes(), &child); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, call(fixture.handler, "PATCH", fmt.Sprintf("%s/tickets/%d", path, child.ID), `{"archived":true}`, child.Version), 200)
	response = call(fixture.handler, "GET", path+"/stats", "", 0)
	requireStatus(t, response, 200)
	if response.Body.String() != "{\"total\":2,\"active\":1,\"archived\":1}\n" {
		t.Fatal("Counts must include archived children")
	}
	response = call(fixture.handler, "GET", fmt.Sprintf("/api/projects/%d/stats", other.ID), "", 0)
	requireStatus(t, response, 200)
	if response.Body.String() != "{\"total\":0,\"active\":0,\"archived\":0}\n" {
		t.Fatal("Counts escaped project scope")
	}
	requireStatus(t, call(fixture.handler, "GET", "/api/projects/99999/stats", "", 0), 404)
}
