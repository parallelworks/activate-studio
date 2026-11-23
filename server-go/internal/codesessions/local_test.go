package codesessions

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode/pwcodetest"
)

// With no platform and this host offered as a site, the page lists this
// host's daemon as one machine, with no links to ACTIVATE, reads a
// session from it, and Find finds it there.
func TestLocalSessions(t *testing.T) {
	sock := pwcodetest.Socket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/status":
			json.NewEncoder(w).Encode(map[string]any{"protocolVersion": pwcode.ProtocolVersion, "machineId": "mach1", "hostname": "box.example"})
		case "/v1/sessions":
			json.NewEncoder(w).Encode([]any{map[string]any{"id": "20261121-120000-abcdef", "name": "pump summary", "status": "idle", "messages": 3, "workspace": "/kb/agents/x"}})
		case "/v1/sessions/20261121-120000-abcdef":
			json.NewEncoder(w).Encode(map[string]any{"id": "20261121-120000-abcdef", "name": "pump summary", "status": "idle",
				"history": []any{map[string]any{"kind": "user", "text": "hi"}, map[string]any{"kind": "thinking", "text": "hm"}, map[string]any{"kind": "assistant", "text": "done"}}})
		default:
			w.WriteHeader(404)
		}
	}))
	dir := t.TempDir()
	env := map[string]string{"INDEX_BASE": filepath.Join(dir, "ix"), "KB_ROOT": filepath.Join(dir, "kb"), "STUDIO_WEB_DIST": filepath.Join(dir, "web"), "PW_CODE_SOCKET": sock,
		// ACTIVATE on this computer, models from a server here: the Code page lists this computer.
		"PW_API_KEY": "owner-key", "OPENAI_BASE_URL": "http://127.0.0.1:11434/v1"}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	p := platform.New(platform.Options{IndexBase: env["INDEX_BASE"], Home: t.TempDir(), LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }})
	was := agentsites.Current()
	agentsites.SetCurrent(&agentsites.Sites{P: p, Daemon: pwcode.New(p)})
	t.Cleanup(func() { agentsites.SetCurrent(was) })
	mux := http.NewServeMux()
	Register(mux, a, p)
	if !a.Providers.LocalCode(httptest.NewRequest("GET", "/", nil)) {
		t.Fatalf("the Code page is not offered on this host: origin %q sites %v", p.Origin(), agentsites.Current() != nil)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res, _ := http.Get(srv.URL + "/api/code-sessions")
	var list struct {
		Machines []Machine
		Sessions []View
		CodeURL  string `json:"codeUrl"`
	}
	json.NewDecoder(res.Body).Decode(&list)
	if res.StatusCode != 200 || len(list.Machines) != 1 || list.Machines[0].ID != "mach1" || list.Machines[0].Name != LocalName || list.Machines[0].DaemonID != "mach1" ||
		len(list.Sessions) != 1 || list.Sessions[0].Title != "pump summary" || list.Sessions[0].URL != "" || list.CodeURL != "" {
		t.Fatalf("%d %+v", res.StatusCode, list)
	}
	res, _ = http.Get(srv.URL + "/api/code-sessions/item?machine=mach1&id=20261121-120000-abcdef")
	var item struct {
		Session View
		History []historyItem
	}
	json.NewDecoder(res.Body).Decode(&item)
	if res.StatusCode != 200 || item.Session.URL != "" || len(item.History) != 2 {
		t.Fatalf("%d %+v", res.StatusCode, item)
	}
	if res, _ := http.Get(srv.URL + "/api/code-sessions/item?machine=other&id=20261121-120000-abcdef"); res.StatusCode != 404 {
		t.Fatalf("another machine: %d", res.StatusCode)
	}
	m, v, err := Find(httptest.NewRequest("GET", "/", nil), p, "mach1", "20261121-120000-abcdef")
	if err != nil || m.Name != LocalName || v.Title != "pump summary" {
		t.Fatalf("find: %v %+v %+v", err, m, v)
	}
}
