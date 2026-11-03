//go:build !windows

package main

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive lock on f without waiting; the kernel drops it
// when the process ends.
func lockFile(f *os.File) bool {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}
