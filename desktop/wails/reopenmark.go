package main

import (
	"os"
	"path/filepath"
)

// clearReopenMark removes updates/reopen, which an update leaves for the
// window to be reopened.
func clearReopenMark(dataDir string) {
	_ = os.Remove(filepath.Join(dataDir, "updates", "reopen"))
}
