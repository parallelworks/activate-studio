package index

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A second write in place within the same clock tick and of the same
// length, as Node's writeFileSync can make, is seen on the next read.
func TestSettingsSeeSameTickWrite(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "indexing.json")
	at := time.Now()
	write := func(threads string) {
		if err := os.WriteFile(f, []byte(`{"libraries":{"kb":{"threads":`+threads+`}}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(f, at, at); err != nil {
			t.Fatal(err)
		}
	}
	s := NewSettingsStore(dir)
	write("4")
	if got := s.Threads("kb"); got != 4 {
		t.Fatalf("threads %d, want 4", got)
	}
	write("8")
	if got := s.Threads("kb"); got != 8 {
		t.Fatalf("threads %d after a same-tick write, want 8", got)
	}
	// Our own write, then Node's in the same tick.
	if _, err := s.Save("kb", map[string]any{"threads": float64(2)}); err != nil {
		t.Fatal(err)
	}
	write("6")
	if got := s.Threads("kb"); got != 6 {
		t.Fatalf("threads %d after a write beside ours, want 6", got)
	}
	// Settled, the kept copy serves.
	old := at.Add(-time.Hour)
	if err := os.Chtimes(f, old, old); err != nil {
		t.Fatal(err)
	}
	if a, b := FileStamp(f, ""), FileStamp(f, ""); a != b {
		t.Fatalf("a settled file stamps differently: %s, %s", a, b)
	}
}
