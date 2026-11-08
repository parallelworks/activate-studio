package tasks

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"context"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode/pwcodetest"
	"os"
	"path/filepath"
	"regexp"
)

// sessionAgents.test.mjs: delegated agents as sessions in a fake pw code
// daemon on a Unix socket.

type fakeSession struct {
	ID        string           `json:"id"`
	Status    string           `json:"status"`
	History   []map[string]any `json:"history"`
	Approvals []map[string]any `json:"approvals"`
	Workspace string           `json:"workspace"`
	Model     string           `json:"model"`
	Messages  int              `json:"messages"`
	mcpPolls  int
}

type fakeDaemon struct {
	mu         sync.Mutex
	sessions   map[string]*fakeSession
	n          int
	creates    []map[string]any
	messages   []map[string]any
	answers    []map[string]any
	directions []map[string]any
	interrupts []string
}

func reply(text string) map[string]any {
	return map[string]any{"kind": "assistant", "fromMain": true, "text": text}
}

func (d *fakeDaemon) behave(s *fakeSession, text string) {
	s.History = append(s.History, map[string]any{"kind": "user", "fromMain": true, "text": text})
	switch {
	case strings.Contains(text, "needs-approval"):
		s.Status = "awaiting_approval"
		s.History = append(s.History, map[string]any{"kind": "tool_call", "fromMain": true, "toolName": "Bash", "toolArgs": `{"command":"ls /data"}`})
		s.Approvals = []map[string]any{{"id": "ap1", "kind": "confirm", "header": "Shell command", "command": "ls /data"}}
	case strings.Contains(text, "fail-fast"):
		s.Status = "idle"
	case strings.Contains(text, "slow"):
		s.Status = "running"
		s.History = append(s.History, map[string]any{"kind": "tool_call", "fromMain": true, "toolName": "mcp__task__search_kb", "toolArgs": `{"query":"x"}`})
	default:
		s.Status = "running"
		s.History = append(s.History, map[string]any{"kind": "tool_call", "fromMain": true, "toolName": "mcp__task__search_kb", "toolArgs": `{"query":"decks"}`},
			map[string]any{"kind": "tool_result", "fromMain": true, "text": "3 hits"})
		time.AfterFunc(120*time.Millisecond, func() {
			d.mu.Lock()
			s.History = append(s.History, reply(`{"result": "session work done"}`))
			s.Status = "idle"
			d.mu.Unlock()
		})
	}
}

