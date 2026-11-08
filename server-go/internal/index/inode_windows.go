//go:build windows

package index

import "os"

// inodeOf is 0 on Windows, where os.Stat does not read the file index.
func inodeOf(os.FileInfo) uint64 { return 0 }
