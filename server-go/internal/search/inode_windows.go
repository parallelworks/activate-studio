//go:build windows

package search

import "os"

// inodeOf: no inode number from a Stat on Windows, so no overlay entry
// matches, as on a file system without them.
func inodeOf(os.FileInfo) (uint64, bool) { return 0, false }
