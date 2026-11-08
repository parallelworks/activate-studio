//go:build !windows

package search

import (
	"os"
	"syscall"
)

// inodeOf is the file's inode number.
func inodeOf(st os.FileInfo) (uint64, bool) {
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(sys.Ino), true
}
