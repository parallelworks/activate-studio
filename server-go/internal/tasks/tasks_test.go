package tasks

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/bridge"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

type env struct {
	kb, ix, bin string
	vars        map[string]string
	p           *platform.Platform
	mode        string
	indexed     []string
	mu          sync.Mutex
}

// newEnv is a KB, an index directory, and a fake pw whose script is body.
func newEnv(t *testing.T, body string, vars map[string]string) *env {
	t.Helper()
	e := &env{kb: t.TempDir(), ix: t.TempDir(), bin: t.TempDir(), vars: map[string]string{}, mode: "runner"}
	cli := filepath.Join(e.bin, "pw")
	os.WriteFile(cli, []byte(body), 0o755)
	e.vars["PW_CLI"] = cli
	for k, v := range vars {
		e.vars[k] = v
	}
	e.p = platform.New(platform.Options{IndexBase: e.ix, KBRoot: e.kb, Home: t.TempDir(), LookupEnv: func(k string) (string, bool) { v, ok := e.vars[k]; return v, ok }})
	return e
}

func (e *env) setCLI(t *testing.T, body string) {
	os.WriteFile(e.vars["PW_CLI"], []byte(body), 0o755)
}

func (e *env) engine() *Engine {
	return New(Config{IndexBase: e.ix, KBRoot: e.kb, Port: 4999, P: e.p, Daemon: pwcode.New(e.p),
		AgentExecution: func() string { e.mu.Lock(); defer e.mu.Unlock(); return e.mode },
		TaskTools:      func() []string { return append([]string{"search_kb"}, BoardTools...) },
		IndexDir:       func(rel string) { e.mu.Lock(); e.indexed = append(e.indexed, rel); e.mu.Unlock() },
		SessionPoll:    40 * time.Millisecond, CampaignPoll: 150 * time.Millisecond, Hostname: func() string { return "studio-host" }})
}

func until(t *testing.T, what string, ms int, cond func() bool) {
	t.Helper()
	end := time.Now().Add(time.Duration(ms) * time.Millisecond)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("not reached in time: %s", what)
}

func settled(e *Engine, id string) func() bool {
	return func() bool { s := e.Get(id); return s != nil && s.State != Working }
}

/* ---- tasks.test.mjs ---- */

const subtaskStub = `#!/bin/bash
prompt=""
while [ $# -gt 0 ]; do [ "$1" = "-p" ] && prompt="$2"; shift; done
if [[ "$prompt" == *root-task* ]]; then
  echo '{"result": "root analysis done", "subtasks": [{"objective": "leaf one"}, {"objective": "leaf two"}]}'
else
  echo '{"result": "leaf work done", "subtasks": [{"objective": "grandchild that must be refused"}]}'
fi
`

