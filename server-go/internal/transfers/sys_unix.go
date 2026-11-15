//go:build unix

package transfers

import "golang.org/x/sys/unix"

// freeBytes is what an unprivileged writer may still use at p.
func freeBytes(p string) (int64, bool) {
	var st unix.Statfs_t
	if unix.Statfs(p, &st) != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}
