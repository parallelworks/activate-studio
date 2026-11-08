package agentsites

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode/pwcodetest"
)

// A cluster's daemon is reached over `pw ssh … pw code daemon proxy`; here
// the bridge runs this test binary as a fake daemon speaking HTTP on its
// stdio, and a fake pw answers the cluster list and the workspace command
// (agentSites.test.mjs).

func TestHelperSiteDaemon(t *testing.T) {
	if os.Getenv("AS_HELPER") != "1" {
		t.Skip("helper process")
	}
	logf := os.Getenv("AS_LOG")
	goal := ""
	history := []any{
		map[string]any{"kind": "user", "text": "objective"},
		map[string]any{"kind": "assistant", "text": "working on it"},
		map[string]any{"kind": "goal_progress", "text": `{"turns":2,"outcome":"","reason":"tests still failing"}`},
	}
	pwcodetest.ServeStdio(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		json.Unmarshal(raw, &b)
		line, _ := json.Marshal(map[string]any{"method": r.Method, "url": r.URL.Path, "body": json.RawMessage(orNull(raw))})
		f, _ := os.OpenFile(logf, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		f.Write(append(line, '\n'))
		f.Close()
		send := func(code int, v any) {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(v)
		}
		u := r.URL.Path
		switch {
		case u == "/v1/status":
			send(200, jsonv.NewObject("protocolVersion", 2.0, "version", "v7.120.0", "pid", 9.0, "hostname", "login1", "sessions", 1.0, "remoteStart", true, "remoteMaxPermissionMode", "accept-edits"))
		case u == "/v1/sessions" && r.Method == "POST":
			model := b["model"]
			if model == nil {
				model = "default"
			}
			send(200, map[string]any{"id": "sess-1", "status": "idle", "messages": 0, "history": []any{}, "approvals": nil, "workspace": b["workspace"], "model": model})
		case u == "/v1/sessions":
			send(200, []any{map[string]any{"id": "sess-1", "name": "fix tests", "status": "running", "workspace": "/home/u/ws", "model": "m"}})
		case u == "/v1/sessions/sess-1/messages":
			send(200, map[string]any{"queued": true, "turnId": "t1"})
		case u == "/v1/sessions/sess-1/goal":
			args, _ := b["args"].(string)
			if args != "" && args != "pause" && args != "resume" && args != "clear" {
				goal = args
			}
			out := "Goal set."
			if args == "pause" {
				out = "Goal paused."
			}
			send(200, map[string]any{"output": out, "status": map[string]any{"active": true, "paused": args == "pause"}})
		case u == "/v1/sessions/sess-1/state":
			send(200, jsonv.NewObject("goal", jsonv.NewObject("active", goal != "", "paused", false)))
		case u == "/v1/sessions/sess-1/direction":
			send(200, map[string]any{"delivered": false})
		case u == "/v1/sessions/sess-1":
			send(200, map[string]any{"id": "sess-1", "status": "running", "messages": 3, "history": history,
				"approvals": []any{map[string]any{"id": "ap1", "kind": "shell", "command": "make test"}}, "workspace": "/home/u/ws", "model": "m"})
		default:
			send(404, map[string]any{"detail": "no route"})
		}
	}))
	os.Exit(0)
}

func orNull(b []byte) []byte {
	if len(b) == 0 {
		return []byte("null")
	}
	return b
}

type fixture struct {
	s      *Sites
	pwLog  string
	daemon string
	spawns *atomic.Int32
	ix     string
}

