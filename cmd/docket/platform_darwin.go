package main

/*
#include <sys/types.h>
#include <sys/acl.h>
#include <sys/stat.h>
#include <errno.h>
#include <stdlib.h>

static int docket_has_acl(const char *path) {
    filesec_t security = filesec_init();
    if (security == NULL) return -errno;
    struct stat status;
    if (lstatx_np(path, &status, security) != 0) {
        int error = errno;
        filesec_free(security);
        return -error;
    }
    // ENOENT means no ACL only after a successful file-security snapshot.
    acl_t acl = NULL;
    int fetched = filesec_get_property(security, FILESEC_ACL, &acl);
    int error = errno;
    filesec_free(security);
    if (fetched != 0) return error == ENOENT ? 0 : -error;
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
