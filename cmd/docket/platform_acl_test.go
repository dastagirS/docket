package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Darwin compiles against actual SDK declarations. Other hosts model only the
// public ACL API; they cannot establish Darwin SDK or filesystem compatibility.
func TestDarwinACLAdapterContract(t *testing.T) {
	data, err := os.ReadFile("platform_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	_, preamble, found := strings.Cut(string(data), "/*\n")
	if !found {
		t.Fatal("Missing Darwin C adapter")
	}
	preamble, _, found = strings.Cut(preamble, "*/")
	if !found {
		t.Fatal("Incomplete Darwin C adapter")
	}
	folder := t.TempDir()
	const header = `
typedef void *acl_t;
typedef void *acl_entry_t;
typedef int acl_type_t;
enum { ACL_TYPE_EXTENDED = 1, ACL_FIRST_ENTRY = 0 };
acl_t acl_get_fd_np(int, acl_type_t);
int acl_get_entry(acl_t, int, acl_entry_t *);
int acl_free(void *);
`
	const driver = `
#include <assert.h>
#include <stdio.h>
enum { ABSENT, EMPTY, CUSTOM, BAD_DESCRIPTOR, ACL_DENIED, ACL_IO_ERROR, ALLOCATION_ERROR, ENTRY_ERROR, FREE_ERROR };
enum { FIXTURE_DESCRIPTOR = 17 };
static int scenario, acl_live;
static void *acl_value;
acl_t acl_get_fd_np(int descriptor, acl_type_t type) {
    assert(descriptor == FIXTURE_DESCRIPTOR && type == ACL_TYPE_EXTENDED);
    if (scenario == ABSENT) { errno = ENOENT; return NULL; }
    if (scenario == BAD_DESCRIPTOR) { errno = EBADF; return NULL; }
    if (scenario == ACL_DENIED) { errno = EACCES; return NULL; }
    if (scenario == ACL_IO_ERROR) { errno = EIO; return NULL; }
    if (scenario == ALLOCATION_ERROR) { errno = ENOMEM; return NULL; }
    assert(!acl_live);
    acl_live = 1;
    return (acl_t)&acl_value;
}
int acl_get_entry(acl_t acl, int entry_id, acl_entry_t *entry) {
    assert(acl == (acl_t)&acl_value && acl_live && entry_id == ACL_FIRST_ENTRY && entry);
    if (scenario == EMPTY) { errno = EINVAL; return -1; }
    if (scenario == ENTRY_ERROR) { errno = EACCES; return -1; }
    *entry = (acl_entry_t)&acl_value;
    return 0;
}
int acl_free(void *acl) {
    assert(acl == &acl_value && acl_live);
    acl_live = 0;
    if (scenario == FREE_ERROR) { errno = EIO; return -1; }
    return 0;
}
int main(void) {
    const struct { const char *name; int scenario; int expected; } cases[] = {
        {"absent ACL", ABSENT, 0}, {"empty ACL", EMPTY, 0}, {"custom ACL", CUSTOM, 1},
        {"bad descriptor", BAD_DESCRIPTOR, -EBADF},
        {"ACL denied", ACL_DENIED, -EACCES}, {"ACL I/O failure", ACL_IO_ERROR, -EIO},
        {"allocation failure", ALLOCATION_ERROR, -ENOMEM},
        {"entry failure", ENTRY_ERROR, -EACCES}, {"release failure", FREE_ERROR, -EIO}
    };
    for (size_t i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
        scenario = cases[i].scenario;
        int actual = docket_has_acl(FIXTURE_DESCRIPTOR);
        if (actual != cases[i].expected) {
            fprintf(stderr, "%s: got %d, expected %d\n", cases[i].name, actual, cases[i].expected);
            return 1;
        }
        assert(!acl_live);
    }
    return 0;
}
`
	if runtime.GOOS != "darwin" {
		if err := os.Mkdir(filepath.Join(folder, "sys"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, "sys", "acl.h"), []byte(header), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(folder, "adapter.c"), []byte(preamble+driver), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	compiler, err := exec.CommandContext(ctx, "go", "env", "CC").Output()
	if err != nil {
		t.Fatal(err)
	}
	arguments := strings.Fields(strings.TrimSpace(string(compiler)))
	if len(arguments) == 0 {
		t.Fatal("Go C compiler is not configured")
	}
	executable := filepath.Join(folder, "adapter")
	command := exec.CommandContext(ctx, arguments[0], append(arguments[1:], "-std=c11", "-Wall", "-Wextra", "-Werror", "-I", folder, filepath.Join(folder, "adapter.c"), "-o", executable)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Compile Darwin ACL contract probe: %v\n%s", err, output)
	}
	if output, err := exec.CommandContext(ctx, executable).CombinedOutput(); err != nil {
		t.Fatalf("Darwin ACL adapter contract failed: %v\n%s", err, output)
	}
}