func TestSubSpawnWithinLimits(t *testing.T) {
	v := newEnv(t, subtaskStub, nil)
	e := v.engine()
	m, err := e.Create(CreateOptions{Objective: "test coordination", Model: "stub:model", Agents: []AgentSpec{{Objective: "root-task investigate"}}, MaxAgents: 10.0, MaxDepth: 1.0})
	if err != nil {
		t.Fatal(err)
	}
	until(t, "settled", 6000, settled(e, m.ID))
	s := e.Get(m.ID)
	if s.State != Completed || len(s.Agents) != 3 {
		t.Fatal(s.State, len(s.Agents))
	}
	leaves := 0
	for _, a := range s.Agents {
		if a.State != Completed {
			t.Fatal(a.Name, a.State)
		}
		if a.Depth == 1 {
			leaves++
			if a.Parent != s.Agents[0].Name {
				t.Fatal("lineage", a.Parent)
			}
		}
		if _, err := os.Stat(filepath.Join(v.kb, a.resultPath())); err != nil {
			t.Fatal(a.Name, "result on disk")
		}
		if _, err := os.Stat(filepath.Join(v.ix, "tasks", m.ID, "work", a.Name, ".agents")); err == nil {
			t.Fatal("no workspace settings")
		}
	}
	refused := 0
	for _, b := range s.Board {
		if b.Topic == "refused" {
			refused++
			if !strings.Contains(b.Body, "depth limit reached (1)") {
				t.Fatal(b.Body)
			}
		}
	}
	if leaves != 2 || refused != 2 {
		t.Fatal(leaves, refused)
	}
	if !sort.SliceIsSorted(s.Board, func(i, j int) bool { return s.Board[i].Seq < s.Board[j].Seq }) {
		t.Fatal("board order")
	}
	// The result file, as Node writes it, and the corpus is indexed.
	body, _ := os.ReadFile(filepath.Join(v.kb, s.Agents[0].resultPath()))
	want := "---\nmission: " + m.ID + "\nagent: agent-1\npersona: \n---\n\n# root-task investigate\n\nroot analysis done\n"
	if string(body) != want {
		t.Fatalf("%q", body)
	}
	until(t, "indexed", 2000, func() bool { v.mu.Lock(); defer v.mu.Unlock(); return len(v.indexed) == 3 })
	if v.indexed[0] != filepath.Join("tasks", m.ID) {
		t.Fatal(v.indexed)
	}
	if !regexp.MustCompile(`^\d{4}-\d\d-\d\d-test-coordination-[0-9a-z]{4}$`).MatchString(m.ID) || len(m.Token) != 48 {
		t.Fatal(m.ID, len(m.Token))
	}
}

func TestAgentCeiling(t *testing.T) {
	v := newEnv(t, subtaskStub, nil)
	e := v.engine()
	m, _ := e.Create(CreateOptions{Objective: "ceiling test", Model: "stub:model", Agents: []AgentSpec{{Objective: "root-task expand"}}, MaxAgents: 2.0, MaxDepth: 3.0})
	until(t, "settled", 6000, settled(e, m.ID))
	s := e.Get(m.ID)
	if len(s.Agents) > 2 {
		t.Fatal(len(s.Agents))
	}
	ok := false
	for _, b := range s.Board {
		ok = ok || (b.Topic == "refused" && strings.Contains(b.Body, "agent ceiling reached (2)"))
	}
	if !ok {
		t.Fatal("the refusal names the ceiling")
	}
}

/* ---- board.test.mjs, through the bridge route ---- */

func boardMux(t *testing.T, e *Engine) *http.ServeMux {
	cfg := config.Config{IndexBase: t.TempDir()}
	a := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	mux := http.NewServeMux()
	e.Routes(mux, a)
	return mux
}

func boardCall(t *testing.T, mux *http.ServeMux, signed bool, body any) (int, any) {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/internal/platform/board", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	if signed {
		bridge.Sign(req)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	v, _ := jsonv.Decode(w.Body.Bytes())
	return w.Code, v
}

