package codesessions

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode/pwcodetest"
)

const sid = "20261121-120000-abcdef"

// answers records the approval answers a fake daemon or platform took.
type answers struct {
	mu   sync.Mutex
	got  []map[string]any
	path []string
}

func (a *answers) take(r *http.Request) {
	var b map[string]any
	json.NewDecoder(r.Body).Decode(&b)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, b)
	a.path = append(a.path, r.URL.Path)
}

func codeServer(t *testing.T, env map[string]string, sites bool) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	env["INDEX_BASE"], env["KB_ROOT"], env["STUDIO_WEB_DIST"] = filepath.Join(dir, "ix"), filepath.Join(dir, "kb"), filepath.Join(dir, "web")
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	p := platform.New(platform.Options{IndexBase: env["INDEX_BASE"], Home: t.TempDir(), LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }})
	was := agentsites.Current()
	if sites {
		agentsites.SetCurrent(&agentsites.Sites{P: p, Daemon: pwcode.New(p)})
	} else {
		agentsites.SetCurrent(nil)
	}
	t.Cleanup(func() { agentsites.SetCurrent(was) })
	mux := http.NewServeMux()
	Register(mux, a, p)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func postAnswer(t *testing.T, base, body string) (int, map[string]any) {
	t.Helper()
	res, err := http.Post(base+"/api/code-sessions/approve", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(res.Body)
	json.Unmarshal(raw, &out)
	return res.StatusCode, out
}

// On this host's daemon: the session's detail lists what it waits on, and
// Approve and Deny reach the daemon's answer route, a deny with a note.
func TestApproveOnThisComputer(t *testing.T) {
	var got answers
	gone := false
	sock := pwcodetest.Socket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/status":
			json.NewEncoder(w).Encode(map[string]any{"protocolVersion": pwcode.ProtocolVersion, "machineId": "mach1", "hostname": "box.example"})
		case r.URL.Path == "/v1/sessions/"+sid:
			json.NewEncoder(w).Encode(map[string]any{"id": sid, "status": "awaiting_approval",
				"approvals": []any{map[string]any{"id": "ap1", "kind": "confirm", "tool": "Bash", "command": "ls -la " + strings.Repeat("x", 900), "reason": "shell command"}},
				"history":   []any{map[string]any{"kind": "user", "text": "hi"}}})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/sessions/"+sid+"/approvals/"):
			if gone {
				w.WriteHeader(404)
				json.NewEncoder(w).Encode(map[string]any{"detail": "no such approval"})
				return
			}
			got.take(r)
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	base := codeServer(t, map[string]string{"PW_CODE_SOCKET": sock}, true).URL

	res, _ := http.Get(base + "/api/code-sessions/item?machine=mach1&id=" + sid)
	var item struct {
		Approvals []approval
	}
	json.NewDecoder(res.Body).Decode(&item)
	if res.StatusCode != 200 || len(item.Approvals) != 1 || item.Approvals[0].ID != "ap1" || item.Approvals[0].Kind != "confirm" ||
		item.Approvals[0].Tool != "Bash" || len([]rune(item.Approvals[0].Command)) != maxToolArgs {
		t.Fatalf("item approvals: %d %+v", res.StatusCode, item.Approvals)
	}

	if code, out := postAnswer(t, base, `{"machine":"mach1","id":"`+sid+`","approval":"ap1","allowed":true}`); code != 200 || out["ok"] != true {
		t.Fatalf("approve: %d %v", code, out)
	}
	if code, out := postAnswer(t, base, `{"machine":"mach1","id":"`+sid+`","approval":"ap1","allowed":false}`); code != 200 || out["ok"] != true {
		t.Fatalf("deny: %d %v", code, out)
	}
	if len(got.got) != 2 || got.got[0]["allowed"] != true || got.got[0]["denyMessage"] != nil ||
		got.got[1]["allowed"] != nil && got.got[1]["allowed"] != false || got.got[1]["denyMessage"] != DenyNote ||
		got.path[0] != "/v1/sessions/"+sid+"/approvals/ap1" {
		t.Fatalf("answers: %v %v", got.got, got.path)
	}
	// Nothing standing is granted from here.
	for _, a := range got.got {
		if a["allowDir"] != nil || a["allowRules"] != nil {
			t.Fatalf("a standing grant: %v", a)
		}
	}

	gone = true
	if code, out := postAnswer(t, base, `{"machine":"mach1","id":"`+sid+`","approval":"ap1","allowed":true}`); code != 409 || !strings.Contains(out["error"].(string), "no longer waiting") {
		t.Fatalf("answered already: %d %v", code, out)
	}
	if code, _ := postAnswer(t, base, `{"machine":"other","id":"`+sid+`","approval":"ap1","allowed":true}`); code != 404 {
		t.Fatalf("another machine: %d", code)
	}
	for _, bad := range []string{`{"machine":"mach1","id":"../x","approval":"ap1"}`, `{"machine":"mach1","id":"` + sid + `"}`, `not json`} {
		if code, _ := postAnswer(t, base, bad); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
}

// Through the platform: the answer goes to its approval route as the
// viewer; a machine that refuses answers from there says so.
func TestApproveThroughThePlatform(t *testing.T) {
	var got answers
	refuse := false
	plat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(401)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/code/machines/m1/sessions/"+sid {
			// The session as read again after a 404: it waits on nothing.
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": sid, "status": "idle", "approvals": []any{}})
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/code/machines/m1/sessions/"+sid+"/approvals/ap2" {
			w.WriteHeader(404)
			return
		}
		if refuse {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(403)
			json.NewEncoder(w).Encode(map[string]any{"detail": "the session runs above the remote permission ceiling"})
			return
		}
		got.take(r)
		w.WriteHeader(204)
	}))
	defer plat.Close()
	base := codeServer(t, map[string]string{"PW_GATEWAY_URL": plat.URL + "/api/openai/v1", "PW_API_KEY": "k"}, false).URL

	if code, out := postAnswer(t, base, `{"machine":"m1","id":"`+sid+`","approval":"ap2","allowed":false}`); code != 200 || out["ok"] != true {
		t.Fatalf("deny: %d %v", code, out)
	}
	if len(got.got) != 1 || got.got[0]["denyMessage"] != DenyNote {
		t.Fatalf("answers: %v", got.got)
	}
	refuse = true
	code, out := postAnswer(t, base, `{"machine":"m1","id":"`+sid+`","approval":"ap2","allowed":true}`)
	if msg, _ := out["error"].(string); code != 403 || !strings.Contains(msg, "remote permission ceiling") || !strings.Contains(msg, "where the session runs") {
		t.Fatalf("refused: %d %v", code, out)
	}
	if code, _ := postAnswer(t, base, `{"machine":"m1","id":"`+sid+`","approval":"gone","allowed":true}`); code != 409 {
		t.Fatalf("gone: %d", code)
	}
}
