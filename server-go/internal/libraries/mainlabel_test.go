package libraries

import (
	"path/filepath"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/settings"
)

// With no name passed, the main library takes the default name by mode,
// not its folder's name.
func TestListNamesTheMainLibraryByMode(t *testing.T) {
	for _, signIn := range []bool{false, true} {
		dir := t.TempDir()
		r := New(Options{KBRoot: filepath.Join(dir, "kb"), IndexBase: dir, GufiIndex: filepath.Join(dir, "gufi"), AuthEnabled: signIn, Getenv: func(string) string { return "" }})
		got := r.List("", Deployment)[0]
		if !got.Primary || got.Label != settings.MainLabelFor(signIn) {
			t.Fatalf("sign-in %v: the main library is %q", signIn, got.Label)
		}
	}
}
