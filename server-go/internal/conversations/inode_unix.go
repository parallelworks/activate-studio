//go:build !windows

package conversations

import (
	"os"
	"strconv"
	"syscall"
)

func inode(st os.FileInfo) string {
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		return strconv.FormatUint(uint64(sys.Ino), 10)
	}
	return ""
}
