package updates

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The update check reads releases (GitHub, or STUDIO_UPDATE_URL) and no
// ACTIVATE credential, so it works the same with or without ACTIVATE.
func TestTheUpdateCheckUsesNoActivateCredential(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	reads := regexp.MustCompile(`internal/platform"|PW_API_KEY|GatewayKey|KeyFor|CLICredential|pw/credentials|PW_CREDENTIALS_DIR`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		if m := reads.Find(b); m != nil {
			t.Errorf("%s reads an ACTIVATE credential (%s)", f, m)
		}
	}
}
