//go:build windows

package core

import "os"

// writable: Windows has no access(2); a folder that opens is taken as writable
// unless it is read only.
func writable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().Perm()&0o200 != 0
}
