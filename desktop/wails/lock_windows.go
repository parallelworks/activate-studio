package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive lock on f without waiting; Windows drops it
// when the process ends.
func lockFile(f *os.File) bool {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol) == nil
}
