package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryDiscoveryIsReadOnlyAndUsesPrivateHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	binding, _, err := discoverBinding()
	if err != nil || binding != nil {
		t.Fatalf("Unexpected default binding: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, markerName)); !os.IsNotExist(err) {
		t.Fatal("Ordinary discovery created registry state")
	}
	path, err := registryLocation(true)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("Home registry must be private")
	}
}

func TestRegistryRejectsCorruptionAndDuplicateFolders(t *testing.T) {
	path := filepath.Join(t.TempDir(), registryName)
	binding := folderBinding{Version: markerVersion, Folder: filepath.Join(t.TempDir(), "deleted-folder"), ProjectURL: "http://127.0.0.1:0/projects/1", ProjectKey: "TEST", ProjectCreatedAt: "2026-01-01T00:00:00Z"}
	valid, err := json.Marshal(folderRegistry{Version: markerVersion, Bindings: []folderBinding{binding}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, valid, 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := readRegistry(path)
	if err != nil || len(registry.Bindings) != 1 {
		t.Fatal("Deleted folder entries must not break other registered folders")
	}
	duplicated, err := json.Marshal(folderRegistry{Version: markerVersion, Bindings: []folderBinding{binding, binding}})
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range [][]byte{[]byte(`null`), []byte(`{}`), []byte(`{"version":1,"bindings":null}`), []byte(`{"version":2,"bindings":[]}`), []byte(`{"version":1,"bindings":[],"unknown":true}`), duplicated, []byte(strings.Repeat(" ", registryBytesMax+1))} {
		if err := os.WriteFile(path, payload, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readRegistry(path); err == nil {
			t.Fatal("Invalid registry was accepted")
		}
	}
}

func TestRegistryRefusesExistingHomeFilesAndSymlinks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, markerName)
	original := []byte("existing user configuration")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := registryLocation(true); err == nil {
		t.Fatal("Existing home file was overwritten")
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != string(original) {
		t.Fatal("Existing home configuration changed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := registryLocation(true); err == nil {
		t.Fatal("Symlinked registry directory was accepted")
	}
}

func TestLegacyCleanupPreservesConcurrentMarkerEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), markerName)
	first := `{"version":1,"project_url":"http://127.0.0.1:0/projects/1","project_key":"FIRST","project_created_at":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(first), 0600); err != nil {
		t.Fatal(err)
	}
	binding, err := readBinding(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(first, "FIRST", "OTHER", 1)
	if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeLegacyBinding(binding); err == nil {
		t.Fatal("Concurrent marker edit was ignored")
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != changed {
		t.Fatal("Concurrent marker edit was deleted or overwritten")
	}
}
