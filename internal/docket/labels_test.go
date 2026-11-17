package docket

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func projectWithLabels(t *testing.T, fixture *fixture, key string, labels string) Project {
	t.Helper()
	response := call(fixture.handler, "POST", "/api/projects", fmt.Sprintf(`{"name":"Project %s","key":"%s","labels":%s}`, key, key, labels), 0)
	requireStatus(t, response, 201)
	var project Project
	if err := json.Unmarshal(response.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	return project
}

func readTicketForTest(t *testing.T, fixture *fixture, projectID, ticketID int64) Ticket {
	t.Helper()
	response := call(fixture.handler, "GET", fmt.Sprintf("/api/projects/%d/tickets/%d", projectID, ticketID), "", 0)
	requireStatus(t, response, 200)
	var ticket Ticket
	if err := json.Unmarshal(response.Body.Bytes(), &ticket); err != nil {
		t.Fatal(err)
	}
	return ticket
}

func TestProjectLabelsAreNormalizedAndValidated(t *testing.T) {
	fixture := newFixture(t)
	project := projectWithLabels(t, fixture, "LAB", `["UI"," docs ","api-2"]`)
	if strings.Join(project.Labels, ",") != "ui,docs,api-2" {
		t.Fatalf("labels %v, want normalized ui,docs,api-2", project.Labels)
	}

	for _, test := range []struct {
		name   string
		labels string
		hint   string
	}{
		{"duplicate after normalization", `["ui","UI"]`, "listed twice"},
		{"space inside name", `["ui fixes"]`, "single hyphens"},
		{"leading hyphen", `["-ui"]`, "single hyphens"},
		{"trailing hyphen", `["ui-"]`, "single hyphens"},
		{"double hyphen", `["ui--fix"]`, "single hyphens"},
		{"non ascii", `["über"]`, "single hyphens"},
		{"too long", `["` + strings.Repeat("a", LabelBytesMax+1) + `"]`, "single hyphens"},
		{"empty", `[""]`, "single hyphens"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := call(fixture.handler, "POST", "/api/projects", `{"name":"Rejected","key":"NO","labels":`+test.labels+`}`, 0)
			requireStatus(t, response, 400)
			if !strings.Contains(response.Body.String(), test.hint) {
				t.Fatalf("message %q does not explain %q", response.Body.String(), test.hint)
			}
		})
	}

	tooMany := make([]string, LabelsMax+1)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("label-%d", index)
	}
	encoded, err := json.Marshal(map[string]any{"name": "Too many", "key": "NO", "labels": tooMany})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, call(fixture.handler, "POST", "/api/projects", string(encoded), 0), 400)
}

