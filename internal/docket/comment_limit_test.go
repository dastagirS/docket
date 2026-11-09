package docket

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCommentCharacterLimit(t *testing.T) {
	const charactersMax = 2000
	for _, character := range []string{"a", "界", "😀"} {
		t.Run(character, func(t *testing.T) {
			fixture := newFixture(t)
			project := projectForTest(t, fixture, "LIMIT")
			ticket := ticketForTest(t, fixture, project.ID)
			path := fmt.Sprintf("/api/projects/%d/tickets/%d/comments", project.ID, ticket.ID)
			oversized, err := json.Marshal(map[string]string{"body": strings.Repeat(character, charactersMax+1)})
			if err != nil {
				t.Fatal(err)
			}
			requireStatus(t, call(fixture.handler, "POST", path, string(oversized), ticket.Version), 400)
			var count int
			if err := fixture.store.database.QueryRow("SELECT COUNT(*) FROM comments").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("Rejected comment must not be stored")
			}
			body := strings.Repeat(character, charactersMax)
			payload, err := json.Marshal(map[string]string{"body": body})
			if err != nil {
				t.Fatal(err)
			}
			requireStatus(t, call(fixture.handler, "POST", path, string(payload), ticket.Version), 201)
			legacy := strings.Repeat(character, charactersMax+1)
			if _, err := fixture.store.database.Exec("INSERT INTO comments(ticket_id,body,actor,created_at) VALUES(?,?,?,?)", ticket.ID, legacy, "Fixture", "2026-01-01T00:00:00Z"); err != nil {
				t.Fatal(err)
			}
			response := call(fixture.handler, "GET", path, "", 0)
			requireStatus(t, response, 200)
			var page struct {
				Items []Comment `json:"items"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 2 || page.Items[0].Body != body || page.Items[1].Body != legacy {
				t.Fatal("New and legacy comments must be returned without truncation")
			}
		})
	}
}
