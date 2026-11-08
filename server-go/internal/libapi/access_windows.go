//go:build windows

package libapi

import "os"

// writable: Windows has no access(2); a folder that opens is taken as
// writable unless it is read only.
func writable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().Perm()&0o200 != 0
}

// readable: a folder that lists is readable.
func readable(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	_, err = f.Readdirnames(1)
	return err == nil || err.Error() == "EOF"
}
