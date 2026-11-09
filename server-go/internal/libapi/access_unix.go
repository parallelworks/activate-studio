//go:build !windows

package libapi

import "syscall"

// writable is fs.accessSync(p, W_OK).
func writable(p string) bool { return syscall.Access(p, 2) == nil }

// readable is fs.accessSync(p, R_OK | X_OK).
func readable(p string) bool { return syscall.Access(p, 4|1) == nil }
