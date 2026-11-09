package tasks

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// fleet.test.mjs: ticks as delegated tasks, triggers, budgets, journals,
// and reload after a restart, against a fake pw code.
const fleetCLI = `#!/bin/bash
if [ "$1" = "code" ]; then
  prompt=""
  while [ $# -gt 0 ]; do [ "$1" = "-p" ] && prompt="$2"; shift; done
  decision=""
  if echo "$prompt" | grep -qi decide; then decision=" NEEDS DECISION: which system should the next round use?"; fi
  printf '{"result":"Tick done. Checked the queue; nothing failed.%s","usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}' "$decision"
  exit 0
fi
printf '{}'
`

func obj(s string) *jsonv.Object { v, _ := jsonv.DecodeString(s); return jsonv.Obj(v) }

// idle reports whether no task of e is working.
func idle(e *Engine) bool {
	for _, x := range e.List() {
		if jsonv.Path(x, "state") == Working {
			return false
		}
	}
	return true
}

func kinds(f *Fleet, id string) []string {
	var out []string
	for _, e := range f.JournalTail(id, 40) {
		out = append(out, jsonv.String(jsonv.Path(e, "kind")))
	}
	return out
}

func lastOf(f *Fleet, id, kind string) string {
	text := ""
	for _, e := range f.JournalTail(id, 40) {
		if jsonv.Path(e, "kind") == kind {
			text = jsonv.String(jsonv.Path(e, "text"))
		}
	}
	return text
}

func TestFleet(t *testing.T) {
	v := newEnv(t, fleetCLI, nil)
	e := v.engine()
	f := NewFleet(e, nil)
	f.Start(nil, 25*time.Millisecond)
	t.Cleanup(func() {
		f.StopScheduler()
		until(t, "ticks finished", 6000, func() bool { return idle(e) })
		time.Sleep(50 * time.Millisecond)
	})
	a, err := f.Create(obj(`{"name":"Queue watcher","goal":"Watch the queue and report.","persona":"watcher","model":"m","every":"15m","onRunEnd":true}`), nil)
	if err != nil || gs(a, "state") != "idle" {
		t.Fatal(err)
	}
	id := gs(a, "id")
	until(t, "ticked", 6000, func() bool {
		b := f.Get(id)
		f.mu.Lock()
		defer f.mu.Unlock()
		return gn(b, "ticks") == 1 && gs(b, "state") == "idle"
	})
	got := f.Get(id)
	f.mu.Lock()
	b := got.Clone()
	f.mu.Unlock()
	if gn(b, "tokens") != 120 {
		t.Fatal(gn(b, "tokens"))
	}
	next, _ := platform.ParseISO(gs(b, "nextTickAt"))
	if time.Until(next) < 10*time.Minute {
		t.Fatal("next tick about fifteen minutes out", gs(b, "nextTickAt"))
	}
	if k := kinds(f, id); strings.Join(k[:4], ",") != "created,tick,result,state" {
		t.Fatal(k)
	}
	if !strings.Contains(lastOf(f, id, "result"), "Tick done") {
		t.Fatal(lastOf(f, id, "result"))
	}
	rec, err := os.ReadFile(filepath.Join(v.ix, "fleet", id, "agent.json"))
	if err != nil || !strings.HasPrefix(string(rec), "{\n \"id\": \""+id+"\",\n \"name\": \"Queue watcher\",\n \"goal\": \"Watch the queue and report.\",\n \"persona\": \"watcher\",\n \"model\": \"m\",\n \"execution\": \"local\",\n \"resource\": null,\n \"triggers\": {\n  \"every\": \"15m\",\n  \"onRunEnd\": true,\n  \"watchPath\": null\n },\n \"budget\": {\n  \"maxTicks\": null,\n  \"maxTokens\": null\n },\n \"state\": \"idle\",") ||
		!strings.Contains(string(rec), "\n \"inbox\": [],\n \"lastSeenMtime\": 0,\n \"owner\": null,\n \"conversationId\": null\n}") {
		t.Fatal(string(rec))
	}

	// A run ending wakes the agents that asked for it.
	f.NotifyRunEnded(RunEnded{Slug: "activatebatch-00012", Workflow: "activatebatch", Resource: "cluster-a", State: "completed"})
	until(t, "woke", 6000, func() bool {
		b := f.Get(id)
		f.mu.Lock()
		defer f.mu.Unlock()
		return gn(b, "ticks") == 2 && gs(b, "state") == "idle"
	})
	if !regexp.MustCompile(`activatebatch-00012 .* ended: completed`).MatchString(lastOf(f, id, "event")) || !strings.Contains(lastOf(f, id, "tick"), "activatebatch-00012") {
		t.Fatal(lastOf(f, id, "event"), lastOf(f, id, "tick"))
	}

	// Paused agents do not tick; a tick budget pauses; a decision parks.
	p, _ := f.Create(obj(`{"name":"Paused","goal":"Nothing yet.","model":"m","every":"15m"}`), nil)
	f.Pause(gs(p, "id"))
	time.Sleep(120 * time.Millisecond)
	if gn(f.Get(gs(p, "id")), "ticks") != 0 {
		t.Fatal("paused ticked")
	}
	bud, _ := f.Create(obj(`{"name":"Budgeted","goal":"One round only.","model":"m","every":"15m","maxTicks":1}`), nil)
	until(t, "budget pause", 6000, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return gs(f.agents[gs(bud, "id")], "state") == "paused" })
	if !strings.Contains(gs(f.Get(gs(bud, "id")), "note"), "tick budget of 1 used") {
		t.Fatal(gs(f.Get(gs(bud, "id")), "note"))
	}
	d, _ := f.Create(obj(`{"name":"Decider","goal":"Please decide the next system.","model":"m","every":"15m"}`), nil)
	did := gs(d, "id")
	until(t, "decision", 6000, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return gs(f.agents[did], "state") == "input-required" })
	if !strings.Contains(gs(f.Get(did), "note"), "which system") {
		t.Fatal(gs(f.Get(did), "note"))
	}
	f.Message(did, "Use the debug partition on cluster-a.")
	until(t, "answered", 6000, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return gn(f.agents[did], "ticks") == 2 })
	if !strings.Contains(lastOf(f, did, "message"), "debug partition") {
		t.Fatal(lastOf(f, did, "message"))
	}

	// After a restart the fleet reloads and an interrupted tick goes idle.
	w, _ := f.Create(obj(`{"name":"Restarter","goal":"Persist.","model":"m","every":"manual"}`), nil)
	wid := gs(w, "id")
	recPath := filepath.Join(v.ix, "fleet", wid, "agent.json")
	raw, _ := os.ReadFile(recPath)
	r := obj(string(raw))
	r.Set("state", "working")
	r.Set("currentTaskId", "gone")
	os.WriteFile(recPath, jsonv.MustMarshal(r), 0o644)
	fresh := NewFleet(v.engine(), nil)
	n := fresh.Start(nil, time.Hour)
	fresh.StopScheduler()
	if n < 4 {
		t.Fatal("reloaded", n)
	}
	back := fresh.Get(wid)
	if gs(back, "state") != "idle" || !strings.Contains(gs(back, "note"), "interrupted by a restart") {
		t.Fatal(gs(back, "state"), gs(back, "note"))
	}
}

