//go:build !windows

package settings

import (
	"os"
	"syscall"
)

func inode(st os.FileInfo) uint64 {
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		return uint64(sys.Ino)
	}
	return 0
}
