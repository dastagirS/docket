package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This exercises the actual C adapter against a model of Apple's API contracts;
// it does not substitute for the native Darwin filesystem tests.
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
	if err := os.Mkdir(filepath.Join(folder, "sys"), 0o700); err != nil {
		t.Fatal(err)
	}
	const header = `
struct stat;
typedef void *acl_t;
typedef void *acl_entry_t;
typedef void *filesec_t;
enum { ACL_TYPE_EXTENDED = 1, ACL_FIRST_ENTRY = 0, FILESEC_ACL = 2 };
acl_t acl_get_file(const char *, int);
int acl_get_entry(acl_t, int, acl_entry_t *);
int acl_free(void *);
filesec_t filesec_init(void);
void filesec_free(filesec_t);
int lstatx_np(const char *, struct stat *, filesec_t);
int filesec_get_property(filesec_t, int, void *);
`
	const driver = `
#include <assert.h>
#include <stdio.h>
enum { ABSENT, EMPTY, CUSTOM, MISSING, STAT_DENIED, ACL_DENIED, ACL_IO_ERROR, ALLOCATION_ERROR };
static int scenario, security_live, acl_live;
static int security_value, acl_value;
filesec_t filesec_init(void) {
    if (scenario == ALLOCATION_ERROR) { errno = ENOMEM; return NULL; }
    assert(!security_live);
    security_live = 1;
    return &security_value;
}
void filesec_free(filesec_t security) {
    assert(security == &security_value && security_live);
    security_live = 0;
}
int lstatx_np(const char *path, struct stat *status, filesec_t security) {
    assert(path && status && security == &security_value && security_live);
    if (scenario == MISSING) { errno = ENOENT; return -1; }
    if (scenario == STAT_DENIED) { errno = EACCES; return -1; }
    return 0;
}
acl_t acl_get_file(const char *path, int type) {
    assert(path && type == ACL_TYPE_EXTENDED);
    if (scenario == ABSENT || scenario == MISSING) { errno = ENOENT; return NULL; }
    if (scenario == STAT_DENIED || scenario == ACL_DENIED) { errno = EACCES; return NULL; }
    if (scenario == ACL_IO_ERROR) { errno = EIO; return NULL; }
    if (scenario == ALLOCATION_ERROR) { errno = ENOMEM; return NULL; }
    assert(!acl_live);
    acl_live = 1;
    return &acl_value;
}
int filesec_get_property(filesec_t security, int property, void *value) {
    assert(security == &security_value && security_live && property == FILESEC_ACL && value);
    acl_t acl = acl_get_file("fixture", ACL_TYPE_EXTENDED);
    if (!acl) return -1;
    *(acl_t *)value = acl;
    return 0;
}
int acl_get_entry(acl_t acl, int entry_id, acl_entry_t *entry) {
    assert(acl == &acl_value && acl_live && entry_id == ACL_FIRST_ENTRY && entry);
    if (scenario == EMPTY) { errno = EINVAL; return -1; }
    *entry = &acl_value;
    return 0;
}
int acl_free(void *acl) {
    assert(acl == &acl_value && acl_live);
    acl_live = 0;
    return 0;
}
int main(void) {
    const struct { const char *name; int scenario; int expected; } cases[] = {
        {"absent ACL", ABSENT, 0}, {"empty ACL", EMPTY, 0}, {"custom ACL", CUSTOM, 1},
        {"missing file", MISSING, -ENOENT}, {"stat denied", STAT_DENIED, -EACCES},
        {"ACL denied", ACL_DENIED, -EACCES}, {"ACL I/O failure", ACL_IO_ERROR, -EIO},
        {"allocation failure", ALLOCATION_ERROR, -ENOMEM}
    };
    for (size_t i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
        scenario = cases[i].scenario;
        int actual = docket_has_acl("fixture");
        if (actual != cases[i].expected) {
            fprintf(stderr, "%s: got %d, expected %d\n", cases[i].name, actual, cases[i].expected);
            return 1;
        }
        assert(!security_live && !acl_live);
    }
    return 0;
}
`
	if err := os.WriteFile(filepath.Join(folder, "sys", "acl.h"), []byte(header), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "sys", "stat.h"), []byte("struct stat { int unused; };\n"), 0o600); err != nil {
		t.Fatal(err)
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
