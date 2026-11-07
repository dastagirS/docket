package main

/*
#include <sys/acl.h>
#include <errno.h>
#include <stddef.h>

static int docket_has_acl(int descriptor) {
    // The caller holds an open file, so ENOENT cannot mean a missing path.
    acl_t acl = acl_get_fd_np(descriptor, ACL_TYPE_EXTENDED);
    if (acl == NULL) return errno == ENOENT ? 0 : -errno;
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
)

const terminalReadRequest = syscall.TIOCGETA

// Renaming a new inode must not silently discard an existing macOS ACL or file flags.
func checkUpdatePermissions(path string) (result error) {
	// A swapped-in FIFO must not block inspection before its type is checked.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, file.Close()) }()
	information, err := file.Stat()
	if err != nil {
		return err
	}
	status, ok := information.Sys().(*syscall.Stat_t)
	if !ok || !information.Mode().IsRegular() || status.Flags != 0 {
		return errors.New("Updater requires a regular macOS file without protected file flags")
	}
	if C.docket_has_acl(C.int(file.Fd())) != 0 {
		return errors.New("Updater refuses custom macOS ACLs or unreadable security metadata")
	}
	return nil
}