func newFixture(t *testing.T, env map[string]string) *fixture {
	t.Helper()
	dir := t.TempDir()
	pwLog := filepath.Join(dir, "pw.log")
	fake := filepath.Join(dir, "pw")
	os.WriteFile(fake, []byte(`#!/bin/sh
echo "$@" >> `+pwLog+`
case "$1" in
  clusters) echo '[{"name":"cluster-a","type":"existing"},{"name":"bad name","type":"x"}]'; echo 'A newer CLI is available.' ;;
  ssh) echo "/home/u/studio-agents/ws" ;;
esac
`), 0o755)
	base := map[string]string{"PW_CLI": fake, "PW_GATEWAY_URL": "https://platform.test/api/openai/v1", "AGENT_LOCAL_SITE": "0", "PW_API_KEY": "deployment-key"}
	for k, v := range env {
		base[k] = v
	}
	ix := t.TempDir()
	p := platform.New(platform.Options{IndexBase: ix, Home: t.TempDir(), LookupEnv: func(k string) (string, bool) { v, ok := base[k]; return v, ok }})
	d := pwcode.New(p)
	daemonLog := filepath.Join(dir, "daemon.jsonl")
	spawns := &atomic.Int32{}
	exe, _ := os.Executable()
	d.SetBridgeSpawn(func(resource, key string) *exec.Cmd {
		spawns.Add(1)
		if resource != "cluster-a" || key != "user-key" {
			t.Errorf("bridge for %s with %s", resource, key)
		}
		cmd := exec.Command(exe, "-test.run=TestHelperSiteDaemon")
		cmd.Env = append(os.Environ(), "AS_HELPER=1", "AS_LOG="+daemonLog)
		return cmd
	})
	t.Cleanup(func() { d.CloseBridges() })
	return &fixture{s: &Sites{P: p, Daemon: d}, pwLog: pwLog, daemon: daemonLog, spawns: spawns, ix: ix}
}

type call struct {
	Method string         `json:"method"`
	URL    string         `json:"url"`
	Body   map[string]any `json:"body"`
}

func (f *fixture) calls(t *testing.T) []call {
	b, _ := os.ReadFile(f.daemon)
	var out []call
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var c call
		if json.Unmarshal([]byte(l), &c) == nil {
			out = append(out, c)
		}
	}
	return out
}

func TestSitesAndChecks(t *testing.T) {
	f := newFixture(t, nil)
	sites, err := f.s.ListSites(context.Background(), "user-key", false)
	if err != nil || string(jsonv.MustMarshal(sites)) != `[{"name":"cluster-a","kind":"cluster","type":"existing"}]` {
		t.Fatal(string(jsonv.MustMarshal(sites)), err)
	}
	if _, err := f.s.CheckSite("local", false); err == nil || !strings.Contains(err.Error(), "not offered") {
		t.Fatal(err)
	}
	if _, err := f.s.CheckSite("a b; rm -rf ~", false); err == nil || err.Error() != "not a site name: a b; rm -rf ~" {
		t.Fatal(err)
	}
	if _, err := f.s.ClientFor("cluster-a", "", false); err == nil || !strings.Contains(err.Error(), "your own platform key") {
		t.Fatal(err)
	}
	// The viewer's own key; the deployment's only on a one-person Studio.
	if f.s.AgentKey("user-key", false) != "user-key" || f.s.AgentKey("", false) != "deployment-key" || f.s.AgentKey("", true) != "" {
		t.Fatal("agentKey")
	}
	g := newFixture(t, map[string]string{"AGENT_LOCAL_SITE": ""})
	if !g.s.LocalSiteAllowed(false) || g.s.LocalSiteAllowed(true) {
		t.Fatal("local site offer")
	}
	if l, _ := g.s.ListSites(context.Background(), "", false); string(jsonv.MustMarshal(l)) != `[{"name":"local","kind":"local"}]` {
		t.Fatal(string(jsonv.MustMarshal(l)))
	}
}

