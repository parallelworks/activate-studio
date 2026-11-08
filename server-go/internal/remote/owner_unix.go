//go:build !windows

package remote

import (
	"os"
	"syscall"
)

// ownedBy says whether uid owns the file st describes.
func ownedBy(st os.FileInfo, uid int) bool {
	sys, ok := st.Sys().(*syscall.Stat_t)
	return ok && int(sys.Uid) == uid
}
