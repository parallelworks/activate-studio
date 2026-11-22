package chat

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// A running platform desktop comes back from list_sessions and from a
// run's session note with the card that shows it, as a web session does.
func TestDesktopSessionsComeWithTheirCard(t *testing.T) {
	plat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions" || r.Header.Get("Authorization") != "Bearer desk-tools-key" {
			w.WriteHeader(404)
			return
		}
		io.WriteString(w, `[{"name":"cad-00003-desktop","user":"alex","type":"desktop","status":"running",`+
			`"software":{"type":"vnc"},"externalHref":"/sessions/alex/cad-00003-desktop/vnc","workflowRun":{"slug":"cad-00003","name":"cad"}},`+
			`{"name":"old-desk","user":"alex","type":"desktop","status":"stopped","software":{"type":"vnc"}}]`)
	}))
	defer plat.Close()
	dir := t.TempDir()
	env := map[string]string{"INDEX_BASE": dir, "KB_ROOT": filepath.Join(dir, "kb"), "STUDIO_WEB_DIST": filepath.Join(dir, "web"),
		"PW_GATEWAY_URL": plat.URL + "/api/openai/v1"}
	cfg, _ := config.Load(func(k string) string { return env[k] })
	m := New(app.New(cfg, slog.New(slog.DiscardHandler), nil, nil))
	m.pf = platform.New(platform.Options{IndexBase: dir, Home: dir, LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }})
	key := "desk-tools-key"
	ctx := ToolCtx{UserKey: &key}
	r := httptest.NewRequest("POST", "/api/chat/stream", nil)

	card := "![cad-00003-desktop](/?embed=session&user=alex&name=cad-00003-desktop)"
	out, err := m.toolListSessions(r, map[string]any{"match": "cad"}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := "cad-00003-desktop (desktop, running, owner alex, opened by run cad-00003 of cad)\n" + card; out.Result != want {
		t.Fatalf("list_sessions:\n%s\nwant\n%s", out.Result, want)
	}
	// A desktop that is not running is listed without a card.
	out, _ = m.toolListSessions(r, map[string]any{"match": "old", "all": true}, ctx)
	if strings.Contains(out.Result, "embed=session") || !strings.HasPrefix(out.Result, "old-desk (desktop, stopped") {
		t.Fatalf("stopped desktop: %s", out.Result)
	}
	if note := m.runSessionsNote(r, ctx, "cad-00003"); note != "Session cad-00003-desktop is running. Show it in the reply with this markdown:\n"+card {
		t.Fatalf("watch_run note: %q", note)
	}
}
