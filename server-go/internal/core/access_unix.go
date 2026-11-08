//go:build !windows

package core

import "syscall"

// writable is fs.accessSync(dir, W_OK).
func writable(p string) bool { return syscall.Access(p, 2) == nil }
