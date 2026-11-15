package settings

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A page added after a sections list was saved shows on that list; a list
// saved since keeps the choice it records.
func TestSavedSectionsGainNewPages(t *testing.T) {
	old := []string{"chat", "library", "search", "query", "overview", "history", "agents", "settings", "help"}
	for _, c := range []struct {
		name  string
		saved string
		want  []string
	}{
		{"saved before Transfers", `{"setupDone":true,"sections":["chat","library","search","query","overview","history","agents","settings","help"]}`,
			[]string{"chat", "library", "transfers", "search", "query", "overview", "history", "agents", "settings", "help"}},
		{"switched off since", `{"setupDone":true,"sections":["chat","library","search","settings","help"],"sectionsGen":1}`,
			[]string{"chat", "library", "search", "settings", "help"}},
		{"no library on the list", `{"setupDone":true,"sections":["chat","search","settings","help"]}`,
			[]string{"transfers", "chat", "search", "settings", "help"}},
		{"every page", `{"setupDone":true,"sections":[]}`, []string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			os.WriteFile(filepath.Join(dir, "settings.json"), []byte(c.saved), 0o644)
			s := Open(dir, "/somewhere/kb", false, func(string) (string, bool) { return "", false })
			if got := s.Effective().Sections; !slices.Equal(got, c.want) {
				t.Errorf("sections %q, want %q", got, c.want)
			}
		})
	}
	// The saved list itself is not changed.
	in := slices.Clone(old)
	WithNewPages(in, 0)
	if !slices.Equal(in, old) {
		t.Errorf("the saved list changed: %q", in)
	}
}