func TestStartStatusSteerGoal(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	r, err := f.s.Start(ctx, StartAgent{Site: "cluster-a", Key: "user-key", Objective: "Fix the failing tests in ~/proj", Goal: "make test passes", MaxTurns: 8, Model: "u:prov/model"}, false)
	if err != nil {
		t.Fatal(err)
	}
	got := string(jsonv.MustMarshal(r))
	want := `{"site":"cluster-a","session":"sess-1","workspace":"/home/u/studio-agents/ws","host":"login1","mode":"accept-edits","goal":"make test passes\n\nIf this is not met within 8 turns, stop and say what remains."}`
	if got != want {
		t.Fatal(got)
	}
	c := f.calls(t)
	var create, msg, goal int = -1, -1, -1
	for i, x := range c {
		switch {
		case x.URL == "/v1/sessions" && x.Method == "POST":
			create = i
		case strings.HasSuffix(x.URL, "/messages"):
			msg = i
		case strings.HasSuffix(x.URL, "/goal"):
			goal = i
		}
	}
	if create < 0 || msg < 0 || goal < 0 || msg > goal {
		t.Fatal(c)
	}
	cb, _ := json.Marshal(c[create].Body)
	if string(cb) != `{"model":"u:prov/model","permissionMode":"accept-edits","remoteControl":true,"workspace":"/home/u/studio-agents/ws"}` {
		t.Fatal(string(cb))
	}
	if c[msg].Body["text"] != "Fix the failing tests in ~/proj" {
		t.Fatal(c[msg].Body)
	}
	b, _ := os.ReadFile(f.pwLog)
	ssh := ""
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "ssh") {
			ssh = l
		}
	}
	if !regexp.MustCompile(`--platform-host platform\.test cluster-a mkdir -p -- ~/studio-agents/\S+ && cd -- ~/studio-agents/\S+ && pwd`).MatchString(ssh) {
		t.Fatal(ssh)
	}
	if f.spawns.Load() != 1 {
		t.Fatal("every daemon call shared one ssh bridge", f.spawns.Load())
	}

	st, err := f.s.Status(ctx, "cluster-a", "user-key", "sess-1", 12, false)
	if err != nil {
		t.Fatal(err)
	}
	if jsonv.Path(st, "status") != "running" || string(jsonv.MustMarshal(jsonv.Path(st, "goal"))) != `{"active":true,"paused":false}` ||
		jsonv.Path(st, "lastCheck", "reason") != "tests still failing" ||
		string(jsonv.MustMarshal(jsonv.Path(st, "approvals"))) != `[{"id":"ap1","kind":"shell","text":"shell: make test"}]` {
		t.Fatal(string(jsonv.MustMarshal(st)))
	}
	if !strings.Contains(string(jsonv.MustMarshal(jsonv.Path(st, "recent"))), "[goal] turn 2: not met yet, tests still failing") {
		t.Fatal(jsonv.Path(st, "recent"))
	}

	s, err := f.s.Steer(ctx, "cluster-a", "user-key", "sess-1", "use the debug partition", "direction", false)
	if err != nil || string(jsonv.MustMarshal(s)) != `{"queued":true,"asMessage":true}` {
		t.Fatal(s, err)
	}
	pv, err := f.s.Goal(ctx, "cluster-a", "user-key", "sess-1", "pause", "", 0, false)
	if err != nil || jsonv.Path(pv, "status", "paused") != true {
		t.Fatal(pv, err)
	}
	if _, err := f.s.Goal(ctx, "cluster-a", "user-key", "sess-1", "set", "", 0, false); err == nil || !strings.Contains(err.Error(), "condition is required") {
		t.Fatal(err)
	}
	if _, err := f.s.Start(ctx, StartAgent{Site: "cluster-a", Key: "user-key", Objective: "x", Workspace: "~/a; rm -rf /"}, false); err == nil || !strings.HasPrefix(err.Error(), "workspace") {
		t.Fatal(err)
	}
	if _, err := f.s.Start(ctx, StartAgent{Site: "cluster-a", Key: "user-key", Objective: "x", PermissionMode: "bypass-permissions"}, false); err == nil || err.Error() != "permission mode: one of read-only, accept-edits, plan" {
		t.Fatal(err)
	}
	if _, err := CheckSession("../../etc"); err == nil || err.Error() != "not a session id" {
		t.Fatal(err)
	}
}