func TestBoardOverTheBridge(t *testing.T) {
	t.Setenv("STUDIO_BRIDGE_SECRET", "board-test-secret")
	v := newEnv(t, "#!/bin/bash\nsleep 30\n", nil)
	e := v.engine()
	m, _ := e.Create(CreateOptions{Objective: "board test", Model: "stub", Agents: []AgentSpec{{Objective: "a"}, {Objective: "b"}}, MaxAgents: 4.0})
	defer e.Stop(m.ID)
	mux := boardMux(t, e)
	if c, _ := boardCall(t, mux, false, map[string]any{"token": m.Token, "agent": "x", "tool": ""}); c != 404 {
		t.Fatal("unsigned", c)
	}
	if _, r := boardCall(t, mux, true, map[string]any{"token": m.Token, "agent": "x", "tool": ""}); string(jsonv.MustMarshal(r)) != `{"found":true}` {
		t.Fatal(r)
	}
	if _, r := boardCall(t, mux, true, map[string]any{"token": "not-a-real-token", "agent": "x", "tool": "board_post", "args": map[string]any{"body": "hi"}}); string(jsonv.MustMarshal(r)) != `{"found":false}` {
		t.Fatal(r)
	}
	tool := func(agent, name string, args map[string]any) string {
		_, r := boardCall(t, mux, true, map[string]any{"token": m.Token, "agent": agent, "tool": name, "args": args})
		return string(jsonv.MustMarshal(r))
	}
	if r := tool("agent-1", "board_offer_task", map[string]any{"objective": "shared work"}); r != `{"found":true,"out":{"id":"s1","objective":"shared work","persona":"","claimedBy":null,"done":false,"resultPath":null}}` {
		t.Fatal(r)
	}
	if r := tool("agent-1", "board_claim", nil); r != `{"found":true,"out":{"claimed":{"id":"s1","objective":"shared work","persona":"","claimedBy":"agent-1","done":false,"resultPath":null},"reason":""}}` {
		t.Fatal(r)
	}
	if r := tool("agent-2", "board_claim", nil); r != `{"found":true,"out":{"claimed":null,"reason":"no unclaimed task"}}` {
		t.Fatal(r)
	}
	if r := tool("agent-2", "board_claim", map[string]any{"task_id": "s1"}); r != `{"found":true,"out":{"claimed":null,"reason":"already claimed by agent-1"}}` {
		t.Fatal(r)
	}
	tool("agent-1", "board_status", map[string]any{"note": "reading the corpus"})
	tool("agent-2", "board_post", map[string]any{"topic": "finding", "body": "two proposals overlap"})
	if r := tool("agent-1", "board_complete", map[string]any{"task_id": "s1", "result_path": "tasks/x.md"}); r != `{"found":true,"out":{"ok":true}}` {
		t.Fatal(r)
	}
	if r := tool("agent-1", "board_complete", map[string]any{"task_id": "s9"}); r != `{"found":true,"out":{"ok":false,"reason":"no such task"}}` {
		t.Fatal(r)
	}
	if r := tool("agent-1", "board_nope", nil); r != `{"found":true,"out":{"error":"unknown board tool"}}` {
		t.Fatal(r)
	}
	_, r := boardCall(t, mux, true, map[string]any{"token": m.Token, "agent": "agent-2", "tool": "board_read", "args": map[string]any{"since": "0"}})
	read := jsonv.Arr(jsonv.Path(r, "out"))
	var status, finding bool
	for _, x := range read {
		status = status || (jsonv.Path(x, "topic") == "status" && jsonv.Path(x, "from") == "agent-1")
		finding = finding || (jsonv.Path(x, "topic") == "finding" && strings.Contains(jsonv.String(jsonv.Path(x, "body")), "overlap"))
	}
	if !status || !finding {
		t.Fatal(read)
	}
	for _, a := range e.Get(m.ID).Agents {
		if a.Name == "agent-1" && a.Note != "reading the corpus" {
			t.Fatal(a.Note)
		}
	}
	if e.TaskByToken(m.Token) != m.ID || e.TaskByToken("") != "" {
		t.Fatal("token")
	}
}

/* ---- taskDurability.test.mjs ---- */

