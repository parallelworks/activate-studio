package libapi

import (
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/wslpath"
)

// Under WSL a Windows path typed for a folder is its /mnt folder.
func TestWindowsPathUnderWSL(t *testing.T) {
	wslpath.Set(true, "/mnt/")
	t.Cleanup(func() { wslpath.Set(false, "/mnt/") })
	if got := expand(`C:\Users\me\Documents`); got != "/mnt/c/Users/me/Documents" {
		t.Fatalf("%q", got)
	}
	wslpath.Set(false, "/mnt/")
	if got := expand("/srv/kb"); got != "/srv/kb" {
		t.Fatalf("%q", got)
	}
}
