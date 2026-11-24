// Package pwstop reads Stop using the pw CLI (Settings, Accounts) for the
// parts of the Studio that read the computer's ACTIVATE credentials
// without the platform package: the stand-in library keys and image
// descriptions. The platform package keeps the same flag in access.json.
package pwstop

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// In says the Studio whose index base is indexBase was told to stop using
// the pw CLI, so it reads no ACTIVATE credential of the computer's. An
// empty indexBase is INDEX_BASE's.
func In(indexBase string) bool {
	if indexBase == "" {
		indexBase = os.Getenv("INDEX_BASE")
	}
	if indexBase == "" {
		return false
	}
	b, err := os.ReadFile(filepath.Join(indexBase, "access.json"))
	if err != nil {
		return false
	}
	var v struct {
		Stopped bool `json:"stopped"`
	}
	return json.Unmarshal(b, &v) == nil && v.Stopped
}
