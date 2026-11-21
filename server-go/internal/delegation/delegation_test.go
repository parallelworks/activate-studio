package delegation

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/agentsmd"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode/pwcodetest"
)

// daemon is a pw code daemon with one session, s1, whose status the test
// sets; it keeps what the Studio asked of it, and what AGENTS.md said in
// the session's folder when the session was made.
type daemon struct {
	mu        sync.Mutex
	created   map[string]any
	agentsMD  string
	messages  []string
	goals     []string
	status    string
	lastError string
}

func (d *daemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	send := func(v any) { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(v) }
	d.mu.Lock()
	defer d.mu.Unlock()
	switch p := r.URL.Path; {
	case p == "/v1/status":
		send(map[string]any{"protocolVersion": pwcode.ProtocolVersion, "hostname": "box"})
	case p == "/v1/sessions" && r.Method == http.MethodPost:
		json.NewDecoder(r.Body).Decode(&d.created)
		b, _ := os.ReadFile(filepath.Join(d.created["workspace"].(string), "AGENTS.md"))
		d.agentsMD = string(b)
		send(map[string]any{"id": "20261121-120000-abcdef", "status": "idle"})
	case strings.HasSuffix(p, "/messages"):
		var m struct{ Text string }
		json.NewDecoder(r.Body).Decode(&m)
		d.messages = append(d.messages, m.Text)
		send(map[string]any{"turnId": "t1"})
	case strings.HasSuffix(p, "/goal"):
		var g struct{ Args string }
		json.NewDecoder(r.Body).Decode(&g)
		d.goals = append(d.goals, g.Args)
		send(map[string]any{"output": "goal set"})
	case strings.HasSuffix(p, "/state"):
		send(map[string]any{"goal": map[string]any{"active": d.status == "running"}})
	case p == "/v1/sessions/20261121-120000-abcdef":
		out := map[string]any{"id": "20261121-120000-abcdef", "status": d.status, "history": []any{}}
		if d.lastError != "" {
			out["lastError"] = d.lastError
		}
		send(out)
	default:
		w.WriteHeader(404)
	}
}

func (d *daemon) set(status, lastError string) {
	d.mu.Lock()
	d.status, d.lastError = status, lastError
	d.mu.Unlock()
}

func stand(t *testing.T) (*Service, *daemon, string, *httptest.Server) {
	dir := t.TempDir()
	root, ix := filepath.Join(dir, "kb"), filepath.Join(dir, "ix")
	os.MkdirAll(filepath.Join(root, ".agents"), 0o755)
	os.WriteFile(filepath.Join(root, ".agents", "reviewer.md"), []byte("---\nname: reviewer\ndescription: Reviews\n---\n\nCheck every claim.\n"), 0o644)
	d := &daemon{status: "running"}
	sock := pwcodetest.Socket(t, d)
	env := map[string]string{"INDEX_BASE": ix, "KB_ROOT": root, "STUDIO_WEB_DIST": filepath.Join(dir, "web"), "STUDIO_NODE": "off",
		"GUFI_BIN": filepath.Join(dir, "no-gufi"), "PW_CODE_SOCKET": sock}
	t.Setenv("PW_CLI", filepath.Join(dir, "no-pw"))
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	mux := http.NewServeMux()
	kb.Register(mux, a)
	agentsmd.Register(mux, a)
	Register(mux, a)
	p := platform.New(platform.Options{IndexBase: ix, Home: t.TempDir(), LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }})
	sites := &agentsites.Sites{P: p, Daemon: pwcode.New(p)}
	s := Default
	s.Sites = func() *agentsites.Sites { return sites }
	s.Poll = 20 * time.Millisecond
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, d, root, srv
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for " + what)
}

// Delegating with instructions makes a folder under agents/<name>/ whose
// AGENTS.md carries them before the session is made, starts the session
// there with the objective and goal, records it, and when the session
// settles indexes the folder and lists what the agent wrote there.
func TestDelegateToAFolder(t *testing.T) {
	s, d, root, srv := stand(t)
	r := httptest.NewRequest("POST", "/", nil)
	rec, err := s.Start(r, Start{Site: agentsites.Local, Objective: "Summarize the Q3 pump tests", Goal: "summary.md exists", Instructions: "Reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rec.Folder, "agents/reviewer/") || !strings.HasSuffix(rec.Folder, "-summarize-the-q3-pump-tests") || rec.Instructions != "reviewer" || rec.State != "working" {
		t.Fatalf("record %+v", rec)
	}
	if !strings.Contains(d.agentsMD, "Check every claim.") {
		t.Fatalf("AGENTS.md when the session was made: %q", d.agentsMD)
	}
	if d.created["workspace"] != filepath.Join(root, rec.Folder) || len(d.messages) != 1 || d.messages[0] != "Summarize the Q3 pump tests" || len(d.goals) != 1 || !strings.Contains(d.goals[0], "summary.md exists") {
		t.Fatalf("created %v messages %v goals %v", d.created, d.messages, d.goals)
	}
	os.WriteFile(filepath.Join(root, rec.Folder, "summary.md"), []byte("# Q3\n"), 0o644)
	d.set("idle", "")
	waitFor(t, "the record to finish", func() bool { g := s.get(rec.Session); return g != nil && g.State == "finished" })
	got := s.get(rec.Session)
	if strings.Join(got.Files, ",") != rec.Folder+"/summary.md" || got.Finished == "" {
		t.Fatalf("finished record %+v", got)
	}
	res, _ := http.Get(srv.URL + "/api/delegated")
	var body struct{ Sessions []Record }
	json.NewDecoder(res.Body).Decode(&body)
	if len(body.Sessions) != 1 || body.Sessions[0].Session != rec.Session {
		t.Fatalf("listed %+v", body.Sessions)
	}
}

// A named folder is used as given; a session that stops on an error is
// recorded as stopped with the error; an unknown template is refused
// before anything starts.
func TestDelegateNamedFolderAndStop(t *testing.T) {
	s, d, root, _ := stand(t)
	r := httptest.NewRequest("POST", "/", nil)
	if _, err := s.Start(r, Start{Site: agentsites.Local, Objective: "x", Instructions: "nobody"}); err == nil || d.created != nil {
		t.Fatalf("an unknown template: %v, created %v", err, d.created)
	}
	rec, err := s.Start(r, Start{Site: agentsites.Local, Objective: "Size the pumps", Folder: "reports/pumps"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Folder != "reports/pumps" || d.created["workspace"] != filepath.Join(root, "reports/pumps") || d.agentsMD != "" {
		t.Fatalf("record %+v created %v AGENTS.md %q", rec, d.created, d.agentsMD)
	}
	d.set("idle", "model refused")
	if !s.Look(context.Background(), rec.Session, "") {
		t.Fatal("a settled session's record did not finish")
	}
	if g := s.get(rec.Session); g.State != "stopped" || g.Error != "model refused" {
		t.Fatalf("record %+v", g)
	}
	if _, err := s.Start(r, Start{Site: agentsites.Local, Objective: "x", Folder: "../out"}); err == nil {
		t.Fatal("a folder outside the library was taken")
	}
}

func TestFolderFor(t *testing.T) {
	at := time.Date(2026, 11, 21, 9, 5, 0, 0, time.UTC)
	if got := folderFor("delegated", "Draft the RFQ: pumps, valves & seals for site B!", at); got != "agents/delegated/20261121-0905-draft-the-rfq-pumps-valves" {
		t.Fatal(got)
	}
	if got := folderFor("x", "!!!", at); got != "agents/x/20261121-0905" {
		t.Fatal(got)
	}
}
