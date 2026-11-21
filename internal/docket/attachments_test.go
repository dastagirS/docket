package docket

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func pngFixture(t *testing.T, width, height int) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	picture.Set(0, 0, color.RGBA{R: 0x33, G: 0x66, B: 0x99, A: 0xff})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func upload(t *testing.T, fixture *fixture, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest("POST", "http://docket.test"+path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/octet-stream")
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	return response
}

func fetch(fixture *fixture, path string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, httptest.NewRequest("GET", "http://docket.test"+path, nil))
	return response
}

func TestAttachmentLifecycle(t *testing.T) {
	fixture := newFixture(t)
	project := projectForTest(t, fixture, "IMAGES")
	ticket := ticketForTest(t, fixture, project.ID)
	collection := fmt.Sprintf("/api/projects/%d/tickets/%d/attachments", project.ID, ticket.ID)
	image := pngFixture(t, 4, 4)

	response := upload(t, fixture, collection+"?filename=board%20sketch.png", image)
	requireStatus(t, response, 201)
	var attachment Attachment
	if err := json.Unmarshal(response.Body.Bytes(), &attachment); err != nil {
		t.Fatal(err)
	}
	if attachment.ID <= 0 || attachment.Filename != "board sketch.png" || attachment.ContentType != "image/png" || attachment.Bytes != int64(len(image)) || attachment.Actor != "Owner" || attachment.SHA256 == "" {
		t.Fatalf("unexpected attachment metadata: %+v", attachment)
	}
	if !strings.HasSuffix(attachment.URL, fmt.Sprintf("/attachments/%d", attachment.ID)) {
		t.Fatalf("attachment URL must address its own bytes: %q", attachment.URL)
	}

	// Attachments are content, not a state transition, so the ticket version stays put.
	after := readTicketForTest(t, fixture, project.ID, ticket.ID)
	if after.Version != ticket.Version {
		t.Fatalf("upload changed the ticket version: %d -> %d", ticket.Version, after.Version)
	}

	response = fetch(fixture, fmt.Sprintf("/api/projects/%d/tickets/%d/attachments/%d", project.ID, ticket.ID, attachment.ID))
	requireStatus(t, response, 200)
	if !bytes.Equal(response.Body.Bytes(), image) {
		t.Fatal("served bytes differ from the uploaded image")
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "image/png" {
		t.Fatalf("content type %q", contentType)
	}
	if cache := response.Header().Get("Cache-Control"); !strings.Contains(cache, "immutable") {
		t.Fatalf("image bytes must be cacheable: %q", cache)
	}
	if disposition := response.Header().Get("Content-Disposition"); !strings.HasPrefix(disposition, "inline") {
		t.Fatalf("content disposition %q", disposition)
	}
	etag := response.Header().Get("ETag")
	if etag != `"`+attachment.SHA256+`"` {
		t.Fatalf("etag %q", etag)
	}
	conditional := httptest.NewRequest("GET", "http://docket.test"+fmt.Sprintf("/api/projects/%d/tickets/%d/attachments/%d", project.ID, ticket.ID, attachment.ID), nil)
	conditional.Header.Set("If-None-Match", etag)
	revalidated := httptest.NewRecorder()
	fixture.handler.ServeHTTP(revalidated, conditional)
	requireStatus(t, revalidated, http.StatusNotModified)

	response = call(fixture.handler, "GET", collection, "", 0)
	requireStatus(t, response, 200)
	var listing struct {
		Items []Attachment `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Items) != 1 || listing.Items[0].ID != attachment.ID {
		t.Fatalf("listing %+v", listing.Items)
	}

	history := call(fixture.handler, "GET", fmt.Sprintf("/api/projects/%d/tickets/%d/history", project.ID, ticket.ID), "", 0)
	requireStatus(t, history, 200)
	if !strings.Contains(history.Body.String(), "attachment.created") {
		t.Fatal("history must record the upload")
	}
	if strings.Contains(history.Body.String(), "iVBORw0KGgo") {
		t.Fatal("history must never carry image bytes")
	}

	response = call(fixture.handler, "DELETE", fmt.Sprintf("%s/%d", collection, attachment.ID), "", 0)
	requireStatus(t, response, 200)
	requireStatus(t, fetch(fixture, fmt.Sprintf("%s/%d", collection, attachment.ID)), 404)
	response = call(fixture.handler, "GET", collection, "", 0)
	requireStatus(t, response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Items) != 0 {
		t.Fatalf("deleted image still listed: %+v", listing.Items)
	}
}

func TestAttachmentValidation(t *testing.T) {
	fixture := newFixture(t)
	project := projectForTest(t, fixture, "REJECT")
	ticket := ticketForTest(t, fixture, project.ID)
	collection := fmt.Sprintf("/api/projects/%d/tickets/%d/attachments", project.ID, ticket.ID)
	image := pngFixture(t, 2, 2)

	requireStatus(t, upload(t, fixture, collection, []byte("")), 415)
	requireStatus(t, upload(t, fixture, collection, []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"><script>alert(1)</script></svg>")), 415)
	requireStatus(t, upload(t, fixture, collection, []byte("<html><body>hi</body></html>")), 415)
	// A PNG signature with a truncated body must not be stored.
	requireStatus(t, upload(t, fixture, collection, append([]byte("\x89PNG\r\n\x1a\n"), []byte("not really a png")...)), 400)
	// A GIF signature whose decoded format disagrees is rejected.
	requireStatus(t, upload(t, fixture, collection, append([]byte("GIF89a"), image...)), 400)
	requireStatus(t, upload(t, fixture, collection+"?filename=..%2Fescape.png", image), 400)
	requireStatus(t, upload(t, fixture, collection+"?filename="+strings.Repeat("a", 200)+".png", image), 400)
	requireStatus(t, upload(t, fixture, fmt.Sprintf("/api/projects/%d/tickets/999/attachments", project.ID), image), 404)
	requireStatus(t, upload(t, fixture, fmt.Sprintf("/api/projects/999/tickets/%d/attachments", ticket.ID), image), 404)

	oversized := make([]byte, AttachmentBytesMax+1)
	copy(oversized, image)
	requireStatus(t, upload(t, fixture, collection, oversized), 413)

	for index := 0; index < AttachmentsPerTicketMax; index++ {
		requireStatus(t, upload(t, fixture, collection, image), 201)
	}
	response := upload(t, fixture, collection, image)
	requireStatus(t, response, 409)
	if !strings.Contains(response.Body.String(), "at most 8 images") {
		t.Fatalf("teaching limit missing: %s", response.Body.String())
	}

	// Removing one frees a slot again.
	requireStatus(t, call(fixture.handler, "DELETE", collection+"/1", "", 0), 200)
	requireStatus(t, upload(t, fixture, collection, image), 201)
	requireStatus(t, call(fixture.handler, "DELETE", collection+"/9999", "", 0), 404)
}

func TestAttachmentBytesStayInsideOneDatabaseFile(t *testing.T) {
	fixture := newFixture(t)
	project := projectForTest(t, fixture, "BACKUP")
	ticket := ticketForTest(t, fixture, project.ID)
	image := pngFixture(t, 8, 8)
	requireStatus(t, upload(t, fixture, fmt.Sprintf("/api/projects/%d/tickets/%d/attachments?filename=a.png", project.ID, ticket.ID), image), 201)

	// One-file backups must stay complete: VACUUM INTO carries the image with the rest.
	backupPath := fixture.path + ".backup"
	if err := fixture.store.Backup(t.Context(), backupPath); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := restored.Close(); err != nil {
			t.Error(err)
		}
	}()
	handler, err := NewServer(restored, "http://docket.test")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "http://docket.test"+fmt.Sprintf("/api/projects/%d/tickets/%d/attachments/1", project.ID, ticket.ID), nil))
	requireStatus(t, response, 200)
	if !bytes.Equal(response.Body.Bytes(), image) {
		t.Fatal("backup did not carry the image bytes")
	}
}