func TestFleetWatchPathAndValidation(t *testing.T) {
	v := newEnv(t, fleetCLI, nil)
	eng := v.engine()
	f := NewFleet(eng, nil)
	t.Cleanup(func() {
		until(t, "ticks finished", 6000, func() bool { return idle(eng) })
		time.Sleep(50 * time.Millisecond)
	})
	if _, err := f.Create(obj(`{"name":"  ","goal":"x","model":"m"}`), nil); err == nil || err.Error() != "an agent needs a name and a goal" {
		t.Fatal(err)
	}
	if _, err := f.Create(obj(`{"name":"c","goal":"x","model":"m","execution":"campaign"}`), nil); err == nil || err.Error() != "campaign execution needs a resource" {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(v.kb, "tasks", "sub"), 0o755)
	os.WriteFile(filepath.Join(v.kb, "tasks", "sub", "a.md"), []byte("x"), 0o644)
	a, _ := f.Create(obj(`{"name":"Reviewer","goal":"Review.","model":"m","every":"manual","watchPath":" tasks "}`), "u1")
	id := gs(a, "id")
	if jsonv.Path(a, "triggers", "watchPath") != "tasks" || gn(a, "lastSeenMtime") == 0 || gv(a, "owner") != "u1" || gv(a, "nextTickAt") == nil {
		t.Fatal(string(jsonv.MustMarshal(a)))
	}
	// A manual agent created now is due now: the first pass ticks it.
	f.Pass()
	until(t, "ticked", 6000, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return gs(f.agents[id], "state") == "idle" && gn(f.agents[id], "ticks") == 1
	})
	if gv(f.Get(id), "nextTickAt") != nil || gs(f.Get(id), "note") != "waiting for an event or a message" {
		t.Fatal(gs(f.Get(id), "note"))
	}
	time.Sleep(20 * time.Millisecond)
	os.WriteFile(filepath.Join(v.kb, "tasks", "sub", "b.md"), []byte("y"), 0o644)
	f.Pass()
	until(t, "watch tick", 6000, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return gn(f.agents[id], "ticks") >= 2 })
	if lastOf(f, id, "event") != "New or changed files under tasks" {
		t.Fatal(lastOf(f, id, "event"))
	}
}

