package kb

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// A file that is not there, or under a name that is a file, is a 404, so
// the viewer can say it may have moved.
func TestReadFileContentOfAMissingFileIsA404(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644)
	for _, rel := range []string{"gone.md", "no/such/dir.pdf", "a.txt/under-a-file.md"} {
		_, err := ReadFileContent(rel, root, "")
		var e *apierr.Error
		if !errors.As(err, &e) || e.Status != 404 {
			t.Errorf("%s: %v", rel, err)
		}
	}
}
