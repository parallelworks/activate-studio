//go:build !windows

package libraries

import (
	"os"
	"syscall"
)

func inode(st os.FileInfo) uint64 {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return s.Ino
	}
	return 0
}