func TestRestartKeepsFinishedTasksAndInterruptsTheRest(t *testing.T) {
	v := newEnv(t, "#!/bin/bash\necho 'working on it' >&2\nprintf '%s' '{\"result\":\"all done\",\"subtasks\":[]}'\n", nil)
	first := v.engine()
	m, _ := first.Create(CreateOptions{Objective: "write the report", Model: "test-model", Agents: []AgentSpec{{Objective: "do it"}}})
	until(t, "done", 6000, settled(first, m.ID))

	second := v.engine()
	second.Rehydrate()
	var listed *jsonv.Object
	for _, x := range second.List() {
		if jsonv.Path(x, "id") == m.ID {
			listed = jsonv.Obj(x)
		}
	}
	if listed == nil || jsonv.Path(listed, "state") != Completed {
		t.Fatal(listed)
	}
	re := second.Get(m.ID)
	if len(re.Agents) != 1 || re.Agents[0].resultPath() == "" || len(re.Board) == 0 || re.Token != "" {
		t.Fatal(re)
	}
	// The snapshot round-trips byte for byte.
	snap, _ := os.ReadFile(filepath.Join(v.ix, "tasks", m.ID, "state.json"))
	if string(jsonv.MustMarshal(second.Summary(m.ID))) != string(snap) {
		t.Fatalf("%s\n%s", jsonv.MustMarshal(second.Summary(m.ID)), snap)
	}
	if !strings.HasPrefix(string(snap), `{"id":"`+m.ID+`","objective":"write the report","model":"test-model","state":"completed","createdAt":"`) ||
		!strings.Contains(string(snap), `"maxAgents":10,"maxDepth":2,"execution":"local","resource":null,"runtime":"runner","usage":null,"agents":[{"name":"agent-1","persona":"","objective":"do it","parent":null,"depth":0,"state":"completed","startedAt":"`) {
		t.Fatal(string(snap))
	}

	// Mid-flight when the process died: interrupted, not working.
	v.setCLI(t, "#!/bin/bash\nsleep 60\n")
	m2, _ := first.Create(CreateOptions{Objective: "long haul", Model: "test-model", Agents: []AgentSpec{{Objective: "never ends"}}})
	time.Sleep(300 * time.Millisecond)
	third := v.engine()
	third.Rehydrate()
	r2 := third.Get(m2.ID)
	if r2 == nil || r2.State != Canceled || r2.Agents[0].State != Canceled || !strings.Contains(r2.Agents[0].Note, "interrupted by a server restart") {
		t.Fatal(r2)
	}
	first.Stop(m2.ID)
	for _, x := range third.List() {
		if s := third.Get(jsonv.String(jsonv.Path(x, "id"))); s.Token != "" {
			t.Fatal("a rehydrated task holds no live token")
		}
	}
	if third.TaskByToken("") != "" {
		t.Fatal("empty token")
	}
}

func TestOutputTail(t *testing.T) {
	v := newEnv(t, "#!/bin/bash\necho 'step one' >&2\nprintf '%s' '{\"result\":\"ok\"}'\n", nil)
	e := v.engine()
	m, _ := e.Create(CreateOptions{Objective: "tail me", Model: "test-model", Agents: []AgentSpec{{Objective: "talk"}}})
	until(t, "done", 6000, settled(e, m.ID))
	tail, ok := e.AgentOutput(m.ID, "agent-1")
	if !ok || !strings.Contains(tail, "step one") || !strings.Contains(tail, `"result"`) {
		t.Fatalf("%q", tail)
	}
	if _, ok := e.AgentOutput(m.ID, "nope"); ok {
		t.Fatal("unknown agent")
	}
}

func TestRunnerFailureAndStop(t *testing.T) {
	v := newEnv(t, "#!/bin/bash\necho 'model not found' >&2\nexit 2\n", nil)
	e := v.engine()
	m, _ := e.Create(CreateOptions{Objective: "fails", Model: "x", Agents: []AgentSpec{{Objective: "y"}}})
	until(t, "failed", 6000, settled(e, m.ID))
	a := e.Get(m.ID).Agents[0]
	if a.State != Failed || !strings.HasPrefix(a.Note, "Command failed: "+v.vars["PW_CLI"]+" code -p ") || !strings.HasSuffix(a.Note, "") {
		t.Fatal(a.State, a.Note)
	}
	// Stopping kills a runner and cancels its agent.
	v.setCLI(t, "#!/bin/bash\nsleep 60\n")
	m2, _ := e.Create(CreateOptions{Objective: "stop me", Model: "x", Agents: []AgentSpec{{Objective: "y"}}})
	time.Sleep(200 * time.Millisecond)
	if !e.Stop(m2.ID) || e.Stop("nope") {
		t.Fatal("stop")
	}
	s := e.Get(m2.ID)
	if s.State != Canceled || s.Agents[0].State != Canceled || s.Agents[0].Note != "stopped by user" || s.Board[len(s.Board)-1].Body != "Stopped by user." {
		t.Fatal(s)
	}
}

