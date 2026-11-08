package libapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/index"
)

// Node-free, the start writes each library's state line to the index log,
// as main.ts does; with Node behind, Node does and Go writes none.
func TestStartStateLines(t *testing.T) {
	for _, node := range []string{"off", ""} {
		dir := t.TempDir()
		kb := filepath.Join(dir, "kb")
		os.MkdirAll(kb, 0o755)
		env := map[string]string{"KB_ROOT": kb, "INDEX_BASE": filepath.Join(dir, "ix"), "GUFI_BIN": filepath.Join(dir, "no-gufi"), "STUDIO_NODE": node}
		cfg, err := config.Load(func(k string) string { return env[k] })
		if err != nil {
			t.Fatal(err)
		}
		a := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
		Register(http.NewServeMux(), a)
		ctx, cancel := context.WithCancel(context.Background())
		a.Start(ctx)
		file := index.Of(a).Log.File
		want := regexp.MustCompile(`(?m)^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d library kb \(.+\): index no, words no, vectors no`)
		wait := 5 * time.Second
		if node == "" {
			wait = 500 * time.Millisecond
		}
		deadline := time.Now().Add(wait)
		var b []byte
		for time.Now().Before(deadline) {
			if b, _ = os.ReadFile(file); want.Match(b) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
		a.Wait(2 * time.Second)
		t.Logf("STUDIO_NODE=%q: %q", node, b)
		if node == "off" && !want.Match(b) {
			t.Fatalf("index log: %q", b)
		}
		if node == "" && want.Match(b) {
			t.Fatalf("Go wrote Node's lines: %q", b)
		}
	}
}