/* ---- the routes ---- */

func routeMux(t *testing.T, v *env, delegation *bool) (*http.ServeMux, *Engine, *Fleet) {
	cfg := config.Config{IndexBase: v.ix}
	a := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	e := v.engine()
	f := NewFleet(e, nil)
	mux := http.NewServeMux()
	e.Routes(mux, a)
	f.Routes(mux, a, v.p, func() bool { return *delegation })
	return mux, e, f
}

func do(mux *http.ServeMux, method, path, body string) (int, string) {
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
	// apierr ends an error body with a newline (json.Encoder).
	return w.Code, strings.TrimSuffix(w.Body.String(), "\n")
}

func TestRoutes(t *testing.T) {
	v := newEnv(t, "#!/bin/bash\nprintf '%s' '{\"result\":\"ok\"}'\n", map[string]string{"PW_CODE_SOCKET": filepath.Join(t.TempDir(), "none.sock")})
	on := true
	mux, e, _ := routeMux(t, v, &on)
	if c, b := do(mux, "GET", "/api/tasks", ""); c != 200 || b != `{"tasks":[]}` {
		t.Fatal(c, b)
	}
	if c, b := do(mux, "POST", "/api/tasks", `{"objective":"x"}`); c != 400 || b != `{"error":"objective, model, and at least one agent are required"}` {
		t.Fatal(c, b)
	}
	if c, b := do(mux, "POST", "/api/tasks", `{"objective":5,"model":"m","agents":[{}]}`); c != 400 || b != `{"error":"s.toLowerCase is not a function"}` {
		t.Fatal(c, b)
	}
	c, b := do(mux, "POST", "/api/tasks", `{"objective":"Route task","model":"m","agents":[{"objective":"go"}],"maxAgents":"3","maxDepth":null}`)
	if c != 200 || !strings.Contains(b, `"objective":"Route task","model":"m","state":"working",`) || !strings.Contains(b, `"maxAgents":3,"maxDepth":2,"execution":"local","resource":null,"runtime":"runner","usage":null,"agents":[{"name":"agent-1"`) {
		t.Fatal(c, b)
	}
	id := jsonv.String(jsonv.Path(obj(b), "id"))
	until(t, "done", 6000, settled(e, id))
	if c, b := do(mux, "GET", "/api/tasks/"+id+"?since=1", ""); c != 200 || !strings.Contains(b, `"board":[{"seq":2,`) {
		t.Fatal(c, b)
	}
	if c, b := do(mux, "GET", "/api/tasks/"+id+"?since=abc", ""); c != 200 || !strings.HasSuffix(b, `"board":[]}`) {
		t.Fatal(c, b)
	}
	if c, b := do(mux, "GET", "/api/tasks/nope", ""); c != 404 || b != `{"error":"no such task"}` {
		t.Fatal(c, b)
	}
	if c, b := do(mux, "GET", "/api/tasks/"+id+"/agents/agent-1/output", ""); c != 200 || !strings.HasPrefix(b, `{"tail":"{\"result\":\"ok\"}`) {
		t.Fatal(c, b)
	}
	if c, b := do(mux, "GET", "/api/tasks/"+id+"/agents/x/output", ""); c != 404 || b != `{"error":"no such task or agent"}` {
		t.Fatal(c, b)
	}
	if c, b := do(mux, "GET", "/api/tasks/runtime", ""); c != 200 || b != `{"mode":"runner","local":{"available":true,"reason":"no daemon is running yet; the first session agent starts one","version":null,"protocol":null,"remoteStart":null,"remoteMaxPermissionMode":null}}` {
		t.Fatal(c, b)
	}
	if c, b := do(mux, "POST", "/api/tasks/"+id+"/agents/agent-1/approvals/a1", `{"allowed":true}`); c != 409 || b != `{"error":"this agent is not waiting for an approval"}` {
		t.Fatal(c, b)
	}
	if c, b := do(mux, "POST", "/api/tasks/"+id+"/agents/agent-1/direction", ""); c != 409 || b != `{"error":"this agent is not a working session"}` {
		t.Fatal(c, b)
	}
	if c, b := do(mux, "POST", "/api/tasks/nope/stop", ""); c != 404 || b != `{"error":"no such task"}` {
		t.Fatal(c, b)
	}
	if c, b := do(mux, "POST", "/api/tasks/"+id+"/stop", ""); c != 200 || b != `{"ok":true}` {
		t.Fatal(c, b)
	}
	if c, b := do(mux, "GET", "/api/tasks", ""); c != 200 || !regexp.MustCompile(`^\{"tasks":\[\{"id":"[^"]+","objective":"Route task","state":"canceled","createdAt":"[^"]+","usage":null,"execution":"local","resource":null,"runtime":"runner","agents":1,"running":0,"waiting":0\}\]\}$`).MatchString(b) {
		t.Fatal(c, b)
	}
}

