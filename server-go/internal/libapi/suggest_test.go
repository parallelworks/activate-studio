package libapi

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestXDGUserDirs(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, ".config")
	os.MkdirAll(cfg, 0o755)
	os.WriteFile(filepath.Join(cfg, "user-dirs.dirs"), []byte(`# written by xdg-user-dirs-update
XDG_DESKTOP_DIR="$HOME/Bureau"
XDG_DOCUMENTS_DIR="$HOME/Docs"
XDG_DOWNLOAD_DIR="$HOME"
XDG_MUSIC_DIR="/srv/music"
XDG_TEMPLATES_DIR="relative"
`), 0o644)
	got := xdgUserDirs(home, "")
	want := map[string]string{
		"XDG_DESKTOP_DIR":   filepath.Join(home, "Bureau"),
		"XDG_DOCUMENTS_DIR": filepath.Join(home, "Docs"),
		"XDG_MUSIC_DIR":     "/srv/music",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: got %q, want %q", k, got[k], v)
		}
	}
	if len(xdgUserDirs(home, filepath.Join(home, "none"))) != 0 {
		t.Fatal("a missing file gave directories")
	}
}

func TestSuggestionsOfferFoldersThatExistAndAreNotLibraries(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{"Docs", "Desktop", "Library/Mail"} {
		os.MkdirAll(filepath.Join(home, d), 0o755)
	}
	// Linux: XDG's Documents, the default Desktop, no Downloads.
	linux := suggestionCandidates("linux", home, map[string]string{"XDG_DOCUMENTS_DIR": filepath.Join(home, "Docs")})
	got := suggest(linux, map[string]bool{}, readOne)
	if len(got) != 2 || got[0].Label != "Documents" || got[0].Path != filepath.Join(home, "Docs") || got[1].ID != "desktop" {
		t.Fatalf("linux: %+v", got)
	}
	// The main folder or a library's source is not offered again.
	got = suggest(linux, map[string]bool{filepath.Join(home, "Docs"): true}, readOne)
	if len(got) != 1 || got[0].ID != "desktop" {
		t.Fatalf("taken: %+v", got)
	}

	// macOS: Mail is offered, and says when it needs Full Disk Access.
	mac := suggestionCandidates("darwin", home, nil)
	got = suggest(mac, map[string]bool{}, readOne)
	ids := ""
	for _, s := range got {
		ids += s.ID + " "
	}
	if ids != "desktop mail " || got[1].Access != "ok" || got[0].Note == "" {
		t.Fatalf("mac: %+v", got)
	}
	denied := func(string) error { return fs.ErrPermission }
	got = suggest(mac, map[string]bool{}, denied)
	if got[0].Access != "ok" || got[1].Access != "full-disk-access" {
		t.Fatalf("mac denied: %+v", got)
	}
	// Any other failure to read Mail's folder leaves it out.
	got = suggest(mac, map[string]bool{}, func(string) error { return os.ErrClosed })
	if len(got) != 1 || got[0].ID != "desktop" {
		t.Fatalf("mac unreadable: %+v", got)
	}
}
