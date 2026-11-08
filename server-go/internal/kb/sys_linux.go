package kb

import (
	"strconv"

	"golang.org/x/sys/unix"
)

// identity is a path's fsid.inode, creation time in nanoseconds ("0" where
// the filesystem has none) and kind, as Node's stat with bigint reports
// them (statx: st_dev is makedev of the device's major and minor).
func identity(abs string) (key, btime string, dir bool, ok bool) {
	var sx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, abs, 0, unix.STATX_BASIC_STATS|unix.STATX_BTIME, &sx); err != nil {
		return "", "", false, false
	}
	dev := unix.Mkdev(sx.Dev_major, sx.Dev_minor)
	bt := "0"
	if sx.Mask&unix.STATX_BTIME != 0 {
		bt = strconv.FormatInt(sx.Btime.Sec*1_000_000_000+int64(sx.Btime.Nsec), 10)
	}
	return strconv.FormatUint(dev, 10) + "." + strconv.FormatUint(sx.Ino, 10), bt, sx.Mode&unix.S_IFMT == unix.S_IFDIR, true
}
