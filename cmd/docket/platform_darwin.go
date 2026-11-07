package main

/*
#include <sys/types.h>
#include <sys/acl.h>
#include <errno.h>
#include <stdlib.h>

static int docket_has_acl(const char *path) {
    acl_t acl = acl_get_file(path, ACL_TYPE_EXTENDED);
    if (acl == NULL) return -errno;
    acl_entry_t entry;
    int found = acl_get_entry(acl, ACL_FIRST_ENTRY, &entry);
    int result = found == 0 ? 1 : (errno == EINVAL ? 0 : -errno);
    if (acl_free(acl) != 0) return -errno;
    return result;
}
*/
import "C"

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

const terminalReadRequest = syscall.TIOCGETA

// Renaming a new inode must not silently discard an existing macOS ACL or file flags.
func checkUpdatePermissions(path string) error {
	information, err := os.Lstat(path)
	if err != nil {
		return err
	}
	status, ok := information.Sys().(*syscall.Stat_t)
	if !ok || status.Flags != 0 {
		return errors.New("Updater refuses macOS files with protected file flags")
	}
	location := C.CString(path)
	defer C.free(unsafe.Pointer(location))
	if C.docket_has_acl(location) != 0 {
		return errors.New("Updater refuses custom macOS ACLs or unreadable security metadata")
	}
	return nil
}