func TestTicketLabelsMustExistInProject(t *testing.T) {
	fixture := newFixture(t)
	project := projectWithLabels(t, fixture, "TAG", `["ui","api"]`)
	path := fmt.Sprintf("/api/projects/%d/tickets", project.ID)

	created := call(fixture.handler, "POST", path, `{"title":"Polished","description":"","labels":["UI","api"]}`, 0)
	requireStatus(t, created, 201)
	var ticket Ticket
	if err := json.Unmarshal(created.Body.Bytes(), &ticket); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ticket.Labels, ",") != "ui,api" {
		t.Fatalf("ticket labels %v, want ui,api", ticket.Labels)
	}

	rejected := call(fixture.handler, "POST", path, `{"title":"Invented","description":"","labels":["ui","perf"]}`, 0)
	requireStatus(t, rejected, 400)
	message := rejected.Body.String()
	if !strings.Contains(message, "perf") || !strings.Contains(message, "ui, api") {
		t.Fatalf("rejection %q must name the offending and the available labels", message)
	}

	listed := call(fixture.handler, "GET", path, "", 0)
	requireStatus(t, listed, 200)
	var page struct {
		Items []Ticket `json:"items"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("stored %d tickets, want the 1 accepted ticket", len(page.Items))
	}
}

func TestTicketLabelsUpdateAndFilter(t *testing.T) {
	fixture := newFixture(t)
	project := projectWithLabels(t, fixture, "FLT", `["ui","api","docs"]`)
	path := fmt.Sprintf("/api/projects/%d/tickets", project.ID)
	created := call(fixture.handler, "POST", path, `{"title":"Filterable","description":"","labels":["api"]}`, 0)
	requireStatus(t, created, 201)
	var ticket Ticket
	if err := json.Unmarshal(created.Body.Bytes(), &ticket); err != nil {
		t.Fatal(err)
	}

	updated := call(fixture.handler, "PATCH", fmt.Sprintf("/api/projects/%d/tickets/%d", project.ID, ticket.ID), `{"labels":["UI","Docs"]}`, ticket.Version)
	requireStatus(t, updated, 200)
	if !strings.Contains(updated.Body.String(), `"labels":["ui","docs"]`) {
		t.Fatalf("update did not store normalized labels: %s", updated.Body.String())
	}

	for _, test := range []struct {
		label string
		want  int
	}{
		{"ui", 1},
		{"UI", 1},
		{"api", 0},
		{"missing", 0},
		{"", 1},
	} {
		response := call(fixture.handler, "GET", path+"?label="+test.label, "", 0)
		requireStatus(t, response, 200)
		var page struct {
			Items []Ticket `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != test.want {
			t.Fatalf("filter %q returned %d tickets, want %d", test.label, len(page.Items), test.want)
		}
	}

	requireStatus(t, call(fixture.handler, "GET", path+"?label=ui%20fixes", "", 0), 400)
	requireStatus(t, call(fixture.handler, "PATCH", fmt.Sprintf("/api/projects/%d/tickets/%d", project.ID, ticket.ID), `{"labels":["perf"]}`, 2), 400)

	history := call(fixture.handler, "GET", fmt.Sprintf("/api/projects/%d/tickets/%d/history", project.ID, ticket.ID), "", 0)
	requireStatus(t, history, 200)
	if !strings.Contains(history.Body.String(), `"labels":["ui","docs"]`) {
		t.Fatalf("history omitted the label change: %s", history.Body.String())
	}
}

func TestProjectLabelRemovalRequiresUnusedLabel(t *testing.T) {
	fixture := newFixture(t)
	project := projectWithLabels(t, fixture, "RMV", `["ui","api"]`)
	ticket := ticketForTest(t, fixture, project.ID)
	requireStatus(t, call(fixture.handler, "PATCH", fmt.Sprintf("/api/projects/%d/tickets/%d", project.ID, ticket.ID), `{"labels":["ui"]}`, ticket.Version), 200)

	blocked := call(fixture.handler, "PATCH", fmt.Sprintf("/api/projects/%d", project.ID), `{"name":"Project RMV","statuses":["To Do","In Progress","Blocked","In Review","Done"],"labels":["api"]}`, project.Version)
	requireStatus(t, blocked, 409)
	if !strings.Contains(blocked.Body.String(), "Remove label ui from its tickets") {
		t.Fatalf("blocked removal message is not actionable: %s", blocked.Body.String())
	}
	if reopened := readTicketForTest(t, fixture, project.ID, ticket.ID); strings.Join(reopened.Labels, ",") != "ui" {
		t.Fatalf("blocked removal changed the ticket: %v", reopened.Labels)
	}

	requireStatus(t, call(fixture.handler, "PATCH", fmt.Sprintf("/api/projects/%d/tickets/%d", project.ID, ticket.ID), `{"labels":[]}`, ticket.Version+1), 200)
	removed := call(fixture.handler, "PATCH", fmt.Sprintf("/api/projects/%d", project.ID), `{"name":"Project RMV","statuses":["To Do","In Progress","Blocked","In Review","Done"],"labels":["api"]}`, project.Version)
	requireStatus(t, removed, 200)
	if !strings.Contains(removed.Body.String(), `"labels":["api"]`) {
		t.Fatalf("label removal was not saved: %s", removed.Body.String())
	}
}

