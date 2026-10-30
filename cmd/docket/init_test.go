package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"docket/internal/docket"
)

func TestCLIInitNonTerminalCharacterInputRequiresExplicitChoice(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	store, err := docket.Open(filepath.Join(t.TempDir(), "init.sqlite"))
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
	var output, diagnostics bytes.Buffer
	if code := execute([]string{"project", "init", "--server", server.URL, "--name", "Fixture", "--key", "FIXTURE"}, strings.NewReader(""), &output, &diagnostics); code != 0 {
		t.Fatalf("init: %d %s", code, diagnostics.String())
	}
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := input.Close(); err != nil {
			t.Error(err)
		}
	}()
	output.Reset()
	diagnostics.Reset()
	if code := execute([]string{"project", "init"}, input, &output, &diagnostics); code != exitUsage {
		t.Fatalf("Repeat init without explicit choice: %d", code)
	}
	if !json.Valid(diagnostics.Bytes()) || output.Len() != 0 {
		t.Fatalf("Non-terminal character input must not produce interactive prompts: %s", diagnostics.String())
	}
}

func TestCLIInitHelpIsOfflineAndReportsOutputFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	t.Setenv("DOCKET_URL", "not a URL")
	var diagnostics bytes.Buffer
	if code := execute([]string{"project", "init", "--help"}, strings.NewReader(""), brokenWriter{}, &diagnostics); code != exitFailure {
		t.Fatalf("Help output failure: %d %s", code, diagnostics.String())
	}
	if !json.Valid(diagnostics.Bytes()) {
		t.Fatal("Help failure must be structured JSON")
	}
	if entries, err := os.ReadDir("."); err != nil || len(entries) != 0 {
		t.Fatal("Init help created local state")
	}
}

func TestCLIFolderMarkerRejectsInvalidMetadata(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), markerName)
	for _, payload := range []string{
		`null`, `[]`, `{}`, `{"version":2,"project_url":"http://127.0.0.1:0/projects/1","project_key":"TEST","project_created_at":"2026-01-01T00:00:00Z"}`,
		`{"version":1,"project_url":"http://user:password@127.0.0.1:0/projects/1","project_key":"TEST","project_created_at":"2026-01-01T00:00:00Z"}`,
		`{"version":1,"project_url":"http://127.0.0.1:0/projects/1?secret=no","project_key":"TEST","project_created_at":"2026-01-01T00:00:00Z"}`,
		`{"version":1,"project_url":"http://127.0.0.1:0/projects/1","project_key":"TEST","project_created_at":"invalid"}`,
		`{"version":1,"project_url":"http://127.0.0.1:0/projects/1","project_key":"TEST","project_created_at":"2026-01-01T00:00:00Z","unknown":true}`,
		strings.Repeat(" ", markerBytesMax+1),
	} {
		if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
			t.Fatal(err)
		}
		if binding, err := readBinding(path); err == nil || binding != nil {
			t.Fatal("Invalid marker was accepted")
		}
	}
}
