package kb

import (
	"strconv"

	"golang.org/x/sys/unix"
)

// identity is a path's fsid.inode, creation time in nanoseconds and kind,
// as Node's stat with bigint reports them.
func identity(abs string) (key, btime string, dir bool, ok bool) {
	var st unix.Stat_t
	if err := unix.Stat(abs, &st); err != nil {
		return "", "", false, false
	}
	bt := strconv.FormatInt(st.Btim.Sec*1_000_000_000+st.Btim.Nsec, 10)
	return strconv.FormatUint(uint64(int64(st.Dev)), 10) + "." + strconv.FormatUint(st.Ino, 10), bt, st.Mode&unix.S_IFMT == unix.S_IFDIR, true
}
