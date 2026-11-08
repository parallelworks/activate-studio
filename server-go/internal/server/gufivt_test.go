package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// The gufi_vt wrapper's path is Node's own, reached on Node's port: the
// front answers 404 to a client, Node-free or with Node behind.
func TestGufiVtIsBridgeOnly(t *testing.T) {
	for _, node := range []string{"off", ""} {
		reached := false
		ns := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
		cfg, _ := config.Load(func(k string) string {
			return map[string]string{"STUDIO_NODE": node, "INDEX_BASE": t.TempDir(), "KB_ROOT": t.TempDir(), "STUDIO_WEB_DIST": t.TempDir()}[k]
		})
		s := New(cfg, slog.New(slog.DiscardHandler), nil, nil)
		if node == "" {
			u, _ := url.Parse(ns.URL)
			s.AttachNode(u)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/internal/gufi-vt", nil))
		ns.Close()
		if w.Code != http.StatusNotFound || reached {
			t.Errorf("STUDIO_NODE=%q: %d, Node reached %v", node, w.Code, reached)
		}
	}
}