func TestBoundedGoal(t *testing.T) {
	if !strings.Contains(BoundedGoal("done", 0), "within 20 turns") || !strings.Contains(BoundedGoal("done", 999), "within 200 turns") || len(BoundedGoal(strings.Repeat("x", 5000), 5)) >= 4000 {
		t.Fatal("bounds")
	}
}

// The routes, through a mux: keys, errors as Node's error handler writes
// them, and the local site's workspace under INDEX_BASE/agents.
func TestRoutes(t *testing.T) {
	sock := pwcodetest.Socket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/v1/status":
			io.WriteString(w, `{"protocolVersion":2,"hostname":"here"}`)
		case "/v1/sessions":
			if r.Method == "POST" {
				io.WriteString(w, `{"id":"s1"}`)
				return
			}
			io.WriteString(w, `[{"id":"s1","status":"idle"}]`)
		default:
			io.WriteString(w, `{"queued":false}`)
		}
	}))
	f := newFixture(t, map[string]string{"AGENT_LOCAL_SITE": "1", "PW_CODE_SOCKET": sock, "PW_API_KEY": ""})
	cfg := config.Config{IndexBase: f.ix}
	a := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	mux := http.NewServeMux()
	f.s.Routes(mux, a)
	do := func(method, path, body string) (int, string) {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, rd)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		// apierr.JSON ends an error body with a newline (json.Encoder).
		return w.Code, strings.TrimSuffix(w.Body.String(), "\n")
	}
	if c, b := do("GET", "/api/agent-sites", ""); c != 200 || b != `{"sites":[{"name":"local","kind":"local"}],"localOffered":true}` {
		t.Fatal(c, b)
	}
	if c, b := do("GET", "/api/agent-sites/local/status", ""); c != 200 || b != `{"protocolVersion":2,"hostname":"here","usable":true,"why":null}` {
		t.Fatal(c, b)
	}
	if c, b := do("GET", "/api/agent-sites/local/sessions", ""); c != 200 || b != `{"sessions":[{"id":"s1","name":null,"status":"idle","workspace":null,"model":null,"updatedAt":null}]}` {
		t.Fatal(c, b)
	}
	c, b := do("POST", "/api/agent-sites/local/sessions", `{"objective":"Tidy","workspace":"~/w1","permissionMode":"plan"}`)
	if c != 200 || b != `{"site":"local","session":"s1","workspace":"`+filepath.Join(f.ix, "agents", "w1")+`","host":"here","mode":"plan","goal":null}` {
		t.Fatal(c, b)
	}
	if _, err := os.Stat(filepath.Join(f.ix, "agents", "w1")); err != nil {
		t.Fatal(err)
	}
	if c, b := do("POST", "/api/agent-sites/bad%20site/sessions/x/interrupt", ""); c != 400 || b != `{"error":"not a site name: bad site"}` {
		t.Fatal(c, b)
	}
	if c, b := do("POST", "/api/agent-sites/local/sessions/s1/steer", `{"text":"  "}`); c != 400 || b != `{"error":"text is required"}` {
		t.Fatal(c, b)
	}
	if c, b := do("POST", "/api/agent-sites/local/sessions/s1/goal", `{"action":"nope"}`); c != 400 || b != `{"error":"action: set, show, pause, resume, or clear"}` {
		t.Fatal(c, b)
	}
	if c, b := do("POST", "/api/agent-sites/local/sessions/s1/approvals/a%20b", `{}`); c != 400 || b != `{"error":"not an approval id"}` {
		t.Fatal(c, b)
	}
	if c, b := do("POST", "/api/agent-sites/local/sessions/s1/interrupt", ""); c != 200 || b != `{"ok":true}` {
		t.Fatal(c, b)
	}
	// A site with its path in one segment, as the browser encodes it.
	if c, b := do("GET", "/api/agent-sites/pw:%2F%2Fu%2Fc/sessions", ""); c != 409 || !strings.Contains(b, "your own platform key") {
		t.Fatal(c, b)
	}
}
