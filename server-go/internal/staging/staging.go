// Package staging names the folders the Studio writes a file in before it
// has its name in a library. Nothing lists, reads or indexes what is in
// them.
package staging

import "strings"

const (
	// Upload starts the name of the folder a large file's parts are written
	// in (internal/kb, internal/userlibs).
	Upload = ".studio-upload-"
	// Transfer starts the name of the folder a transfer is copied to first
	// (internal/transfers).
	Transfer = ".studio-transfer-"
)

// Name reports whether a file or folder of this name is one. Case is
// ignored, as some file systems do.
func Name(name string) bool {
	if len(name) < len(Upload) || name[0] != '.' {
		return false
	}
	n := strings.ToLower(name)
	return strings.HasPrefix(n, Upload) || strings.HasPrefix(n, Transfer)
}

// In reports whether path, its parts parted by slashes, goes through one.
func In(path string) bool {
	for part := range strings.SplitSeq(path, "/") {
		if Name(part) {
			return true
		}
	}
	return false
}
