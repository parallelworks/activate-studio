package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsmd"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode/pwcodetest"
)

// daemon is a pw code daemon whose sessions answer as the test says.
type daemon struct {
	mu       sync.Mutex
	n        int
	created  []map[string]any
	agentsMD []string
	messages []string
	steered  []string
	goals    []string
	archived []string
	// per session: status, answer, approvals, tokens
	status    map[string]string
	answer    map[string]string
	approval  map[string]string
	tokens    map[string]float64
	goalAlive map[string]bool
}

func newDaemon() *daemon {
	return &daemon{status: map[string]string{}, answer: map[string]string{}, approval: map[string]string{}, tokens: map[string]float64{}, goalAlive: map[string]bool{}}
}

func (d *daemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	send := func(v any) { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(v) }
	d.mu.Lock()
	defer d.mu.Unlock()
	p := r.URL.Path
	if p == "/v1/sessions" && r.Method == http.MethodPost {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		d.created = append(d.created, req)
		b, _ := os.ReadFile(filepath.Join(req["workspace"].(string), "AGENTS.md"))
		d.agentsMD = append(d.agentsMD, string(b))
		d.n++
		id := "20261121-12000" + string(rune('0'+d.n)) + "-abcdef"
		d.status[id] = "idle"
		send(map[string]any{"id": id, "status": "idle"})
		return
	}
	rest, ok := strings.CutPrefix(p, "/v1/sessions/")
	if !ok {
		w.WriteHeader(404)
		return
	}
	id, action, _ := strings.Cut(rest, "/")
	if _, ok := d.status[id]; !ok {
		w.WriteHeader(404)
		send(map[string]any{"error": "no such session"})
		return
	}
	switch action {
	case "messages":
		var m struct{ Text string }
		json.NewDecoder(r.Body).Decode(&m)
		d.messages = append(d.messages, m.Text)
		d.status[id] = "running"
		send(map[string]any{"turnId": "t"})
	case "direction":
		var m struct{ Text string }
		json.NewDecoder(r.Body).Decode(&m)
		d.steered = append(d.steered, m.Text)
		send(map[string]any{"delivered": true})
	case "goal":
		var g struct{ Args string }
		json.NewDecoder(r.Body).Decode(&g)
		d.goals = append(d.goals, g.Args)
		d.goalAlive[id] = true
		send(map[string]any{"output": "ok"})
	case "state":
		send(map[string]any{"goal": map[string]any{"active": d.goalAlive[id]}})
	case "archive":
		d.archived = append(d.archived, id)
		w.WriteHeader(204)
	case "":
		out := map[string]any{"id": id, "status": d.status[id], "inputTokens": d.tokens[id], "outputTokens": 0.0,
			"history": []any{map[string]any{"kind": "user", "text": "wake"}, map[string]any{"kind": "assistant", "text": d.answer[id]}}}
		if a := d.approval[id]; a != "" {
			out["approvals"] = []any{map[string]any{"id": "ap1", "text": a}}
		}
		send(out)
	default:
		w.WriteHeader(404)
	}
}

// finish ends the session's round with an answer and a token count.
func (d *daemon) finish(id, answer string, tokens float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.status[id], d.answer[id], d.tokens[id], d.goalAlive[id] = "idle", answer, tokens, false
}

func stand(t *testing.T) (*Fleet, *daemon, string) {
	dir := t.TempDir()
	root, ix := filepath.Join(dir, "kb"), filepath.Join(dir, "ix")
	os.MkdirAll(filepath.Join(root, ".agents"), 0o755)
	os.WriteFile(filepath.Join(root, ".agents", "watcher.md"), []byte("---\nname: watcher\ndescription: Watches\n---\n\nClassify every failure.\n"), 0o644)
	d := newDaemon()
	sock := pwcodetest.Socket(t, d)
	env := map[string]string{"INDEX_BASE": ix, "KB_ROOT": root, "STUDIO_WEB_DIST": filepath.Join(dir, "web"), "STUDIO_NODE": "off", "GUFI_BIN": filepath.Join(dir, "no-gufi"), "PW_CODE_SOCKET": sock}
	t.Setenv("PW_CLI", filepath.Join(dir, "no-pw"))
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	mux := http.NewServeMux()
	kb.Register(mux, a)
	agentsmd.Register(mux, a)
	p := platform.New(platform.Options{IndexBase: ix, Home: t.TempDir(), LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }})
	svc := pwcode.New(p)
	f := New(a, func() pwcode.Client { return svc.Local() })
	return f, d, root
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for " + what)
}

func (f *Fleet) state(id string) string { return f.Get(id).State }