/* ---- usageAndSeed.test.mjs (the task parts) ---- */

func TestUsage(t *testing.T) {
	dec := func(s string) any { v, _ := jsonv.DecodeString(s); return v }
	m := func(u *Usage) string { return string(jsonv.MustMarshal(u.value())) }
	if got := m(UsageOf(dec(`{"usage":{"prompt_tokens":120,"completion_tokens":30,"total_tokens":150},"total_cost_usd":0.004}`))); got != `{"input":120,"output":30,"total":150,"cost":0.004}` {
		t.Fatal(got)
	}
	if got := m(UsageOf(dec(`{"input_tokens":10,"output_tokens":5}`))); got != `{"input":10,"output":5,"total":15,"cost":null}` {
		t.Fatal(got)
	}
	if UsageOf(dec(`{"result":"done"}`)) != nil || UsageOf("plain text") != nil {
		t.Fatal("none")
	}
	c := 0.1
	sum := SumUsage([]*Agent{{Usage: &Usage{Input: 1, Output: 2, Total: 3}}, {Usage: &Usage{Input: 10, Output: 20, Total: 30, Cost: &c}}, {}})
	if m(sum) != `{"input":11,"output":22,"total":33,"cost":0.1}` || SumUsage([]*Agent{{}}) != nil {
		t.Fatal(m(sum))
	}
}

/* ---- campaign.test.mjs ---- */

func campaignCLI(state string) string {
	contract := `{"result":"campaign findings","subtasks":[]}`
	return `#!/bin/bash
S=` + state + `
echo "$*" >> $S/calls.log
sub="$1 $2 $3"
case "$sub" in
  "workflows get "*)
    if [ ! -f $S/runner-created ]; then echo '[ERROR] workflow not found' >&2; exit 1; fi
    echo '{"name":"studio-agent-runner"}' ;;
  "workflows create "*) touch $S/runner-created; echo '{}' ;;
  "workflows runs view")
    t=$(( $(cat $S/ticks) + 1 )); echo $t > $S/ticks
    if [ $t -lt 10 ]; then st=running; else st=completed; fi
    if [ $t -lt 3 ]; then step=running; else step=completed; fi
    echo "{\"slug\":\"agent-run-00001\",\"status\":\"$st\",\"executedJobs\":{\"agent\":{\"status\":\"$st\",\"steps\":[{\"name\":\"Run agent\",\"status\":\"$step\"}]}}}" ;;
  "workflows runs logs")
    t=$(cat $S/ticks)
    if [ $t -lt 10 ]; then printf 'setting up the agent...'; else printf 'setting up the agent...\nSTUDIO_RESULT_BEGIN\n%s\nSTUDIO_RESULT_END\ntrailing stderr tail' '` + contract + `'; fi ;;
  "workflows runs cancel") touch $S/canceled; echo '{}' ;;
  "workflows run "*) echo 0 > $S/ticks; echo '{"run":{"slug":"agent-run-00001","status":"running"}}' ;;
  *) echo '{}' ;;
esac
`
}