func (d *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	b := map[string]any{}
	json.Unmarshal(raw, &b)
	send := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if v != nil {
			json.NewEncoder(w).Encode(v)
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	u := r.URL.Path
	if u == "/v1/status" {
		send(200, map[string]any{"protocolVersion": 2, "version": "v-test", "pid": 1, "hostname": "node-7", "sessions": len(d.sessions), "remoteStart": true, "remoteMaxPermissionMode": "read-only"})
		return
	}
	if r.Method == "POST" && u == "/v1/sessions" {
		d.n++
		id := "sess-" + jsonv.FormatNumber(float64(d.n))
		d.creates = append(d.creates, b)
		model, _ := b["model"].(string)
		ws, _ := b["workspace"].(string)
		d.sessions[id] = &fakeSession{ID: id, Status: "idle", History: []map[string]any{}, Workspace: ws, Model: model}
		send(200, d.sessions[id])
		return
	}
	rest := strings.TrimPrefix(u, "/v1/sessions/")
	id, sub, _ := strings.Cut(rest, "/")
	s := d.sessions[id]
	if s == nil {
		send(404, map[string]any{"detail": "no such session"})
		return
	}
	switch {
	case r.Method == "GET" && sub == "":
		send(200, s)
	case r.Method == "GET" && sub == "mcp":
		s.mcpPolls++
		send(200, []any{map[string]any{"name": "task", "connected": s.mcpPolls > 2, "connecting": s.mcpPolls == 2}})
	case r.Method == "GET" && sub == "state":
		send(200, map[string]any{"inputTokens": 1200, "outputTokens": 300})
	case r.Method == "POST" && sub == "messages":
		d.messages = append(d.messages, map[string]any{"id": s.ID, "text": b["text"], "mcpConnected": s.mcpPolls > 2})
		d.behave(s, jsonv.String(b["text"]))
		send(200, map[string]any{"queued": false, "turnId": "t1"})
	case r.Method == "POST" && strings.HasPrefix(sub, "approvals/"):
		b["id"], b["approval"] = s.ID, strings.TrimPrefix(sub, "approvals/")
		d.answers = append(d.answers, b)
		s.Approvals = nil
		s.Status = "running"
		time.AfterFunc(80*time.Millisecond, func() {
			d.mu.Lock()
			s.History = append(s.History, reply(`{"result": "approved work done"}`))
			s.Status = "idle"
			d.mu.Unlock()
		})
		send(204, nil)
	case r.Method == "POST" && sub == "direction":
		d.directions = append(d.directions, map[string]any{"id": s.ID, "text": b["text"]})
		send(200, map[string]any{"delivered": true})
	case r.Method == "POST" && sub == "interrupt":
		d.interrupts = append(d.interrupts, s.ID)
		s.Status = "idle"
		send(200, map[string]any{})
	default:
		send(404, map[string]any{"detail": "unhandled"})
	}
}

func sessionEnv(t *testing.T) (*env, *Engine, *fakeDaemon) {
	d := &fakeDaemon{sessions: map[string]*fakeSession{}}
	sock := pwcodetest.Socket(t, d)
	v := newEnv(t, "#!/bin/sh\nexit 1\n", map[string]string{"PW_CODE_SOCKET": sock})
	v.mode = "sessions"
	return v, v.engine(), d
}

func TestSessionRuntimeChoice(t *testing.T) {
	v, e, _ := sessionEnv(t)
	if e.ResolveRuntime(nil, "local") != "session" || e.ResolveRuntime("runner", "local") != "session" || e.ResolveRuntime(nil, "campaign") != "runner" {
		t.Fatal("sessions mode")
	}
	v.mu.Lock()
	v.mode = "both"
	v.mu.Unlock()
	if e.ResolveRuntime("runner", "local") != "runner" || e.ResolveRuntime(nil, "local") != "session" {
		t.Fatal("both")
	}
}

func TestSessionAgentCarriesTheBoardAndCompletes(t *testing.T) {
	v, e, d := sessionEnv(t)
	m, err := e.Create(CreateOptions{Objective: "sessions", Model: "u:openai/m", Agents: []AgentSpec{{Objective: "summarize the decks"}}, MaxAgents: 2.0, MaxDepth: 0.0})
	if err != nil || m.Runtime != "session" || m.MaxDepth != 0 {
		t.Fatal(err, m.Runtime, m.MaxDepth)
	}
	until(t, "completed", 5000, func() bool { return e.Get(m.ID).Agents[0].State == Completed })
	a := e.Get(m.ID).Agents[0]
	d.mu.Lock()
	req := d.creates[len(d.creates)-1]
	msg := d.messages[len(d.messages)-1]
	d.mu.Unlock()
	if req["permissionMode"] != "read-only" || req["agent"] != "studio_task" || req["remoteControl"] != true || req["model"] != "u:openai/m" {
		t.Fatal(req)
	}
	tools := req["allowedTools"].([]any)
	if len(tools) != 7 || tools[0] != "mcp__task__search_kb" || tools[3] != "mcp__task__board_status" {
		t.Fatal(tools)
	}
	if req["agentsJson"] != `{"studio_task":{"description":"An agent working one subtask of an ACTIVATE Studio task","mcpServers":[{"task":{"type":"http","url":"http://127.0.0.1:4999/api/mcp","headers":{"Authorization":"Bearer `+m.Token+`","X-Task-Agent":"agent-1"}}}]}}` {
		t.Fatal(req["agentsJson"])
	}
	if !strings.Contains(jsonv.String(msg["text"]), "board_status") || msg["mcpConnected"] != true {
		t.Fatal(msg)
	}
	for _, b := range e.Get(m.ID).Board {
		if strings.Contains(b.Body, "did not connect") {
			t.Fatal("false alarm")
		}
	}
	if !strings.HasPrefix(a.sessionID(), "sess-") || a.Host != "node-7" {
		t.Fatal(a.SessionID, a.Host)
	}
	body, _ := os.ReadFile(filepath.Join(v.kb, a.resultPath()))
	if !strings.Contains(string(body), "session work done") {
		t.Fatal(string(body))
	}
	if got := string(jsonv.MustMarshal(a.Usage.value())); got != `{"input":1200,"output":300,"total":1500,"cost":null}` {
		t.Fatal(got)
	}
	live, _ := os.ReadFile(filepath.Join(v.ix, "tasks", m.ID, "work", a.Name, "live.log"))
	if !strings.Contains(string(live), "[mcp__task__search_kb]") {
		t.Fatal(string(live))
	}
	if _, err := os.Stat(filepath.Join(v.ix, "tasks", m.ID, "work", a.Name, ".agents")); err == nil {
		t.Fatal("no workspace settings file")
	}
	until(t, "task completed", 2000, func() bool { return e.Get(m.ID).State == Completed })
	// The agent's JSON keys: spawnAgent's, then sessionId, then usage, as
	// they were first set.
	got := string(jsonv.MustMarshal(a.value()))
	if !regexp.MustCompile(`^\{"name":"agent-1","persona":"","objective":"summarize the decks","parent":null,"depth":0,"state":"completed","startedAt":"[^"]+","updatedAt":"[^"]+","note":"finished","resultPath":"tasks/[^"]+/agent-1.md","runSlug":null,"host":"node-7","sessionId":"sess-\d+","usage":\{.*\}\}$`).MatchString(got) {
		t.Fatal(got)
	}
}

func TestSessionApprovalSteerStopAndFailure(t *testing.T) {
	_, e, d := sessionEnv(t)
	ctx := context.Background()
	m, _ := e.Create(CreateOptions{Objective: "approvals", Model: "u:openai/m", Agents: []AgentSpec{{Objective: "needs-approval to list data"}}, MaxAgents: 1.0, MaxDepth: 0.0})
	until(t, "input-required", 5000, func() bool { return e.Get(m.ID).Agents[0].State == InputRequired })
	a := e.Get(m.ID).Agents[0]
	if len(a.Approvals) != 1 || string(jsonv.MustMarshal(a.Approvals[0].value())) != `{"id":"ap1","kind":"confirm","text":"Shell command: ls /data"}` {
		t.Fatal(a.Approvals)
	}
	found := false
	for _, b := range e.Get(m.ID).Board {
		found = found || (b.Topic == "input-required" && strings.Contains(b.Body, "ls /data"))
	}
	if !found {
		t.Fatal("board")
	}
	if err := e.AnswerApproval(ctx, m.ID, a.Name, "nope", true, ""); err == nil || err.Error() != "no such approval request" {
		t.Fatal(err)
	}
	if err := e.AnswerApproval(ctx, m.ID, a.Name, "ap1", true, ""); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	ans, _ := json.Marshal(d.answers[len(d.answers)-1])
	d.mu.Unlock()
	if string(ans) != `{"allowed":true,"approval":"ap1","id":"`+a.sessionID()+`"}` {
		t.Fatal(string(ans))
	}
	until(t, "completed", 5000, func() bool { return e.Get(m.ID).Agents[0].State == Completed })

	m2, _ := e.Create(CreateOptions{Objective: "steer", Model: "u:openai/m", Agents: []AgentSpec{{Objective: "slow survey"}}, MaxAgents: 1.0, MaxDepth: 0.0})
	until(t, "message sent", 5000, func() bool {
		s := e.Get(m2.ID).Agents[0].sessionID()
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, x := range d.messages {
			if s != "" && x["id"] == s {
				return true
			}
		}
		return false
	})
	until(t, "watched", 2000, func() bool { return e.Get(m2.ID).Pollers == 1 })
	a2 := e.Get(m2.ID).Agents[0]
	if ok, err := e.Steer(ctx, m2.ID, a2.Name, "focus on the 2024 runs"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	d.mu.Lock()
	dir := d.directions[len(d.directions)-1]
	d.mu.Unlock()
	if dir["id"] != a2.sessionID() || dir["text"] != "focus on the 2024 runs" {
		t.Fatal(dir)
	}
	e.Stop(m2.ID)
	if e.Get(m2.ID).Agents[0].State != Canceled {
		t.Fatal("canceled")
	}
	until(t, "interrupted", 3000, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, x := range d.interrupts {
			if x == a2.sessionID() {
				return true
			}
		}
		return false
	})
	if _, err := e.Steer(ctx, m2.ID, a2.Name, "too late"); err == nil || err.Error() != "this agent is not a working session" {
		t.Fatal(err)
	}

	m3, _ := e.Create(CreateOptions{Objective: "failure", Model: "u:openai/m", Agents: []AgentSpec{{Objective: "fail-fast please"}}, MaxAgents: 1.0, MaxDepth: 0.0})
	until(t, "failed", 15000, func() bool { return e.Get(m3.ID).Agents[0].State == Failed })
	if !strings.Contains(e.Get(m3.ID).Agents[0].Note, "without a reply") {
		t.Fatal(e.Get(m3.ID).Agents[0].Note)
	}
}

func TestSessionReattachAfterRestart(t *testing.T) {
	v, e, _ := sessionEnv(t)
	m, _ := e.Create(CreateOptions{Objective: "steer", Model: "u:openai/m", Agents: []AgentSpec{{Objective: "slow survey"}}, MaxAgents: 1.0, MaxDepth: 0.0})
	until(t, "session", 5000, func() bool { return e.Get(m.ID).Pollers == 1 })
	second := v.engine()
	second.Rehydrate()
	re := second.Get(m.ID)
	if re.State != Working || re.Agents[0].Note != "reattached after a server restart" && re.Agents[0].Note != "calling mcp__task__search_kb" || re.Pollers != 1 {
		t.Fatal(re.State, re.Agents[0].Note, re.Pollers)
	}
	e.Stop(m.ID)
	second.Stop(m.ID)
}