// An agent wakes into a pw code session in its own library folder, whose
// AGENTS.md carries its persona and its goal; the round's goal bounds it
// by turns; its answer goes to the journal and its tokens to the budget;
// a NEEDS DECISION line waits for a person, whose message wakes it again
// in the same session.
func TestWakeFollowDecide(t *testing.T) {
	f, d, root := stand(t)
	turns := 3.0
	a, err := f.Create(Input{Name: "Queue watcher", Goal: "Watch the queues.", Persona: "watcher", Every: "1h", MaxTurns: &turns}, &auth.User{ID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if !f.Tick(a.ID, "the schedule", false) {
		t.Fatal("no wake-up")
	}
	waitFor(t, "the session", func() bool { return f.Get(a.ID).Session != "" })
	got := f.Get(a.ID)
	if got.State != "working" || got.Workspace != filepath.Join(root, "agents/fleet", a.ID) || got.Ticks != 1 {
		t.Fatalf("agent %+v", got)
	}
	if md := d.agentsMD[0]; !strings.Contains(md, "Classify every failure.") || !strings.Contains(md, "Watch the queues.") || !strings.Contains(md, "NEEDS DECISION:") {
		t.Fatalf("AGENTS.md when the session was made:\n%s", md)
	}
	// It reads the library and lists its folder without asking, and its
	// AGENTS.md says how.
	if rules := fmt.Sprint(d.created[0]["allowedTools"]); !strings.Contains(rules, "GrepSearch("+filepath.ToSlash(root)+"/**)") || !strings.Contains(rules, "Bash(pwd)") {
		t.Fatalf("allowed tools: %s", rules)
	}
	if !strings.Contains(d.agentsMD[0], "append to a file by editing it") {
		t.Fatalf("no working note in AGENTS.md:\n%s", d.agentsMD[0])
	}
	if !strings.Contains(d.messages[0], "the schedule") || !strings.Contains(d.goals[0], "within 3 turns") {
		t.Fatalf("message %q goal %q", d.messages[0], d.goals[0])
	}
	f.follow(context.Background())
	if f.state(a.ID) != "working" {
		t.Fatal("settled while running")
	}
	d.finish(got.Session, "Checked two queues.\nNEEDS DECISION: resubmit run 42 on the debug partition?", 1200)
	f.follow(context.Background())
	got = f.Get(a.ID)
	if got.State != "input-required" || got.Note != "resubmit run 42 on the debug partition?" || got.Tokens != 1200 || got.NextTickAt == nil {
		t.Fatalf("after the round %+v", got)
	}
	// A person answers: the next wake-up goes to the same session.
	if !f.Message(a.ID, "yes, resubmit") || f.state(a.ID) != "idle" {
		t.Fatal("message")
	}
	f.Pass(context.Background())
	waitFor(t, "the second wake-up", func() bool { d.mu.Lock(); defer d.mu.Unlock(); return len(d.messages) == 2 })
	if len(d.created) != 1 || !strings.Contains(d.messages[1], "yes, resubmit") || strings.Contains(d.messages[1], "the schedule") || strings.Contains(d.messages[1], "This is a new session") {
		t.Fatalf("created %d, message %q", len(d.created), d.messages[1])
	}
	waitFor(t, "working", func() bool { return f.Get(a.ID).Note == "wake-up 2 working" })
	// A message while it works goes straight into the session.
	if !f.Message(a.ID, "also check the GPU queue") || len(d.steered) != 1 {
		t.Fatalf("steered %v", d.steered)
	}
	d.finish(got.Session, "Resubmitted.", 1500)
	f.follow(context.Background())
	got = f.Get(a.ID)
	if got.State != "idle" || got.Tokens != 1500 {
		t.Fatalf("after the second round %+v", got)
	}
	kinds := []string{}
	for _, e := range f.Journal(a.ID, 50) {
		kinds = append(kinds, e.Kind)
	}
	if j := strings.Join(kinds, ","); !strings.Contains(j, "created") || strings.Count(j, "result") != 2 || strings.Count(j, "tick") != 2 {
		t.Fatalf("journal %s", j)
	}
	if !f.Retire(a.ID) || len(d.archived) != 1 || f.state(a.ID) != "retired" {
		t.Fatalf("retire: archived %v", d.archived)
	}
}

// A session waiting for an approval is a question for the person; a
// session that is gone is replaced at the next wake-up, which tells the
// new session its latest results; a budget pauses the agent.
func TestApprovalGoneAndBudget(t *testing.T) {
	f, d, _ := stand(t)
	one := 2.0
	a, _ := f.Create(Input{Name: "Digest", Goal: "Write the digest.", Every: "manual", MaxTicks: &one}, nil)
	f.Tick(a.ID, "the person asked", true)
	waitFor(t, "the session", func() bool { return f.Get(a.ID).Session != "" })
	s1 := f.Get(a.ID).Session
	d.mu.Lock()
	d.approval[s1], d.status[s1] = "Run: rm -rf build", "awaiting_approval"
	d.mu.Unlock()
	f.follow(context.Background())
	if g := f.Get(a.ID); g.State != "input-required" || !strings.Contains(g.Note, "rm -rf build") {
		t.Fatalf("approval %+v", g)
	}
	d.mu.Lock()
	d.approval[s1] = ""
	d.mu.Unlock()
	d.finish(s1, "Digest written to reports/digest.md.", 10)
	f.follow(context.Background())
	if g := f.Get(a.ID); g.State != "idle" || g.NextTickAt != nil {
		t.Fatalf("after %+v", g)
	}
	d.mu.Lock()
	delete(d.status, s1)
	d.mu.Unlock()
	f.Tick(a.ID, "the person asked again", false)
	waitFor(t, "a new session", func() bool { g := f.Get(a.ID); return g.Session != "" && g.Session != s1 })
	if g := f.Get(a.ID); len(g.Sessions) != 1 || g.Sessions[0] != s1 || !strings.Contains(d.messages[1], "This is a new session") || !strings.Contains(d.messages[1], "Digest written") {
		t.Fatalf("renewed %+v\n%s", g, d.messages[1])
	}
	d.finish(f.Get(a.ID).Session, "Done again.", 5)
	f.follow(context.Background())
	if g := f.Get(a.ID); g.State != "paused" || !strings.Contains(g.Note, "wake-up budget of 2 used") {
		t.Fatalf("budget %+v", g)
	}
	if f.Tick(a.ID, "x", false) {
		t.Fatal("woke over its budget")
	}
}

// A record of the earlier engine is taken over: goal, persona, triggers,
// budget and journal kept, and what did not carry over said.
func TestTakeOver(t *testing.T) {
	f, _, _ := stand(t)
	dir := filepath.Join(f.dir, "watch-abcd")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "agent.json"), []byte(`{"id":"watch-abcd","name":"Watch","goal":"Watch runs.","persona":"watcher","model":"gpt-x","execution":"campaign","resource":"pw://u/cluster1","triggers":{"every":"30m","onRunEnd":true,"watchPath":null},"budget":{"maxTicks":10,"maxTokens":null},"state":"working","note":"tick 3 running","createdAt":"2026-11-01T00:00:00.000Z","updatedAt":"x","lastTickAt":null,"nextTickAt":null,"ticks":3,"tokens":5000,"currentTaskId":"t-9","inbox":[],"lastSeenMtime":0,"owner":"u1","conversationId":null}`), 0o644)
	os.WriteFile(filepath.Join(dir, "journal.jsonl"), []byte(`{"at":"2026-11-01T00:00:00.000Z","kind":"result","text":"Two runs failed.","taskId":"t-8"}`+"\n"), 0o644)
	a := f.Get("watch-abcd")
	if a == nil || a.Engine != "pwcode" || a.Folder != "agents/fleet/watch-abcd" || a.State != "idle" || a.Model != "" || a.Goal != "Watch runs." || str(a.Persona) != "watcher" ||
		a.Triggers.Every != "30m" || !a.Triggers.OnRunEnd || *a.Budget.MaxTicks != 10 || a.Ticks != 3 {
		t.Fatalf("taken over %+v", a)
	}
	for _, want := range []string{"platform workflow runs on pw://u/cluster1", "gpt-x is a Studio model", "5000 tokens", "(t-9) is not resumed", "task board"} {
		if !strings.Contains(a.Migrated, want) {
			t.Errorf("migrated note lacks %q: %s", want, a.Migrated)
		}
	}
	j := f.Journal(a.ID, 10)
	if len(j) != 2 || j[0].Text != "Two runs failed." || j[1].Kind != "migrated" {
		t.Fatalf("journal %+v", j)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "agent.json"))
	if strings.Contains(string(b), "currentTaskId") || strings.Contains(string(b), `"execution"`) {
		t.Fatalf("the earlier engine's fields were written again:\n%s", b)
	}
	if !strings.Contains(instructions(a), "The system you work on: pw://u/cluster1") {
		t.Fatal("the system is not in its instructions")
	}
}

func TestCreateChecks(t *testing.T) {
	f, _, _ := stand(t)
	for _, in := range []Input{{Name: "x"}, {Goal: "y"}, {Name: "x", Goal: "y", Model: "gpt-x"}, {Name: "x", Goal: "y", WatchPath: "../out"}} {
		if _, err := f.Create(in, nil); err == nil {
			t.Errorf("%+v was taken", in)
		}
	}
	a, err := f.Create(Input{Name: "  Nightly Bench!  ", Goal: "g", Model: "claude-code/haiku", WatchPath: "/reports/"}, nil)
	if err != nil || !strings.HasPrefix(a.ID, "nightly-bench-") || a.Model != "claude-code/haiku" || str(a.Triggers.WatchPath) != "reports" || a.Triggers.Every != "1h" {
		t.Fatalf("%+v %v", a, err)
	}
}
