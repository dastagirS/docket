package main

import (
	"bytes"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"docket/skills"
)

func TestBundledSkillDiscoveryIsLocalAndExact(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("DOCKET_URL", "http://user:private-password@127.0.0.1:0")
	for _, document := range []string{"SKILL.md", "references/commands.md", "references/safety.md"} {
		arguments := []string{"--skill"}
		if strings.HasPrefix(document, "references/") {
			arguments = append(arguments, "--references", strings.TrimPrefix(document, "references/"))
		}
		var output, diagnostics bytes.Buffer
		if code := execute(arguments, strings.NewReader(""), &output, &diagnostics); code != 0 {
			t.Fatalf("skill failed: %d %s", code, diagnostics.String())
		}
		expected, err := fs.ReadFile(skills.Docket, "docket-cli/"+document)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(output.Bytes(), expected) || diagnostics.Len() != 0 {
			t.Fatal("Skill output differs from bundled source")
		}
	}
	var diagnostics bytes.Buffer
	if execute([]string{"--skill"}, strings.NewReader(""), brokenWriter{}, &diagnostics) != exitFailure {
		t.Fatal("Broken skill stdout reported success")
	}
}

func TestSkillReferenceSelectionAndBounds(t *testing.T) {
	one := fstest.MapFS{"docket-cli/SKILL.md": &fstest.MapFile{Data: []byte("main")}, "docket-cli/references/only.md": &fstest.MapFile{Data: []byte("single reference")}}
	payload, err := skillOutput([]string{"--references"}, one)
	if err != nil || string(payload) != "single reference" {
		t.Fatal("A sole reference should print without a filename")
	}
	multiple := fstest.MapFS{"docket-cli/references/alpha.md": &fstest.MapFile{Data: []byte("alpha")}, "docket-cli/references/beta.md": &fstest.MapFile{Data: []byte("beta")}}
	payload, err = skillOutput([]string{"--references"}, multiple)
	if err != nil || !strings.Contains(string(payload), "alpha.md") || !strings.Contains(string(payload), "beta.md") {
		t.Fatal("Multiple references must be discoverable")
	}
	for _, name := range []string{"../SKILL.md", "/etc/passwd", "references/alpha.md", "..\\SKILL.md", "missing.md", ""} {
		if _, err := skillOutput([]string{"--references", name}, multiple); err == nil {
			t.Fatal("Invalid reference accepted")
		}
	}
	if _, err := skillOutput([]string{"--references", "alpha.md", "extra"}, multiple); err == nil {
		t.Fatal("Extra arguments accepted")
	}
	if _, err := skillOutput([]string{"--server", "http://127.0.0.1:0"}, multiple); err == nil {
		t.Fatal("Network flags must not turn skill discovery into an API command")
	}
	oversized := fstest.MapFS{"docket-cli/SKILL.md": &fstest.MapFile{Data: bytes.Repeat([]byte("x"), skillBytesMax+1)}}
	_, err = skillOutput(nil, oversized)
	var failure *cliFailure
	if !errors.As(err, &failure) || failure.Exit != exitFailure {
		t.Fatal("Oversized skill was not bounded")
	}
}