func TestFleetRoutes(t *testing.T) {
	v := newEnv(t, fleetCLI, nil)
	on := true
	mux, eng, f := routeMux(t, v, &on)
	t.Cleanup(func() {
		until(t, "ticks finished", 6000, func() bool { return idle(eng) })
		time.Sleep(50 * time.Millisecond)
	})
	c, b := do(mux, "GET", "/api/fleet/templates", "")
	if c != 200 || !strings.HasPrefix(b, `{"templates":[{"key":"benchmark-campaign","name":"Nightly benchmark campaign","persona":"campaign_runner","every":"1d","onRunEnd":true,"watchPath":null,"execution":"local","goal":"Keep a small CPU benchmark`) || !strings.Contains(b, `"watchPath":"tasks"`) {
		t.Fatal(c, b)
	}
	if c, b := do(mux, "GET", "/api/fleet", ""); c != 200 || b != `{"agents":[]}` {
		t.Fatal(c, b)
	}
	if c, b := do(mux, "POST", "/api/fleet", ""); c != 500 || !strings.HasPrefix(b, `{"error":"internal error (ref `) {
		t.Fatal(c, b)
	}
	c, b = do(mux, "POST", "/api/fleet", `{"name":"Manual","goal":"Do it.","model":"m","every":"manual"}`)
	if c != 200 || !strings.HasPrefix(b, `{"agent":{"id":"manual-`) {
		t.Fatal(c, b)
	}
	id := jsonv.String(jsonv.Path(obj(b), "agent", "id"))
	if c, b := do(mux, "GET", "/api/fleet/"+id, ""); c != 200 || !strings.Contains(b, `"journal":[{"at":"`) || !strings.HasSuffix(b, `"currentTask":null}`) {
		t.Fatal(c, b)
	}
	if c, b := do(mux, "GET", "/api/fleet/nope", ""); c != 404 || b != `{"error":"no such agent"}` {
		t.Fatal(c, b)
	}
	c, b = do(mux, "POST", "/api/fleet/"+id+"/tick", "")
	if c != 200 || !regexp.MustCompile(`^\{"ok":true,"result":"\d{4}-\d\d-\d\d-you-are-the-standing-agent-manual-your-g-[0-9a-z]{4}"\}$`).MatchString(b) {
		t.Fatal(c, b)
	}
	if c, b := do(mux, "POST", "/api/fleet/"+id+"/tick", ""); c != 409 || b != `{"error":"cannot tick this agent in its current state"}` {
		t.Fatal(c, b)
	}
	until(t, "tick done", 6000, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return gs(f.agents[id], "state") == "idle" })
	for _, a := range []string{"pause", "resume", "message", "retire"} {
		if c, b := do(mux, "POST", "/api/fleet/"+id+"/"+a, `{"text":"hello"}`); c != 200 || b != `{"ok":true,"result":true}` {
			t.Fatal(a, c, b)
		}
	}
	if c, b := do(mux, "POST", "/api/fleet/"+id+"/pause", ""); c != 409 || b != `{"error":"cannot pause this agent in its current state"}` {
		t.Fatal(c, b)
	}
	// Delegation off: the gate's error is a 500, as Node's error handler
	// answers an Error that is no KbError; the templates stay open.
	on = false
	if c, b := do(mux, "GET", "/api/fleet", ""); c != 500 || !strings.HasPrefix(b, `{"error":"internal error (ref `) {
		t.Fatal(c, b)
	}
	if c, _ := do(mux, "GET", "/api/fleet/templates", ""); c != 200 {
		t.Fatal(c)
	}
}
