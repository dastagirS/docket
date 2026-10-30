package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"docket/internal/docket"
)

func TestCLIParsePreservesPatchFieldsAndScope(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DOCKET_URL", "http://127.0.0.1:9999")
	command, help, err := parseCommand([]string{"ticket", "update", "42", "--project", "3", "--version", "7", "--assignee", ""}, strings.NewReader(""))
	if err != nil || help != "" {
		t.Fatalf("parse failed: %v", err)
	}
	if command.path != "/api/projects/3/tickets/42" || command.method != "PATCH" || command.version != 7 || command.server != "http://127.0.0.1:9999" {
		t.Fatalf("wrong request: %+v", command)
	}
	if string(command.body) != `{"assignee":""}` {
		t.Fatalf("Omitted fields must not be sent: %s", command.body)
	}
	command, _, err = parseCommand([]string{"ticket", "list", "--project", "3", "--parent", "42", "--q", "a&b + c", "--server", "http://127.0.0.1:9998"}, strings.NewReader(""))
	if err != nil || !strings.Contains(command.path, "q=a%26b+%2B+c") || !strings.Contains(command.path, "parent_id=42") || command.server != "http://127.0.0.1:9998" {
		t.Fatalf("query/config error: %+v, %v", command, err)
	}
}

func TestCLIRejectsInvalidInputsWithoutDatabase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	t.Setenv("DOCKET_URL", "http://127.0.0.1:0")
	t.Setenv("DOCKET_ACTOR", "")
	cases := [][]string{
		{"ticket", "get", "1"},
		{"ticket", "update", "1", "--project", "1", "--title", "No version"},
		{"ticket", "update", "1", "--project", "1", "--version", "1", "--parent", "2"},
		{"ticket", "create", "--project", "1", "--title", "x", "--input", "-"},
		{"ticket", "list", "--project", "1", "--parent", "0"},
		{"ticket", "list", "--project", "1", "--limit", "101"},
		{"ticket", "list", "--project", "1", "--after", "-1"},
		{"project", "get", "bad"},
		{"project", "list", "extra"},
		{"doctor", "--json=false"},
		{"unknown"},
		{"serve=invalid"},
	}
	for _, arguments := range cases {
		var output, diagnostics bytes.Buffer
		if code := execute(arguments, strings.NewReader(`{}`), &output, &diagnostics); code != exitUsage {
			t.Fatalf("%v: exit %d, %s", arguments, code, diagnostics.String())
		}
		if output.Len() != 0 || !json.Valid(diagnostics.Bytes()) {
			t.Fatal("Usage errors must only emit structured stderr")
		}
	}
	for _, arguments := range [][]string{nil, {"--help"}, {"ticket", "update", "--help"}} {
		var output, diagnostics bytes.Buffer
		if execute(arguments, strings.NewReader(""), &output, &diagnostics) != 0 || output.Len() == 0 || diagnostics.Len() != 0 {
			t.Fatal("Help must succeed without side effects")
		}
	}
	if _, err := os.Stat("data"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("CLI/help must not initialize a local database")
	}
}

