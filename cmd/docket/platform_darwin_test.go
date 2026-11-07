package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestDarwinUpdateACLPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "docket")
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Explicitly remove inherited entries so the baseline tests an absent ACL.
	if output, err := exec.CommandContext(ctx, "/bin/chmod", "-N", path).CombinedOutput(); err != nil {
		t.Fatalf("Remove fixture ACL: %v %s", err, output)
	}
	if err := checkUpdatePermissions(path); err != nil {
		t.Fatalf("An ordinary file without an ACL must be allowed: %v", err)
	}
	if output, err := exec.CommandContext(ctx, "/bin/chmod", "+a", "everyone allow read", path).CombinedOutput(); err != nil {
		t.Fatalf("Set fixture ACL: %v %s", err, output)
	}
	if err := checkUpdatePermissions(path); err == nil {
		t.Fatal("A file with a custom ACL must be rejected")
	}
	if output, err := exec.CommandContext(ctx, "/bin/chmod", "-N", path).CombinedOutput(); err != nil {
		t.Fatalf("Remove fixture ACL: %v %s", err, output)
	}
	if err := checkUpdatePermissions(path); err != nil {
		t.Fatalf("Removing the custom ACL must restore eligibility: %v", err)
	}
	if err := checkUpdatePermissions(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("A missing file must not be treated as an absent ACL")
	}
}
