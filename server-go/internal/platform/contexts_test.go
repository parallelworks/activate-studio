package platform

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCredentialsIn: in a PW_CREDENTIALS_DIR the CLI's file is
// .credentials; credentials is read where only it exists.
func TestCredentialsIn(t *testing.T) {
	dir := t.TempDir()
	dot, plain := filepath.Join(dir, ".credentials"), filepath.Join(dir, "credentials")
	if got := CredentialsIn(dir); got != dot {
		t.Errorf("an empty folder: %s", got)
	}
	os.WriteFile(plain, []byte("{}"), 0o600)
	if got := CredentialsIn(dir); got != plain {
		t.Errorf("only credentials: %s", got)
	}
	os.WriteFile(dot, []byte("{}"), 0o600)
	if got := CredentialsIn(dir); got != dot {
		t.Errorf("both: %s", got)
	}
}
