package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// TestInventoryMatchesNode reads the Node server's route registrations and
// checks nodeRoutes lists each of them (the templated fleet and
// user-library loops expanded).
func TestInventoryMatchesNode(t *testing.T) {
	src := "../../../server/src"
	re := regexp.MustCompile("app\\.(get|post|put|delete|patch)\\(\\s*['`]([^'`]+)['`]")
	want := map[string]bool{}
	filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".ts") {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			paths := []string{m[2]}
			switch {
			case strings.Contains(m[2], "${name}"):
				paths = nil
				for _, n := range []string{"tick", "pause", "resume", "retire", "message"} {
					paths = append(paths, strings.ReplaceAll(m[2], "${name}", n))
				}
			case strings.Contains(m[2], "${t}"):
				paths = []string{strings.ReplaceAll(m[2], "${t}", "ssh"), strings.ReplaceAll(m[2], "${t}", "pw")}
			}
			for _, p := range paths {
				p = regexp.MustCompile(`:([A-Za-z_]+)`).ReplaceAllString(p, "{$1}")
				want[strings.ToUpper(m[1])+" "+p] = true
			}
		}
		return nil
	})
	want["GET /api/session-proxy/{host}/{rest...}"] = true // app.route
	if len(want) == 0 {
		t.Skip("no Node sources")
	}
	have := map[string]bool{}
	for _, r := range nodeRoutes {
		have[r.method+" "+r.pattern] = true
	}
	for k := range want {
		if !have[k] {
			t.Errorf("Node route missing from the inventory: %s", k)
		}
	}
	for k := range have {
		if !want[k] {
			t.Errorf("inventory route Node no longer has: %s", k)
		}
	}
}

func TestNodeFreeAnswers501ForUnported(t *testing.T) {
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"STUDIO_NODE": "off", "INDEX_BASE": t.TempDir(), "KB_ROOT": t.TempDir(), "STUDIO_WEB_DIST": t.TempDir()}[k]
	})
	s := New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	for _, c := range []struct {
		method, path string
		status       int
	}{
		{"POST", "/api/chat/stream", 501},
		{"GET", "/api/kb/tree", 501},
		{"GET", "/api/no-such-route", 404},
		{"GET", "/healthz", 200},
	} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != c.status {
			t.Errorf("%s %s: %d, want %d (%s)", c.method, c.path, w.Code, c.status, w.Body)
		}
	}
	w := httptest.NewRecorder()
	s.App.Node().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/x", nil))
	if w.Code != 501 {
		t.Errorf("App.Node() without Node: %d", w.Code)
	}
}

func TestClientCannotClaimAnInternalCall(t *testing.T) {
	var got string
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Studio-Internal") + "|" + r.Header.Get("X-Other")
	}))
	defer node.Close()
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": t.TempDir(), "KB_ROOT": t.TempDir(), "STUDIO_WEB_DIST": t.TempDir()}[k]
	})
	s := New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	u, _ := url.Parse(node.URL)
	s.AttachNode(u)
	r := httptest.NewRequest("GET", "/api/kb/tree", nil)
	r.Header.Set("X-Studio-Internal", "1")
	r.Header.Set("X-Other", "kept")
	s.ServeHTTP(httptest.NewRecorder(), r)
	if got != "|kept" {
		t.Fatalf("Node saw %q", got)
	}
}
