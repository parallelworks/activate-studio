package kb

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// slowIndexer reindexes after a pause, long enough for the request that
// started the job to have been answered, and fails when its context has
// been canceled by then.
type slowIndexer struct {
	mu   sync.Mutex
	dirs []string
}

func (s *slowIndexer) IndexDir(ctx context.Context, rel string) (int64, error) {
	time.Sleep(100 * time.Millisecond)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.dirs = append(s.dirs, rel)
	s.mu.Unlock()
	return 1, nil
}
func (s *slowIndexer) IndexRootDB(ctx context.Context) (int64, error) { return 1, ctx.Err() }
func (s *slowIndexer) Changed(context.Context, ...string)             {}

// A move started with async runs to its end after the request that started
// it is answered and its context canceled: both folders are reindexed.
func TestAsyncMoveOutlivesItsRequest(t *testing.T) {
	kbRoot, ix := t.TempDir(), t.TempDir()
	env := map[string]string{"KB_ROOT": kbRoot, "INDEX_BASE": ix, "STUDIO_NODE": "off", "GUFI_BIN": filepath.Join(ix, "no-gufi")}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	m := New(a)
	slow := &slowIndexer{}
	m.Files.Indexer = slow
	mux := http.NewServeMux()
	m.Routes(mux)
	os.MkdirAll(filepath.Join(kbRoot, "notes"), 0o755)
	os.MkdirAll(filepath.Join(kbRoot, "plans"), 0o755)
	os.WriteFile(filepath.Join(kbRoot, "notes", "idea.md"), []byte("x"), 0o644)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/api/kb/move", strings.NewReader(`{"paths":["notes/idea.md"],"dest":"plans","async":true}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	cancel() // what net/http does once the handler returns
	var started map[string]any
	json.Unmarshal(rec.Body.Bytes(), &started)
	id, ok := started["id"].(float64)
	if rec.Code != 200 || !ok {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	var job map[string]any
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/jobs/"+strconv.Itoa(int(id)), nil))
		json.Unmarshal(rec.Body.Bytes(), &job)
		if job["state"] != "running" {
			break
		}
	}
	if job["state"] != "done" {
		t.Fatalf("job: %v", job)
	}
	slow.mu.Lock()
	defer slow.mu.Unlock()
	if len(slow.dirs) != 2 {
		t.Fatalf("reindexed %v, want both folders", slow.dirs)
	}
}