func TestCLIJSONInputBoundsAndUTF8(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	arguments := []string{"ticket", "create", "--project", "1", "--input", "-"}
	for _, body := range []string{"null", "[]", `{}`, `{"title":"x"} {}`, string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}), strings.Repeat(" ", requestBytesMax+1)} {
		if _, _, err := parseCommand(arguments, strings.NewReader(body)); err == nil {
			t.Fatal("Invalid or oversized JSON input accepted")
		}
	}
	command, _, err := parseCommand(arguments, strings.NewReader("{\"title\":\"first\\nsecond\"}"))
	if err != nil || !strings.Contains(string(command.body), `first\nsecond`) {
		t.Fatalf("Multiline JSON input failed: %v", err)
	}
	path := filepath.Join(t.TempDir(), "body.json")
	if err := os.WriteFile(path, []byte(`{"title":"From file"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseCommand([]string{"ticket", "create", "--project", "1", "--input", path}, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	if _, err := readCommandInput(filepath.Dir(path), strings.NewReader(""), requestTimeoutDefault); err == nil {
		t.Fatal("Directory accepted as JSON input")
	}
}

func TestCLIRequestsUseRealApplication(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	store, err := docket.Open(filepath.Join(t.TempDir(), "cli.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	server := httptest.NewUnstartedServer(nil)
	handler, err := docket.NewServer(store, "http://"+server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	defer server.Close()
	t.Setenv("DOCKET_URL", server.URL)
	t.Setenv("DOCKET_ACTOR", "CLI test")
	var output, diagnostics bytes.Buffer
	if code := execute([]string{"project", "create", "--name", "Fixture", "--key", "CLI"}, strings.NewReader(""), &output, &diagnostics); code != 0 {
		t.Fatalf("create: %d %s", code, diagnostics.String())
	}
	var project docket.Project
	if err := json.Unmarshal(output.Bytes(), &project); err != nil || project.ID <= 0 {
		t.Fatalf("Invalid project result: %v", err)
	}
	output.Reset()
	if code := execute([]string{"ticket", "create", "--project", fmt.Sprint(project.ID), "--title", "Original"}, strings.NewReader(""), &output, &diagnostics); code != 0 {
		t.Fatalf("create ticket: %d %s", code, diagnostics.String())
	}
	var ticket docket.Ticket
	if err := json.Unmarshal(output.Bytes(), &ticket); err != nil || ticket.Version != 1 {
		t.Fatalf("Invalid ticket result: %v", err)
	}
	output.Reset()
	arguments := []string{"ticket", "update", fmt.Sprint(ticket.ID), "--project", fmt.Sprint(project.ID), "--version", "1", "--title", "Changed"}
	if execute(arguments, strings.NewReader(""), &output, &diagnostics) != 0 {
		t.Fatal(diagnostics.String())
	}
	output.Reset()
	if code := execute(arguments, strings.NewReader(""), &output, &diagnostics); code != exitConflict || output.Len() != 0 {
		t.Fatalf("Stale mutation not rejected: %d %s", code, diagnostics.String())
	}
	diagnostics.Reset()
	if code := execute([]string{"ticket", "create", "--project", fmt.Sprint(project.ID), "--title", "Committed with broken stdout"}, strings.NewReader(""), brokenWriter{}, &diagnostics); code != exitUncertain {
		t.Fatalf("Lost stdout must not report write success: %d", code)
	}
	output.Reset()
	if execute([]string{"ticket", "list", "--project", fmt.Sprint(project.ID)}, strings.NewReader(""), &output, &diagnostics) != 0 {
		t.Fatal("Cannot reconcile output failure")
	}
	var page struct {
		Items []docket.Ticket `json:"items"`
	}
	if err := json.Unmarshal(output.Bytes(), &page); err != nil || len(page.Items) != 2 {
		t.Fatal("Output failure replayed or lost the committed write")
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("test output unavailable") }

func TestCLITransportErrorsAreBoundedAndNeverRetried(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DOCKET_ACTOR", "")
	for _, scenario := range []struct {
		name, payload    string
		status, expected int
		method           string
	}{
		{"redirect", `{}`, 302, exitFailure, "GET"},
		{"write redirect", `{}`, 302, exitUncertain, "POST"},
		{"invalid UTF-8", string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}), 200, exitFailure, "GET"},
		{"not found", `{"error":"Missing"}`, 404, exitNotFound, "GET"},
		{"busy", `{"error":"Busy"}`, 429, exitUnavailable, "GET"},
		{"bad success", `<html>not API</html>`, 200, exitFailure, "GET"},
		{"uncertain success", `<html>lost result</html>`, 200, exitUncertain, "POST"},
		{"uncertain upstream", `{"error":"Interrupted"}`, 502, exitUncertain, "POST"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				writer.Header().Set("Location", "/redirect-target")
				writer.Header().Set("Retry-After", "60")
				writer.WriteHeader(scenario.status)
				if _, err := io.WriteString(writer, scenario.payload); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			_, err := (apiCommand{server: server.URL, method: scenario.method, path: "/proof", timeout: requestTimeoutDefault}).request()
			var failure *cliFailure
			if !errors.As(err, &failure) || failure.Exit != scenario.expected || requests.Load() != 1 {
				t.Fatalf("Wrong error/retry behavior: %v, requests=%d", err, requests.Load())
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-request.Context().Done():
			return
		case <-timer.C:
		}
		if _, err := io.WriteString(writer, `{"status":"ok"}`); err != nil {
			return
		}
	}))
	defer server.Close()
	_, err := (apiCommand{server: server.URL, method: "GET", path: "/", timeout: time.Millisecond}).request()
	var failure *cliFailure
	if !errors.As(err, &failure) || failure.Exit != exitUnavailable {
		t.Fatal("Read timeout was not reported")
	}
	for _, origin := range []string{"file:///tmp/db", "http://user:password@localhost", "http://localhost/path", "http://localhost?token=example", "http://localhost#fragment"} {
		if _, err := (apiCommand{server: origin, method: "GET", path: "/healthz", timeout: requestTimeoutDefault}).request(); err == nil {
			t.Fatal("Unsafe origin accepted")
		}
	}
}

func TestCLIBrokenOutputDoesNotReportSuccess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var diagnostics bytes.Buffer
	if execute([]string{"--help"}, strings.NewReader(""), brokenWriter{}, &diagnostics) != exitFailure {
		t.Fatal("Broken stdout reported success")
	}
	if emitFailure(brokenWriter{}, errors.New("usage")) != exitFailure {
		t.Fatal("Broken stderr was not handled")
	}
}

func TestLegacyAndExplicitBackupModesRemainCompatible(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, explicit := range []bool{false, true} {
		directory := t.TempDir()
		arguments := []string{"--database", filepath.Join(directory, "db.sqlite"), "--backup-to", filepath.Join(directory, "backup.sqlite")}
		if explicit {
			arguments = append([]string{"serve"}, arguments...)
		}
		var output, diagnostics bytes.Buffer
		if code := execute(arguments, strings.NewReader(""), &output, &diagnostics); code != 0 {
			t.Fatalf("Backup mode failed: %d %s", code, diagnostics.String())
		}
		if info, err := os.Stat(filepath.Join(directory, "backup.sqlite")); err != nil || info.Size() == 0 {
			t.Fatal("Backup mode did not produce a database")
		}
	}
}

func TestCLIInputRejectsFIFOWithoutBlocking(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "input.pipe")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCommandInput(path, strings.NewReader(""), requestTimeoutDefault); err == nil {
		t.Fatal("FIFO input must not be opened as a blocking regular file")
	}
}

func TestCLIResponseSizeAndHealthValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/healthz" {
			if _, err := io.WriteString(writer, `{"status":"not-docket"}`); err != nil {
				t.Error(err)
			}
			return
		}
		if _, err := io.WriteString(writer, strings.Repeat("x", responseBytesMax+1)); err != nil {
			return
		}
	}))
	defer server.Close()
	for _, path := range []string{"/oversized", "/healthz"} {
		_, err := (apiCommand{server: server.URL, method: "GET", path: path, timeout: requestTimeoutDefault}).request()
		var failure *cliFailure
		if !errors.As(err, &failure) || failure.Code != "invalid_response" {
			t.Fatalf("Unexpected response accepted: %v", err)
		}
	}
}