func TestTicketLabelLimits(t *testing.T) {
	fixture := newFixture(t)
	project := projectWithLabels(t, fixture, "LIM", `["a","b","c","d","e","f","g","h","i"]`)
	path := fmt.Sprintf("/api/projects/%d/tickets", project.ID)
	nine := make([]string, TicketLabelsMax+1)
	for index := range nine {
		nine[index] = string(rune('a' + index))
	}
	encoded, err := json.Marshal(map[string]any{"title": "Too many", "description": "", "labels": nine})
	if err != nil {
		t.Fatal(err)
	}
	response := call(fixture.handler, "POST", path, string(encoded), 0)
	requireStatus(t, response, 400)
	if !strings.Contains(response.Body.String(), fmt.Sprintf("at most %d labels", TicketLabelsMax)) {
		t.Fatalf("limit message unclear: %s", response.Body.String())
	}
}

func TestLabelsMigrationFromSchemaThree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-three.sqlite")
	legacy, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, setupError := legacy.Exec(`
CREATE TABLE projects (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, key TEXT NOT NULL UNIQUE, statuses TEXT NOT NULL CHECK(json_valid(statuses)), version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0), created_at TEXT NOT NULL, archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)));
CREATE TABLE tickets (id INTEGER PRIMARY KEY AUTOINCREMENT, project_id INTEGER NOT NULL REFERENCES projects(id), title TEXT NOT NULL, description TEXT NOT NULL, status TEXT NOT NULL, version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0), created_at TEXT NOT NULL, updated_at TEXT NOT NULL, archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)), assignee TEXT NOT NULL DEFAULT '', parent_id INTEGER REFERENCES tickets(id) CHECK(parent_id > 0 AND parent_id <> id));
CREATE INDEX tickets_project ON tickets(project_id,id);
CREATE INDEX tickets_parent ON tickets(project_id,parent_id,id);
CREATE TABLE comments (id INTEGER PRIMARY KEY AUTOINCREMENT, ticket_id INTEGER NOT NULL REFERENCES tickets(id), body TEXT NOT NULL, actor TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE INDEX comments_ticket ON comments(ticket_id,id);
CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, project_id INTEGER NOT NULL REFERENCES projects(id), ticket_id INTEGER REFERENCES tickets(id), kind TEXT NOT NULL, actor TEXT NOT NULL, data TEXT NOT NULL CHECK(json_valid(data)), created_at TEXT NOT NULL);
CREATE INDEX events_project ON events(project_id,id);
CREATE INDEX events_ticket ON events(ticket_id,id);
INSERT INTO projects VALUES(1,'Legacy','OLD','["To Do"]',1,'2026-01-01T00:00:00Z',0);
INSERT INTO tickets VALUES(1,1,'Parent','Description','To Do',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',0,'Owner',NULL);
INSERT INTO tickets VALUES(2,1,'Child','Description','To Do',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',0,'',1);
PRAGMA user_version=3;`)
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
	var version int
	if err := store.database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version %d, want %d", version, schemaVersion)
	}

	response := call(handler, "GET", "/api/projects/1", "", 0)
	requireStatus(t, response, 200)
	if !strings.Contains(response.Body.String(), `"labels":[]`) {
		t.Fatalf("migrated project labels are not empty: %s", response.Body.String())
	}
	response = call(handler, "GET", "/api/projects/1/tickets/2", "", 0)
	requireStatus(t, response, 200)
	if !strings.Contains(response.Body.String(), `"labels":[]`) || !strings.Contains(response.Body.String(), `"parent_id":1`) {
		t.Fatalf("migrated ticket lost its parent or labels: %s", response.Body.String())
	}

	requireStatus(t, call(handler, "PATCH", "/api/projects/1", `{"name":"Legacy","statuses":["To Do"],"labels":["Migrated"]}`, 1), 200)
	requireStatus(t, call(handler, "PATCH", "/api/projects/1/tickets/1", `{"labels":["migrated"]}`, 1), 200)
	if migrated := readTicketForTest(t, &fixture{handler: handler}, 1, 1); strings.Join(migrated.Labels, ",") != "migrated" {
		t.Fatalf("labels unusable after migration: %v", migrated.Labels)
	}
}
