package docket

import (
	"context"
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
	if project.ID <= 0 || len(project.Statuses) == 0 || project.Version != 1 {
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
	if ticket.ID <= 0 || ticket.ProjectID != projectID || ticket.Version != 1 {
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
	if _, err := fixture.store.database.Exec("PRAGMA user_version=2"); err != nil {
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
