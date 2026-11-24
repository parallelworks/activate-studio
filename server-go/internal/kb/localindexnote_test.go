package kb_test

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	_ "modernc.org/sqlite"
)

// A file of a library on this computer with no indexed text says why when
// that is the index and not the file: the library is indexing now, or the
// index has no entry for the file or one older than it, or there is no
// index yet. A file the index
// holds as it is, with no text, says nothing more.
func TestALocalFileTheIndexHasNotCaughtUpWithSaysSo(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "kb")
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": filepath.Join(dir, "ix"), "KB_ROOT": root, "STUDIO_WEB_DIST": filepath.Join(dir, "web")}[k]
	})
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	for _, f := range []string{"reports/seen.xlsx", "reports/changed.xlsx", "reports/new.xlsx", "fresh/new.pdf", ".hidden/x.pdf"} {
		p := filepath.Join(root, f)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("PK"), 0o644)
		os.Chtimes(p, old, old)
	}
	now := time.Now()
	os.Chtimes(filepath.Join(root, "reports/changed.xlsx"), now, now)
	// The index of reports holds seen.xlsx and changed.xlsx as they were an hour ago.
	os.MkdirAll(filepath.Join(cfg.GufiIndex, "reports"), 0o755)
	db, err := sql.Open("sqlite", filepath.Join(cfg.GufiIndex, "reports", "db.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE entries (name TEXT, type TEXT, inode TEXT, mtime INT64)"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"seen.xlsx", "changed.xlsx"} {
		db.Exec("INSERT INTO entries VALUES (?, 'f', '1', ?)", n, old.Unix())
	}
	db.Close()

	var st kb.LocalIndex
	var asked []string
	kb.LocalIndexing = func(lib kb.Library) (kb.LocalIndex, bool) {
		asked = append(asked, lib.ID)
		return st, lib.Primary
	}
	t.Cleanup(func() { kb.LocalIndexing = nil })
	mux := http.NewServeMux()
	kb.Register(mux, app.New(cfg, slog.New(slog.DiscardHandler), nil, nil))
	note := func(p string) map[string]any {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/kb/file?path="+p, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", p, w.Code, w.Body.String())
		}
		var out struct {
			Source string         `json:"source"`
			Index  map[string]any `json:"index"`
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		if out.Source != "none" {
			t.Fatalf("%s: source %q", p, out.Source)
		}
		return out.Index
	}

	st = kb.LocalIndex{SyncEvery: 300, CatchUp: "sync"}
	// With no index at its top the library has none yet: the sync does not make one.
	if got := note("reports/new.xlsx"); got["state"] != "unindexed" || got["catchUp"] != "sync" {
		t.Errorf("no index: %v", got)
	}
	os.WriteFile(filepath.Join(cfg.GufiIndex, "db.db"), nil, 0o644)
	for _, p := range []string{"reports/changed.xlsx", "reports/new.xlsx", "fresh/new.pdf"} {
		if got := note(p); got["state"] != "newer" || got["syncEvery"] != 300.0 || got["catchUp"] != "sync" {
			t.Errorf("%s: %v", p, got)
		}
	}
	for _, p := range []string{"reports/seen.xlsx", ".hidden/x.pdf"} {
		if got := note(p); got != nil {
			t.Errorf("%s: %v, want no note", p, got)
		}
	}
	st.Busy = true
	if got := note("reports/new.xlsx"); got["state"] != "indexing" {
		t.Errorf("while indexing: %v", got)
	}
	if len(asked) == 0 || asked[0] != kb.PrimaryID {
		t.Errorf("asked about %v", asked)
	}
}
