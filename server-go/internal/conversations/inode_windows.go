//go:build windows

package conversations

import "os"

func inode(os.FileInfo) string { return "" }