func TestCampaign(t *testing.T) {
	state := t.TempDir()
	v := newEnv(t, campaignCLI(state), nil)
	e := v.engine()
	if _, err := e.Create(CreateOptions{Objective: "x", Model: "m", Agents: []AgentSpec{{Objective: "y"}}, Execution: "campaign"}); err == nil || !strings.Contains(err.Error(), "needs a resource") {
		t.Fatal(err)
	}
	m, err := e.Create(CreateOptions{Objective: "campaign objective", Model: "test-model", Agents: []AgentSpec{{Objective: "study the thing"}}, Execution: "campaign", Resource: "pw://user/testcluster"})
	if err != nil || m.Execution != "campaign" || m.Runtime != "runner" {
		t.Fatal(err)
	}
	agent := func() Agent { return e.Get(m.ID).Agents[0] }
	until(t, "run slug", 10000, func() bool { return agent().RunSlug == "agent-run-00001" })
	until(t, "running note", 10000, func() bool { return strings.Contains(agent().Note, "running") })
	if !strings.Contains(agent().Note, "agent-run-00001") || agent().Host != "pw://user/testcluster" {
		t.Fatal(agent().Note, agent().Host)
	}
	until(t, "completed", 15000, func() bool { return agent().State == Completed })
	s := e.Get(m.ID)
	if s.State != Completed || s.Procs != 0 {
		t.Fatal(s.State, s.Procs)
	}
	body, _ := os.ReadFile(filepath.Join(v.kb, agent().resultPath()))
	if !strings.Contains(string(body), "campaign findings") {
		t.Fatal(string(body))
	}
	if tail, _ := e.AgentOutput(m.ID, agent().Name); !strings.Contains(tail, "setting up the agent") {
		t.Fatal(tail)
	}
	y, _ := os.ReadFile(filepath.Join(v.ix, "tasks", "agent-runner.yaml"))
	if string(y) != RunnerYAML() || !strings.Contains(string(y), "STUDIO_RESULT_BEGIN") {
		t.Fatal("runner yaml")
	}
	calls, _ := os.ReadFile(filepath.Join(state, "calls.log"))
	if !strings.Contains(string(calls), `workflows run -i {"resource":"pw://user/testcluster","prompt":"You are one agent in a coordinated task. Task objective: campaign objective`) ||
		!strings.Contains(string(calls), `"model":"test-model","agent_name":"agent-1","board_url":"","board_token":"","timeout_seconds":7200} studio-agent-runner -o json`) {
		t.Fatal(string(calls))
	}

	// Stopping cancels the platform run.
	os.Remove(filepath.Join(state, "ticks"))
	m2, _ := e.Create(CreateOptions{Objective: "to be stopped", Model: "test-model", Agents: []AgentSpec{{Objective: "long haul"}}, Execution: "campaign", Resource: "pw://user/testcluster"})
	until(t, "slug", 10000, func() bool { return e.Get(m2.ID).Agents[0].runSlug() != "" })
	e.Stop(m2.ID)
	until(t, "canceled on the platform", 5000, func() bool { _, err := os.Stat(filepath.Join(state, "canceled")); return err == nil })
	if e.Get(m2.ID).Agents[0].State != Canceled {
		t.Fatal("canceled")
	}

	// A restart reattaches to a running campaign.
	os.Remove(filepath.Join(state, "canceled"))
	m3, _ := e.Create(CreateOptions{Objective: "survives restarts", Model: "test-model", Agents: []AgentSpec{{Objective: "keep going"}}, Execution: "campaign", Resource: "pw://user/testcluster"})
	until(t, "slug", 10000, func() bool { return e.Get(m3.ID).Agents[0].runSlug() != "" })
	second := v.engine()
	second.Rehydrate()
	re := second.Get(m3.ID)
	if re.State != Working || re.Agents[0].State != Working || re.Agents[0].Note != "reattached after a server restart" && !strings.HasPrefix(re.Agents[0].Note, "run ") {
		t.Fatal(re.State, re.Agents[0].State, re.Agents[0].Note)
	}
	until(t, "finished under the new process", 15000, func() bool { return second.Get(m3.ID).Agents[0].State == Completed })
	if second.Get(m3.ID).Agents[0].resultPath() == "" {
		t.Fatal("result")
	}
	e.Stop(m3.ID)
	second.Stop(m3.ID)
}

func TestExtractRunnerResult(t *testing.T) {
	if r, ok := ExtractRunnerResult("a\nSTUDIO_RESULT_BEGIN\n  x\ny \nSTUDIO_RESULT_END\nz"); !ok || r != "x\ny" {
		t.Fatal(r)
	}
	if _, ok := ExtractRunnerResult("none"); ok {
		t.Fatal("none")
	}
}
