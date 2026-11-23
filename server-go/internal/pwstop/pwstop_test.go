package pwstop

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIn(t *testing.T) {
	dir := t.TempDir()
	if In(dir) {
		t.Fatal("stopped with no access.json")
	}
	os.WriteFile(filepath.Join(dir, "access.json"), []byte(`{"pwContext":"work","stopped":true}`), 0o644)
	if !In(dir) {
		t.Fatal("not stopped")
	}
	t.Setenv("INDEX_BASE", dir)
	if !In("") {
		t.Fatal("INDEX_BASE not read")
	}
}
