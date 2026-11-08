package server

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/bridge"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/search"
)

// gufi_vt's remote command reaches the server at /internal/gufi-vt: the
// front answers 404 to a client without the bridge secret, Node-free or
// with Node behind, and Node is never reached.
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

// The Go server's own gufi-vt-remote subcommand carries the bridge secret,
// so it gets past the front to the Go route, never to Node; there a wrong
// secret is 403, an unknown token 404 and another command 400, as gufiVt.ts
// answers them.
func TestGufiVtSubcommandReachesTheGoRoute(t *testing.T) {
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
		post := func(secret, body string) (int, string) {
			r := httptest.NewRequest(http.MethodPost, "/internal/gufi-vt", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set(bridge.Header, bridge.Secret())
			r.Header.Set("X-Studio-Secret", secret)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			b, _ := io.ReadAll(w.Body)
			return w.Code, string(b)
		}
		if code, body := post("wrong", `{"token":"t","args":[]}`); code != 403 || body != "forbidden" {
			t.Errorf("STUDIO_NODE=%q, wrong secret: %d %q", node, code, body)
		}
		if code, body := post(search.VtSecretForTests(), `{"token":"missing","args":["gufi_query","/i"]}`); code != 404 || body != "unknown location" {
			t.Errorf("STUDIO_NODE=%q, unknown token: %d %q", node, code, body)
		}
		search.AddVtLocationForTests("front-t1", "cluster-a", "k", "/c/bin", []string{"/i"}, "p")
		if code, body := post(search.VtSecretForTests(), `{"token":"front-t1","args":["cat","/etc/passwd","/i"]}`); code != 400 || body != "not a gufi_query on this location" {
			t.Errorf("STUDIO_NODE=%q, another command: %d %q", node, code, body)
		}
		// The subcommand itself, against this front.
		front := httptest.NewServer(s)
		var out, errOut bytes.Buffer
		env := map[string]string{"STUDIO_GUFI_VT_URL": front.URL + "/internal/gufi-vt", "STUDIO_GUFI_VT_SECRET": search.VtSecretForTests()}
		search.VtRemoteMain([]string{"missing", "gufi_query /i"}, func(k string) string { return env[k] }, &out, &errOut)
		front.Close()
		ns.Close()
		if !strings.Contains(errOut.String(), "unknown location") || reached {
			t.Errorf("STUDIO_NODE=%q, the subcommand: %q, Node reached %v", node, errOut.String(), reached)
		}
	}
}
