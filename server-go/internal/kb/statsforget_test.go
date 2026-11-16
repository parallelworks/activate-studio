package kb

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// A change to the index reported to the app ("dbList", as after a sweep or
// a re-index that did not go through this module) drops the counts Stats
// shows, which were kept ten minutes; another kind of change keeps them.
func TestIndexChangeDropsStatsCounts(t *testing.T) {
	kbRoot, ix := t.TempDir(), t.TempDir()
	env := map[string]string{"KB_ROOT": kbRoot, "INDEX_BASE": ix, "STUDIO_NODE": "off", "GUFI_BIN": filepath.Join(ix, "no-gufi")}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	m := New(a)
	keep := func() {
		m.stats.mu.Lock()
		m.stats.m = map[string]struct {
			at time.Time
			c  corpus
		}{"/index": {time.Now(), corpus{Available: true, Files: 10}}}
		m.stats.mu.Unlock()
	}
	kept := func() bool { m.stats.mu.Lock(); defer m.stats.mu.Unlock(); return len(m.stats.m) > 0 }

	keep()
	a.Providers.Changed(context.Background(), "context")
	if !kept() {
		t.Fatal("a change to the chat's context dropped the counts")
	}
	a.Providers.Changed(context.Background(), "dbList", "context")
	if kept() {
		t.Fatal("the counts outlived a change to the index")
	}
}
