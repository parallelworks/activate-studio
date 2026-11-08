//go:build !windows

package platform

import "syscall"

// executable is fs.accessSync(f, X_OK): the file may be run by this user.
func executable(f string) bool { return syscall.Access(f, 0x1) == nil }
