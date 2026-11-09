package settings

import "os"

// Windows has no inode in os.FileInfo; size and modification time decide.
func inode(os.FileInfo) uint64 { return 0 }
