//go:build windows

package posixaccess

import "os"

func runsAsRoot() bool { return false }

// Shared is true on Windows, where an index folder does not carry its
// source folder's owner and mode.
func Shared(dir, file os.FileInfo) bool { return true }
