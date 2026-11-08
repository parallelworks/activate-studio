//go:build windows

package remote

import "os"

// ownedBy: Windows has no uid (os.Getuid is -1, and SocketDir returns
// before asking), so nothing is owned.
func ownedBy(os.FileInfo, int) bool { return false }
