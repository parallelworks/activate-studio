package kb

import (
	"errors"

	"golang.org/x/sys/unix"
)

// xattrsSupported: labels go on the files as user.studio.tags where the
// filesystem takes user xattrs.
const xattrsSupported = true

// getXattr is getfattr --only-values: the value, or ok false when the
// file has none or it cannot be read.
func getXattr(abs, name string) (string, bool) {
	buf := make([]byte, 4096)
	for {
		n, err := unix.Getxattr(abs, name, buf)
		if errors.Is(err, unix.ERANGE) {
			buf = make([]byte, len(buf)*4)
			continue
		}
		if err != nil {
			return "", false
		}
		return string(buf[:n]), true
	}
}

// setXattr is setfattr -n name -v value: false when the filesystem refuses
// (NFS and EFS answer ENOTSUP), which sends the label to the overlay.
func setXattr(abs, name, value string) bool {
	return unix.Setxattr(abs, name, []byte(value), 0) == nil
}

// removeXattr is setfattr -x; like Node, a failure is not reported.
func removeXattr(abs, name string) { unix.Removexattr(abs, name) }
