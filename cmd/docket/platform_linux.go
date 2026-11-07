package main

import (
	"errors"
	"syscall"
)

const terminalReadRequest = syscall.TCGETS

// Replacement creates a new inode; silently dropping ACLs or capabilities would change access policy.
func checkUpdatePermissions(path string) error {
	for _, attribute := range []string{"security.capability", "system.posix_acl_access"} {
		if _, err := syscall.Getxattr(path, attribute, nil); err == nil || (!errors.Is(err, syscall.ENODATA) && !errors.Is(err, syscall.EOPNOTSUPP)) {
			return errors.New("Updater refuses ACL/capability-protected files or unreadable security metadata")
		}
	}
	return nil
}
