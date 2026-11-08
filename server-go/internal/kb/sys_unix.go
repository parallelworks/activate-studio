//go:build unix

package kb

import (
	"fmt"
	"os"
	"syscall"
)

// fileStamp changes when a file is replaced or written: size,
// modification time and inode, "none" when it is not there.
func fileStamp(f string) string {
	st, err := os.Stat(f)
	if err != nil {
		return "none"
	}
	var ino uint64
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		ino = uint64(s.Ino)
	}
	return fmt.Sprintf("%d:%d:%d", st.Size(), st.ModTime().UnixNano(), ino)
}

// inodeOfInfo is a FileInfo's inode.
func inodeOfInfo(st os.FileInfo) uint64 {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return uint64(s.Ino)
	}
	return 0
}

// processUmask is the umask at start, read once (setting it back at once).
var processUmask = func() os.FileMode {
	m := syscall.Umask(0)
	syscall.Umask(m)
	return os.FileMode(m)
}()

// umask is the mode bits a new file does not get, as Node's writeFile
// leaves them to the process umask.
func umask() os.FileMode { return processUmask }
