// Package tasks is delegated tasks and standing agents: a request that
// decomposes into subtasks worked by headless agents, visible as one tree
// (server/src/tasks.ts), the token board the agents coordinate through
// (mcp.ts's board tools, served to Node at /internal/platform/board), and
// the fleet of standing agents that live in ticks (server/src/fleet.ts).
//
// An agent runs one of three ways: a one-shot `pw code -p` run beside the
// server (the runner), a session in the local pw code daemon followed turn
// by turn, or a platform workflow run on a named system (a campaign).
// Results are corpus files under KB_ROOT/tasks/<id>/; the board is
// append-only JSONL under INDEX_BASE/tasks/<id>/ with a state.json
// snapshot beside it, both in Node's formats, so a restart (of either
// server) reloads them.
package tasks

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/workspace"
)

const (
	maxAgentsCap   = 24
	maxDepthCap    = 3
	agentTimeout   = 15 * time.Minute
	sessionAgent   = "studio_task"
	runnerWorkflow = "studio-agent-runner"
)

// BoardTools are the board's MCP tools, in mcp.ts's order.
var BoardTools = []string{"board_post", "board_read", "board_status", "board_claim", "board_complete", "board_offer_task"}

// Persona is the part of a persona the engine needs: its name and file.
type Persona struct {
	Name string `json:"name"`
	File string `json:"file"`
}

// Config is what the engine reaches.
type Config struct {
	IndexBase, KBRoot string
	// Port is the port agents on this host reach the board on (LOCAL_URL).
	Port int
	P    *platform.Platform
	// Daemon is the pw code daemon access.
	Daemon *pwcode.Service
	// AgentExecution is effectiveSettings().agentExecution.
	AgentExecution func() string
	// Personas lists the persona library (extensions.ts's personas()).
	Personas func() []Persona
	// TaskTools are mcp.ts's taskToolNames().
	TaskTools func() []string
	// IndexDir indexes a corpus directory (incrementalIndexDir).
	IndexDir func(rel string)
	// Go runs a background job until the server stops (app.Go).
	Go func(name string, fn func(ctx context.Context))
	// SessionPoll and CampaignPoll are the watchers' intervals.
	SessionPoll, CampaignPoll time.Duration
	// Hostname is os.Hostname (tests).
	Hostname func() string
}

// Task is one delegated task.
type Task struct {
	ID, Token string
	Shared    []*SharedTask
	Objective string
	Model     string
	CreatedAt string
	State     string
	MaxAgents float64
	MaxDepth  float64
	Execution string // local or campaign
	Resource  any    // string or nil
	Runtime   string // runner or session

	nodes   []*Agent
	byName  map[string]*Agent
	board   []BoardMsg
	seq     float64
	procs   map[string]*runnerProc
	pollers map[string]context.CancelFunc
}

type runnerProc struct {
	cmd      *exec.Cmd
	canceled bool
}

func (m *Task) node(name string) *Agent { return m.byName[name] }

func (m *Task) addNode(a *Agent) {
	if m.byName[a.Name] == nil {
		m.nodes = append(m.nodes, a)
	} else {
		for i, x := range m.nodes {
			if x.Name == a.Name {
				m.nodes[i] = a
			}
		}
	}
	m.byName[a.Name] = a
}

// Finished is a task as the finish listeners see it: a snapshot taken
// when it reached a terminal state.
type Finished struct {
	ID, State string
	// First is the task's first agent, nil without one.
	First *Agent
	Usage *Usage
}

// Engine is one process's tasks.
type Engine struct {
	cfg Config
	dir string // INDEX_BASE/tasks

	mu        sync.Mutex
	tasks     map[string]*Task
	order     []string
	listeners []func(Finished)

	runnerMu      sync.Mutex
	runnerEnsured bool
}

// New is the engine for cfg.
func New(cfg Config) *Engine {
	if cfg.Go == nil {
		cfg.Go = func(_ string, fn func(context.Context)) { go fn(context.Background()) }
	}
	if cfg.SessionPoll <= 0 {
		cfg.SessionPoll = 2 * time.Second
	}
	if cfg.CampaignPoll <= 0 {
		cfg.CampaignPoll = 20 * time.Second
	}
	if cfg.Hostname == nil {
		cfg.Hostname = func() string { h, _ := os.Hostname(); return h }
	}
	if cfg.AgentExecution == nil {
		cfg.AgentExecution = func() string { return "runner" }
	}
	if cfg.Personas == nil {
		cfg.Personas = func() []Persona { return ReadPersonas(cfg.KBRoot, cfg.IndexBase) }
	}
	if cfg.TaskTools == nil {
		cfg.TaskTools = func() []string { return BoardTools }
	}
	if cfg.IndexDir == nil {
		cfg.IndexDir = func(string) {}
	}
	return &Engine{cfg: cfg, dir: filepath.Join(cfg.IndexBase, "tasks"), tasks: map[string]*Task{}}
}

// OnFinished adds a listener told when a task reaches a terminal state
// (the fleet ticks on it). Listeners run on their own goroutine.
func (e *Engine) OnFinished(fn func(Finished)) {
	e.mu.Lock()
	e.listeners = append(e.listeners, fn)
	e.mu.Unlock()
}

// notifyFinished runs the listeners on a snapshot of m; e.mu is held.
func (e *Engine) notifyFinished(m *Task) {
	f := Finished{ID: m.ID, State: m.State, Usage: SumUsage(m.nodes)}
	if len(m.nodes) > 0 {
		c := *m.nodes[0]
		f.First = &c
	}
	for _, fn := range e.listeners {
		go func() {
			defer func() { recover() }() // a listener must not break the engine
			fn(f)
		}()
	}
}

/* ---- text helpers, as JavaScript computes them ---- */

var (
	nonSlug   = regexp.MustCompile(`[^a-z0-9]+`)
	edgeDash  = regexp.MustCompile(`^-+|-+$`)
	wsTrim    = regexp.MustCompile(`^[\s\x{feff}\x{a0}]+|[\s\x{feff}\x{a0}]+$`)
	frontMat  = regexp.MustCompile(`^---\n(?s:.*?)\n---\n?`)
	resultRe  = regexp.MustCompile(`(?s)STUDIO_RESULT_BEGIN\n(.*?)\nSTUDIO_RESULT_END`)
	decisionR = regexp.MustCompile(`(?i)NEEDS DECISION:\s*(.+)`)
)

func trimJS(s string) string { return wsTrim.ReplaceAllString(s, "") }

// slice is s.slice(0, n) in UTF-16 code units (a surrogate pair is never
// split).
func slice(s string, n int) string {
	units := 0
	for i, r := range s {
		w := 1
		if r > 0xFFFF {
			w = 2
		}
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}

func slugN(s string, n int, fallback string) string {
	v := slice(edgeDash.ReplaceAllString(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), ""), n)
	if v == "" {
		return fallback
	}
	return v
}

func now() string { return platform.NowISO() }

func num(f float64) string { return jsonv.FormatNumber(f) }

// rand36 is Math.random().toString(36).slice(2, 2+n).
func rand36(n int) string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, n)
	for i := range b {
		k, _ := rand.Int(rand.Reader, big.NewInt(36))
		b[i] = digits[k.Int64()]
	}
	return string(b)
}

/* ---- the board and the snapshot ---- */

func (e *Engine) taskDir(id string) string { return filepath.Join(e.dir, id) }

// post records a board message; every state change routes through one,
// so it also writes the snapshot. e.mu is held.
func (e *Engine) post(m *Task, from, topic, body string) {
	m.seq++
	msg := BoardMsg{Seq: m.seq, At: now(), From: from, Topic: topic, Body: slice(body, 4000)}
	m.board = append(m.board, msg)
	if len(m.board) > 500 {
		m.board = append([]BoardMsg(nil), m.board[len(m.board)-500:]...)
	}
	dir := e.taskDir(m.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	line, _ := jsonv.Marshal(msg.value())
	f, err := os.OpenFile(filepath.Join(dir, "board.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	f.Write(append(line, '\n'))
	f.Close()
	// The token is not in the snapshot: a rehydrated task is history.
	snap, _ := jsonv.Marshal(e.summary(m))
	os.WriteFile(filepath.Join(dir, "state.json"), snap, 0o644)
}

// summary is taskSummary(m). e.mu is held.
func (e *Engine) summary(m *Task) *jsonv.Object {
	agents := make([]any, len(m.nodes))
	for i, a := range m.nodes {
		agents[i] = a.value()
	}
	return jsonv.NewObject("id", m.ID, "objective", m.Objective, "model", m.Model, "state", m.State,
		"createdAt", m.CreatedAt, "maxAgents", m.MaxAgents, "maxDepth", m.MaxDepth,
		"execution", m.Execution, "resource", m.Resource, "runtime", m.Runtime,
		"usage", SumUsage(m.nodes).value(), "agents", agents)
}

func alive(a *Agent) bool { return a.State == Working || a.State == InputRequired }

// maybeFinish completes a working task whose agents have all finished.
func (e *Engine) maybeFinish(m *Task) {
	if m.State != Working {
		return
	}
	for _, a := range m.nodes {
		if alive(a) {
			return
		}
	}
	m.State = Completed
	e.post(m, "orchestrator", "task", "Every agent has finished.")
	e.notifyFinished(m)
}

/* ---- the CLI ---- */

// pwRun is tasks.ts's pwRun: the CLI with a timeout (default 60 s) and
// 16 MiB buffers; a failure's error is its stderr, else its message.
func (e *Engine) pwRun(ctx context.Context, args []string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	out, err := e.cfg.P.Exec(ctx, args, platform.ExecOptions{Timeout: timeout, MaxBuffer: 16 << 20})
	if err != nil {
		return "", errors.New(platform.ErrText(err))
	}
	return out, nil
}

// RunnerYAML is the runner workflow registered once per account.
func RunnerYAML() string { return runnerYAML }

func (e *Engine) ensureRunnerWorkflow(ctx context.Context) error {
	e.runnerMu.Lock()
	defer e.runnerMu.Unlock()
	if e.runnerEnsured {
		return nil
	}
	if _, err := e.pwRun(ctx, []string{"workflows", "get", runnerWorkflow, "-o", "json"}, 0); err == nil {
		e.runnerEnsured = true
		return nil
	}
	tmp := filepath.Join(e.dir, "agent-runner.yaml")
	os.MkdirAll(e.dir, 0o755)
	if err := os.WriteFile(tmp, []byte(runnerYAML), 0o644); err != nil {
		return err
	}
	if _, err := e.pwRun(ctx, []string{"workflows", "create", "--yaml", tmp, runnerWorkflow}, 60*time.Second); err != nil {
		return err
	}
	e.runnerEnsured = true
	return nil
}

/* ---- prompts and results ---- */

// agentPrompt is agentPromptFor. It reads the persona library, so it is
// called without e.mu.
func (e *Engine) agentPrompt(taskObjective, persona, objective string, board bool) string {
	personaText := ""
	for _, p := range e.cfg.Personas() {
		if p.Name == persona {
			if b, err := os.ReadFile(filepath.Join(e.cfg.KBRoot, ".agents", p.File)); err == nil {
				personaText = trimJS(frontMat.ReplaceAllString(string(b), ""))
			}
			break
		}
	}
	parts := []string{}
	if personaText != "" {
		parts = append(parts, "Adopt this persona for the task:\n"+personaText)
	}
	parts = append(parts, "You are one agent in a coordinated task. Task objective: "+taskObjective, "Your task: "+objective)
	if board {
		parts = append(parts, "As you work, call the task MCP tool board_status before each major step with a one-line note of what you are doing; that note is what the operator watching the task sees. Post findings other agents need with board_post. The same server also carries the knowledge base search and read tools.")
	}
	parts = append(parts,
		"Work headlessly and do not ask questions. When finished, output ONLY a JSON object:",
		`{"result": "<your findings or work product, as markdown>", "subtasks": [{"persona": "<optional persona name>", "objective": "<subtask>"}]}`,
		"Include subtasks only when part of your task genuinely needs a separate agent; most tasks need none.")
	return strings.Join(parts, "\n\n")
}

// completeAgent is the one completion path for every worker kind. e.mu
// is held.
func (e *Engine) completeAgent(m *Task, p *Agent, rawText string) {
	text := trimJS(rawText)
	result := text
	var subtasks []any
	tryContract := func(v any) bool {
		o := jsonv.Obj(v)
		if o == nil || !(o.Has("result") || o.Has("subtasks")) {
			return false
		}
		r, _ := o.Get("result")
		result = jsonv.String(r)
		subtasks = jsonv.Arr(jsonv.Path(o, "subtasks"))
		return true
	}
	if env, err := jsonv.DecodeString(text); err == nil {
		if used := UsageOf(env); used != nil {
			p.setUsage(used)
		}
		if o := jsonv.Obj(env); o != nil && !tryContract(env) {
			var inner any
			for _, k := range []string{"content", "output", "message"} {
				if v, _ := o.Get(k); v != nil {
					inner = v
					break
				}
			}
			if unwrapped := jsonv.String(inner); unwrapped != "" {
				result = unwrapped
				if v, err := jsonv.DecodeString(unwrapped); err == nil {
					tryContract(v)
				}
			}
		}
	}

	relDir := filepath.Join("tasks", m.ID)
	rel := filepath.Join(relDir, p.Name+".md")
	if err := os.MkdirAll(filepath.Join(e.cfg.KBRoot, relDir), 0o755); err == nil {
		body := "---\nmission: " + m.ID + "\nagent: " + p.Name + "\npersona: " + p.Persona + "\n---\n\n# " + p.Objective + "\n\n" + result + "\n"
		if os.WriteFile(filepath.Join(e.cfg.KBRoot, rel), []byte(body), 0o644) == nil {
			p.ResultPath = rel
		}
	}
	go e.cfg.IndexDir(relDir)

	p.State = Completed
	p.Note = "finished"
	p.UpdatedAt = now()
	if rp := p.resultPath(); rp != "" {
		e.post(m, p.Name, "complete", "Result at "+rp)
	} else {
		e.post(m, p.Name, "complete", slice(result, 1500))
	}

	if len(subtasks) > 5 {
		subtasks = subtasks[:5]
	}
	for _, st := range subtasks {
		so := jsonv.Obj(st)
		objective := slice(jsonv.String(jsonv.Path(so, "objective")), 2000)
		if objective == "" {
			continue
		}
		persona := p.Persona
		if v, _ := so.Get("persona"); v != nil {
			persona = jsonv.String(v)
		}
		if _, err := e.spawnAgent(m, persona, objective, p.Name, p.Depth+1); err != nil {
			e.post(m, "orchestrator", "refused", "Subtask from "+p.Name+" refused: "+err.Error())
		}
	}
	e.maybeFinish(m)
}

// ExtractRunnerResult is what the poller takes from a campaign run's log:
// everything the runner bracketed between its markers.
func ExtractRunnerResult(log string) (string, bool) {
	m := resultRe.FindStringSubmatch(log)
	if m == nil {
		return "", false
	}
	return trimJS(m[1]), true
}

/* ---- spawning ---- */

// spawnAgent adds an agent to m and starts its worker. e.mu is held; the
// worker starts on its own goroutine.
func (e *Engine) spawnAgent(m *Task, persona, objective string, parent any, depth float64) (string, error) {
	if m.State != Working {
		return "", errors.New("task is not running")
	}
	if float64(len(m.nodes)) >= m.MaxAgents {
		return "", errors.New("agent ceiling reached (" + num(m.MaxAgents) + ")")
	}
	if depth > m.MaxDepth {
		return "", errors.New("depth limit reached (" + num(m.MaxDepth) + ")")
	}
	base := persona
	if base == "" {
		base = "agent"
	}
	name := slugN(base, 40, "task") + "-" + strconv.Itoa(len(m.nodes)+1)
	var host any = e.cfg.Hostname()
	if m.Execution == "campaign" {
		host = m.Resource
	}
	t := now()
	p := &Agent{Name: name, Persona: persona, Objective: objective, Parent: parent, Depth: depth,
		State: Working, StartedAt: t, UpdatedAt: t, Note: "starting", ResultPath: nil, RunSlug: nil, Host: host,
		order: append([]string(nil), baseOrder...)}
	m.addNode(p)
	shownPersona := persona
	if shownPersona == "" {
		shownPersona = "none"
	}
	line := name + " (persona " + shownPersona + ", depth " + num(depth)
	if ps, _ := parent.(string); ps != "" {
		line += ", child of " + ps
	}
	if m.Execution == "campaign" {
		line += ", campaign on " + jsString(m.Resource)
	}
	e.post(m, "orchestrator", "spawn", line+"): "+objective)

	switch {
	case m.Execution == "campaign":
		e.cfg.Go("campaign agent "+name, func(ctx context.Context) { e.submitCampaign(ctx, m, p) })
	case m.Runtime == "session":
		e.cfg.Go("session agent "+name, func(ctx context.Context) { e.startSession(ctx, m, p) })
	default:
		proc := &runnerProc{}
		m.procs[name] = proc
		e.cfg.Go("runner agent "+name, func(ctx context.Context) { e.runRunner(m, p, proc) })
	}
	return name, nil
}

// jsString is `${v}` for a string or null.
func jsString(v any) string {
	if v == nil {
		return "null"
	}
	return jsonv.String(v)
}

// runRunner is the one-shot `pw code -p` run beside the server.
func (e *Engine) runRunner(m *Task, p *Agent, proc *runnerProc) {
	prompt := e.agentPrompt(m.Objective, p.Persona, p.Objective, false)
	workdir := filepath.Join(e.taskDir(m.ID), "work", p.Name)
	os.MkdirAll(workdir, 0o755)
	args := []string{"code", "-p", prompt, "-o", "json", "--permission-mode", "read-only", "-w", workdir, "-m", m.Model}
	file := e.cfg.P.CLI()
	live, _ := os.OpenFile(filepath.Join(workdir, "live.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	stdout := &capped{max: 16 << 20, tee: live}
	stderr := &capped{max: 16 << 20, tee: live}
	cmd := exec.Command(file, args...)
	cmd.Env = e.cfg.P.Environ(nil)
	cmd.Stdout, cmd.Stderr = stdout, stderr

	e.mu.Lock()
	if proc.canceled {
		// Stopped before it started: what a kill on start would leave.
		delete(m.procs, p.Name)
		e.mu.Unlock()
		if live != nil {
			live.Close()
		}
		return
	}
	startErr := cmd.Start()
	if startErr == nil {
		proc.cmd = cmd
	}
	e.mu.Unlock()

	var runErr error
	killed := false
	if startErr != nil {
		runErr = errors.New("spawn " + file + " " + errnoName(startErr))
	} else {
		stop := func() { cmd.Process.Signal(syscall.SIGTERM) }
		stdout.stop, stderr.stop = stop, stop
		timer := time.AfterFunc(agentTimeout, func() { killed = true; stop() })
		err := cmd.Wait()
		timer.Stop()
		switch {
		case stdout.over:
			runErr = errors.New("stdout maxBuffer length exceeded")
		case stderr.over:
			runErr = errors.New("stderr maxBuffer length exceeded")
		case err != nil:
			runErr = errors.New("Command failed: " + strings.Join(append([]string{file}, args...), " ") + "\n" + stderr.String())
		}
	}
	_ = killed
	if live != nil {
		live.Close()
	}
	out := stdout.String()

	e.mu.Lock()
	defer e.mu.Unlock()
	delete(m.procs, p.Name)
	p.UpdatedAt = now()
	if p.State == Canceled {
		e.maybeFinish(m)
		return
	}
	if runErr != nil && out == "" {
		p.State = Failed
		p.Note = slice(runErr.Error(), 200)
		e.post(m, p.Name, "failed", p.Note)
		e.maybeFinish(m)
		return
	}
	e.completeAgent(m, p, trimJS(out))
}

func errnoName(err error) string {
	var en syscall.Errno
	if errors.As(err, &en) {
		switch en {
		case syscall.ENOENT:
			return "ENOENT"
		case syscall.EACCES:
			return "EACCES"
		}
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return "ENOENT"
	}
	return err.Error()
}

// capped buffers a stream up to max bytes (execFile's maxBuffer) and
// copies everything to tee as it arrives.
type capped struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	max  int
	over bool
	tee  *os.File
	stop func()
}

func (c *capped) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tee != nil {
		c.tee.Write(b)
	}
	if c.over {
		return len(b), nil
	}
	if c.buf.Len()+len(b) > c.max {
		c.buf.Write(b[:c.max-c.buf.Len()])
		c.over = true
		if c.stop != nil {
			c.stop()
		}
		return len(b), nil
	}
	return c.buf.Write(b)
}

func (c *capped) String() string { c.mu.Lock(); defer c.mu.Unlock(); return c.buf.String() }

/* ---- watchers ---- */

// watch runs tick now and every interval until it returns false or the
// poller is cleared. e.mu is held when called.
func (e *Engine) watch(m *Task, name string, every time.Duration, tick func(ctx context.Context) bool) {
	if old := m.pollers[name]; old != nil {
		old()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.pollers[name] = cancel
	e.cfg.Go("task watcher "+m.ID+"/"+name, func(stop context.Context) {
		defer cancel()
		go func() {
			select {
			case <-stop.Done():
				cancel()
			case <-ctx.Done():
			}
		}()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			if ctx.Err() != nil || !tick(ctx) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	})
}

// clearPoller stops an agent's watcher. e.mu is held.
func (e *Engine) clearPoller(m *Task, name string) {
	if c := m.pollers[name]; c != nil {
		c()
		delete(m.pollers, name)
	}
}

var runTerminal = map[string]bool{"completed": true, "error": true, "failed": true, "canceled": true, "cancelled": true}

// submitCampaign submits a campaign agent as a run of the runner workflow
// on the task's system, then watches the run.
func (e *Engine) submitCampaign(ctx context.Context, m *Task, p *Agent) {
	fail := func(err error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		p.State = Failed
		p.Note = "submission failed: " + slice(err.Error(), 200)
		p.UpdatedAt = now()
		e.post(m, p.Name, "failed", p.Note)
		e.maybeFinish(m)
	}
	if err := e.ensureRunnerWorkflow(ctx); err != nil {
		fail(err)
		return
	}
	inputs := jsonv.NewObject("resource", m.Resource, "prompt", e.agentPrompt(m.Objective, p.Persona, p.Objective, false),
		"model", m.Model, "agent_name", p.Name, "board_url", "", "board_token", "", "timeout_seconds", 7200.0)
	in, _ := jsonv.Marshal(inputs)
	cli := func(ctx context.Context, args []string, timeout time.Duration) (string, error) {
		return e.pwRun(ctx, args, timeout)
	}
	out, res, err := workspace.With(ctx, workspace.Runner{CLI: cli}, func(ctx context.Context) (string, error) {
		return e.pwRun(ctx, []string{"workflows", "run", "-i", string(in), runnerWorkflow, "-o", "json"}, 180*time.Second)
	})
	if err != nil {
		fail(err)
		return
	}
	if res.Started {
		e.mu.Lock()
		e.post(m, "orchestrator", "status", "the platform user workspace was not running; it was started and the submission retried")
		e.mu.Unlock()
	}
	head, err := platform.JSONHead(out)
	if err != nil {
		fail(err)
		return
	}
	run := jsonv.Path(head, "run")
	if run == nil {
		run = head
	}
	slugV := jsonv.Path(run, "slug")
	if slugV == nil {
		slugV = jsonv.Path(run, "id")
	}
	slug := jsonv.String(slugV)
	e.mu.Lock()
	defer e.mu.Unlock()
	p.RunSlug = slug
	if slug == "" {
		p.State = Failed
		p.Note = "submission failed: submission returned no run identifier"
		p.UpdatedAt = now()
		e.post(m, p.Name, "failed", p.Note)
		e.maybeFinish(m)
		return
	}
	e.post(m, "orchestrator", "spawn", p.Name+" submitted as run "+slug)
	e.watchCampaign(m, p)
}

// watchCampaign follows a campaign agent's platform run. e.mu is held.
func (e *Engine) watchCampaign(m *Task, p *Agent) {
	workdir := filepath.Join(e.taskDir(m.ID), "work", p.Name)
	os.MkdirAll(workdir, 0o755)
	live := filepath.Join(workdir, "live.log")
	e.watch(m, p.Name, e.cfg.CampaignPoll, func(ctx context.Context) bool {
		e.mu.Lock()
		if p.State != Working {
			e.clearPoller(m, p.Name)
			e.mu.Unlock()
			return false
		}
		slug := p.runSlug()
		e.mu.Unlock()
		out, err := e.pwRun(ctx, []string{"workflows", "runs", "view", slug, "-o", "json"}, 0)
		if err != nil {
			return true // transient; next tick
		}
		doc, err := platform.JSONHead(out)
		if err != nil {
			return true
		}
		job := jsonv.Obj(jsonv.Path(doc, "executedJobs", "agent"))
		var stepName string
		for _, st := range jsonv.Arr(jsonv.Path(job, "steps")) {
			s := jsonv.Path(st, "status")
			if jsonv.Truthy(s) && s != "completed" {
				if n := jsonv.Path(st, "name"); jsonv.Truthy(n) {
					stepName = jsonv.String(n)
				}
				break
			}
		}
		statusV := jsonv.Path(doc, "status")
		status := "unknown"
		if statusV != nil {
			status = jsonv.String(statusV)
		}
		note := "run " + slug + ": " + status
		if js := jsonv.Path(job, "status"); jsonv.Truthy(js) {
			note += ", job " + jsonv.String(js)
		}
		if stepName != "" {
			note += " (" + stepName + ")"
		}
		e.mu.Lock()
		p.Note = note
		p.UpdatedAt = now()
		e.mu.Unlock()
		log, err := e.pwRun(ctx, []string{"workflows", "runs", "logs", slug}, 60*time.Second)
		if err == nil {
			os.WriteFile(live, []byte(log), 0o644)
		}
		statusText := "undefined" // String(doc.status)
		if o := jsonv.Obj(doc); o != nil && o.Has("status") {
			statusText = jsString(statusV)
		}
		if !runTerminal[statusText] {
			return true
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		e.clearPoller(m, p.Name)
		if p.State != Working {
			return false
		}
		bracketed, ok := ExtractRunnerResult(log)
		if statusV == "completed" && ok {
			e.completeAgent(m, p, bracketed)
		} else {
			p.State = Failed
			p.Note = "run " + slug + " " + statusText
			if !ok {
				p.Note += ", no result markers in the log"
			}
			p.UpdatedAt = now()
			e.post(m, p.Name, "failed", p.Note)
			e.maybeFinish(m)
		}
		return false
	})
}

// failAgent fails a working agent. e.mu is held.
func (e *Engine) failAgent(m *Task, p *Agent, note string) {
	e.clearPoller(m, p.Name)
	if !alive(p) {
		return
	}
	p.State = Failed
	p.Note = slice(note, 300)
	p.setApprovals(nil)
	p.UpdatedAt = now()
	e.post(m, p.Name, "failed", p.Note)
	e.maybeFinish(m)
}

// historyLine is one history item as a line of the agent's live log.
func historyLine(h *jsonv.Object) (string, bool) {
	text := trimJS(field(h, "text"))
	switch field(h, "kind") {
	case "user":
		return "> " + text, true
	case "assistant":
		return text, text != ""
	case "tool_call":
		name := "tool"
		if v, _ := h.Get("toolName"); v != nil {
			name = jsonv.String(v)
		}
		return "[" + name + "] " + slice(field(h, "toolArgs"), 400), true
	case "tool_result":
		return "  result: " + slice(text, 600), text != ""
	case "notice":
		return "! " + text, text != ""
	}
	return "", false
}

// field is String(o[k] ?? ”).
func field(o *jsonv.Object, k string) string {
	v, _ := o.Get(k)
	return jsonv.String(v)
}

// describeApproval is what an approval request asks, in one line.
func describeApproval(a *jsonv.Object) string {
	head := truthyStr(jsonv.Path(a, "header"))
	if head == "" {
		head = truthyStr(jsonv.Path(a, "kind"))
	}
	what := ""
	for _, k := range []string{"command", "question", "reason"} {
		if what = truthyStr(jsonv.Path(a, k)); what != "" {
			break
		}
	}
	parts := []string{}
	for _, s := range []string{head, what} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return slice(strings.Join(parts, ": "), 400)
}

// startSession starts a session agent in the local daemon, with the board
// registered inline and the task's tools pre-approved; the token travels
// in the request body over the owner-only socket.
func (e *Engine) startSession(ctx context.Context, m *Task, p *Agent) {
	prompt := e.agentPrompt(m.Objective, p.Persona, p.Objective, true)
	workdir := filepath.Join(e.taskDir(m.ID), "work", p.Name)
	os.MkdirAll(workdir, 0o755)
	fail := func(err error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.failAgent(m, p, "the pw code session could not start: "+err.Error())
	}
	d := e.cfg.Daemon
	st, err := d.EnsureDaemon(ctx)
	if err != nil {
		fail(err)
		return
	}
	e.mu.Lock()
	if h := jsonv.Path(st, "hostname"); jsonv.Truthy(h) {
		p.Host = h
	}
	token, name, model := m.Token, p.Name, m.Model
	e.mu.Unlock()
	tools := []string{}
	for _, t := range e.cfg.TaskTools() {
		tools = append(tools, "mcp__task__"+t)
	}
	def := jsonv.NewObject(sessionAgent, jsonv.NewObject(
		"description", "An agent working one subtask of an ACTIVATE Studio task",
		"mcpServers", []any{jsonv.NewObject("task", jsonv.NewObject(
			"type", "http", "url", "http://127.0.0.1:"+strconv.Itoa(e.cfg.Port)+"/api/mcp",
			"headers", jsonv.NewObject("Authorization", "Bearer "+token, "X-Task-Agent", name)))},
	))
	agentsJSON, _ := jsonv.Marshal(def)
	req := jsonv.NewObject("workspace", workdir, "model", model, "permissionMode", "read-only",
		"agent", sessionAgent, "agentsJson", string(agentsJSON), "allowedTools", tools, "remoteControl", true)
	local := d.Local()
	session, err := pwcode.CreateSession(ctx, local, req)
	if err != nil {
		fail(err)
		return
	}
	sid := jsonv.Path(session, "id")
	id := jsonv.String(sid)
	e.mu.Lock()
	p.setSession(sid)
	p.Note = "session started"
	e.post(m, "orchestrator", "spawn", p.Name+" runs as pw code session "+id+" on "+jsString(p.Host))
	stillWorking := p.State == Working
	e.mu.Unlock()
	if !stillWorking {
		go pwcode.InterruptSession(context.Background(), local, id)
		return
	}
	servers := pwcode.WaitForMCP(ctx, local, id, 0)
	for _, s := range servers {
		if field(s, "name") != "task" {
			continue
		}
		if c, _ := s.Get("connected"); !jsonv.Truthy(c) {
			why := truthyStr(jsonv.Path(s, "err"))
			if why == "" {
				why = "no reason given"
			}
			e.mu.Lock()
			e.post(m, "orchestrator", "status", p.Name+": the board did not connect ("+why+"); the agent works without it")
			e.mu.Unlock()
		}
		break
	}
	if _, err := pwcode.SendMessage(ctx, local, id, prompt); err != nil {
		fail(err)
		return
	}
	e.mu.Lock()
	e.watchSession(m, p)
	e.mu.Unlock()
}

// watchSession follows one session agent: new history to the live log,
// approval requests to input-required, and the end of the turn to the
// completion path. e.mu is held.
func (e *Engine) watchSession(m *Task, p *Agent) {
	workdir := filepath.Join(e.taskDir(m.ID), "work", p.Name)
	os.MkdirAll(workdir, 0o755)
	live := filepath.Join(workdir, "live.log")
	started := time.Now()
	shown, misses := 0, 0
	active := false
	local := e.cfg.Daemon.Local()
	e.watch(m, p.Name, e.cfg.SessionPoll, func(ctx context.Context) bool {
		e.mu.Lock()
		if !alive(p) {
			e.clearPoller(m, p.Name)
			e.mu.Unlock()
			return false
		}
		id := jsonv.String(p.SessionID)
		e.mu.Unlock()
		d, err := pwcode.GetSession(ctx, local, id)
		if err != nil {
			misses++
			if misses >= 15 {
				e.mu.Lock()
				e.failAgent(m, p, "lost the pw code session: "+err.Error())
				e.mu.Unlock()
				return false
			}
			return true
		}
		misses = 0
		var hist []*jsonv.Object
		for _, h := range jsonv.Arr(jsonv.Path(d, "history")) {
			o := jsonv.Obj(h)
			if o == nil {
				o = jsonv.NewObject()
			}
			hist = append(hist, o)
		}
		if len(hist) < shown {
			shown = 0
			os.WriteFile(live, nil, 0o644)
		}
		if len(hist) > shown {
			var lines []string
			for _, h := range hist[shown:] {
				if l, ok := historyLine(h); ok {
					lines = append(lines, l)
				}
			}
			if len(lines) > 0 {
				if f, err := os.OpenFile(live, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
					f.WriteString(strings.Join(lines, "\n") + "\n")
					f.Close()
				}
			}
			shown = len(hist)
		}
		status := jsonv.Path(d, "status")
		if status != "idle" || len(hist) > 0 {
			active = true
		}
		e.mu.Lock()
		if !alive(p) {
			e.mu.Unlock()
			return false
		}
		p.UpdatedAt = now()
		pending := jsonv.Arr(jsonv.Path(d, "approvals"))
		if len(pending) > 0 {
			known := map[string]bool{}
			for _, a := range p.Approvals {
				known[jsonv.String(a.ID)] = true
			}
			list := make([]Approval, 0, len(pending))
			for _, x := range pending {
				a := jsonv.Obj(x)
				list = append(list, Approval{ID: jsonv.Path(a, "id"), Kind: jsonv.Path(a, "kind"), Text: describeApproval(a)})
			}
			p.setApprovals(list)
			p.State = InputRequired
			p.Note = "waiting for approval"
			for _, x := range pending {
				a := jsonv.Obj(x)
				if !known[jsonv.String(jsonv.Path(a, "id"))] {
					e.post(m, p.Name, "input-required", "needs approval: "+describeApproval(a))
				}
			}
			e.mu.Unlock()
			return true
		}
		if p.State == InputRequired {
			p.State = Working
			p.setApprovals(nil)
			p.Note = "working"
		}
		var lastTool *jsonv.Object
		for i := len(hist) - 1; i >= 0; i-- {
			if field(hist[i], "kind") == "tool_call" {
				lastTool = hist[i]
				break
			}
		}
		if status == "running" && lastTool != nil && (p.Note == "session started" || strings.HasPrefix(p.Note, "calling ")) {
			if tn, has := lastTool.Get("toolName"); has {
				p.Note = "calling " + jsString(tn)
			} else {
				p.Note = "calling undefined"
			}
		}
		if status == "idle" {
			lastUser := -1
			for i, h := range hist {
				if field(h, "kind") == "user" {
					lastUser = i
				}
			}
			var reply *jsonv.Object
			for i := len(hist) - 1; i > lastUser; i-- {
				h := hist[i]
				fm, _ := h.Get("fromMain")
				if field(h, "kind") == "assistant" && fm != false && trimJS(field(h, "text")) != "" {
					reply = h
					break
				}
			}
			if reply != nil {
				e.clearPoller(m, p.Name)
				e.mu.Unlock()
				var used *Usage
				if st, err := pwcode.SessionState(context.WithoutCancel(ctx), local, id); err == nil {
					used = UsageOf(jsonv.NewObject("input_tokens", jsonv.Path(st, "inputTokens"), "output_tokens", jsonv.Path(st, "outputTokens")))
				}
				e.mu.Lock()
				defer e.mu.Unlock()
				if used != nil {
					p.setUsage(used)
				}
				if alive(p) {
					e.completeAgent(m, p, field(reply, "text"))
				}
				return false
			}
			if active || time.Since(started) > 10*time.Second {
				e.failAgent(m, p, "the session ended its turn without a reply; the model or the platform login on this host may need attention (see the session in pw code for its error)")
				e.mu.Unlock()
				return false
			}
		}
		if time.Since(started) > agentTimeout {
			go pwcode.InterruptSession(context.Background(), local, id)
			e.failAgent(m, p, "timed out after "+strconv.Itoa(int(math.Round(agentTimeout.Minutes())))+" minutes")
			e.mu.Unlock()
			return false
		}
		e.mu.Unlock()
		return true
	})
}

/* ---- operator actions ---- */

// AnswerApproval passes a person's answer to an agent's approval request
// to its session.
func (e *Engine) AnswerApproval(ctx context.Context, id, agent, approvalID string, allowed bool, denyMessage string) error {
	e.mu.Lock()
	m := e.tasks[id]
	var p *Agent
	if m != nil {
		p = m.node(agent)
	}
	if m == nil || p == nil {
		e.mu.Unlock()
		return errors.New("no such task or agent")
	}
	if p.sessionID() == "" || p.State != InputRequired {
		e.mu.Unlock()
		return errors.New("this agent is not waiting for an approval")
	}
	found := false
	for _, a := range p.Approvals {
		found = found || jsonv.String(a.ID) == approvalID
	}
	if !found {
		e.mu.Unlock()
		return errors.New("no such approval request")
	}
	sid := p.sessionID()
	e.mu.Unlock()
	answer := jsonv.NewObject("allowed", allowed)
	if !allowed {
		if denyMessage == "" {
			denyMessage = "Denied by the operator in the Studio."
		}
		answer.Set("denyMessage", denyMessage)
	}
	if _, err := pwcode.AnswerApproval(ctx, e.cfg.Daemon.Local(), sid, approvalID, answer); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	rest := []Approval{}
	for _, a := range p.Approvals {
		if jsonv.String(a.ID) != approvalID {
			rest = append(rest, a)
		}
	}
	p.setApprovals(rest)
	if len(rest) == 0 {
		p.setApprovals(nil)
		p.State = Working
		if allowed {
			p.Note = "approved; working"
		} else {
			p.Note = "denied; working"
		}
	}
	verb := "denied"
	if allowed {
		verb = "approved"
	}
	e.post(m, "orchestrator", "status", agent+": request "+verb+" by the operator")
	return nil
}

// Steer delivers a person's direction to a working session agent.
func (e *Engine) Steer(ctx context.Context, id, agent, text string) (bool, error) {
	e.mu.Lock()
	m := e.tasks[id]
	var p *Agent
	if m != nil {
		p = m.node(agent)
	}
	if m == nil || p == nil {
		e.mu.Unlock()
		return false, errors.New("no such task or agent")
	}
	if p.sessionID() == "" || !alive(p) {
		e.mu.Unlock()
		return false, errors.New("this agent is not a working session")
	}
	clean := slice(trimJS(text), 4000)
	if clean == "" {
		e.mu.Unlock()
		return false, errors.New("nothing to send")
	}
	sid := p.sessionID()
	e.mu.Unlock()
	r, err := pwcode.SteerSession(ctx, e.cfg.Daemon.Local(), sid, clean)
	if err != nil {
		return false, err
	}
	e.mu.Lock()
	e.post(m, "operator", "direction", "to "+agent+": "+clean)
	e.mu.Unlock()
	d, _ := r.Get("delivered")
	return d != false, nil
}

// ResolveRuntime is resolveRuntime: which way a new task's agents run.
func (e *Engine) ResolveRuntime(requested any, execution string) string {
	if execution == "campaign" {
		return "runner"
	}
	switch e.cfg.AgentExecution() {
	case "runner":
		return "runner"
	case "sessions":
		return "session"
	}
	if requested == "runner" {
		return "runner"
	}
	return "session"
}

// AgentOutput is the last 16 KiB of an agent's live log; ok is false for
// an unknown task or agent.
func (e *Engine) AgentOutput(id, agent string) (string, bool) {
	e.mu.Lock()
	m := e.tasks[id]
	ok := m != nil && m.node(agent) != nil
	e.mu.Unlock()
	if !ok {
		return "", false
	}
	f, err := os.Open(filepath.Join(e.taskDir(id), "work", agent, "live.log"))
	if err != nil {
		return "", true
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", true
	}
	want := st.Size()
	if want > 16*1024 {
		want = 16 * 1024
	}
	buf := make([]byte, want)
	n, _ := f.ReadAt(buf, st.Size()-want)
	return strings.ToValidUTF8(string(buf[:n]), "�"), true
}

// AgentSpec is one initial agent of a new task.
type AgentSpec struct{ Persona, Objective string }

// CreateOptions are createTask's options.
type CreateOptions struct {
	Objective, Model string
	Agents           []AgentSpec
	// MaxAgents and MaxDepth are the request's values as given (nil: absent).
	MaxAgents, MaxDepth any
	Execution           string
	Resource            any
	Runtime             any
}

// Create is createTask.
func (e *Engine) Create(o CreateOptions) (*Task, error) {
	if o.Execution == "campaign" && !jsonv.Truthy(o.Resource) {
		return nil, errors.New("campaign execution needs a resource (a connected system to run the agents on)")
	}
	maxAgents := jsNumber(o.MaxAgents)
	if maxAgents == 0 || math.IsNaN(maxAgents) {
		maxAgents = 10
	}
	maxAgents = math.Min(math.Max(maxAgents, 1), maxAgentsCap)
	depth := math.NaN()
	if o.MaxDepth != nil {
		depth = jsNumber(o.MaxDepth)
	}
	if math.IsNaN(depth) || math.IsInf(depth, 0) {
		depth = 2
	}
	depth = math.Min(math.Max(depth, 0), maxDepthCap)
	execution := "local"
	if o.Execution == "campaign" {
		execution = "campaign"
	}
	tok := make([]byte, 24)
	rand.Read(tok)
	var resource any
	if o.Resource != nil {
		resource = o.Resource
	}
	m := &Task{
		ID:        time.Now().UTC().Format("2006-01-02") + "-" + slugN(o.Objective, 40, "task") + "-" + rand36(4),
		Objective: slice(o.Objective, 2000), Model: o.Model, CreatedAt: now(), State: Working,
		MaxAgents: maxAgents, MaxDepth: depth, Execution: execution, Resource: resource,
		Runtime: e.ResolveRuntime(o.Runtime, execution),
		Token:   hex.EncodeToString(tok), Shared: []*SharedTask{},
		byName: map[string]*Agent{}, procs: map[string]*runnerProc{}, pollers: map[string]context.CancelFunc{},
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.put(m)
	how := "agents run as one-shot pw code runs"
	if m.Runtime == "session" {
		how = "agents run as pw code sessions"
	}
	e.post(m, "orchestrator", "task", "Objective: "+m.Objective+" (up to "+num(m.MaxAgents)+" agents, depth "+num(m.MaxDepth)+"; "+how+")")
	agents := o.Agents
	if lim := int(m.MaxAgents); len(agents) > lim {
		agents = agents[:lim]
	}
	for _, a := range agents {
		if _, err := e.spawnAgent(m, a.Persona, slice(a.Objective, 2000), nil, 0); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (e *Engine) put(m *Task) {
	if _, ok := e.tasks[m.ID]; !ok {
		e.order = append(e.order, m.ID)
	}
	e.tasks[m.ID] = m
}

// Stop is stopTask: the task canceled, its runners killed, its watchers
// stopped, and its sessions and platform runs interrupted.
func (e *Engine) Stop(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.tasks[id]
	if m == nil {
		return false
	}
	m.State = Canceled
	for name, proc := range m.procs {
		if p := m.node(name); p != nil {
			p.State = Canceled
			p.Note = "stopped by user"
		}
		proc.canceled = true
		if proc.cmd != nil && proc.cmd.Process != nil {
			proc.cmd.Process.Signal(syscall.SIGTERM)
		}
	}
	m.procs = map[string]*runnerProc{}
	local := e.cfg.Daemon.Local()
	for name, cancel := range m.pollers {
		cancel()
		p := m.node(name)
		if p != nil && alive(p) {
			p.State = Canceled
			p.Note = "stopped by user"
			p.setApprovals(nil)
			if sid := p.sessionID(); sid != "" {
				go pwcode.InterruptSession(context.Background(), local, sid)
			}
			if slug := p.runSlug(); slug != "" {
				go e.pwRun(context.Background(), []string{"workflows", "runs", "cancel", slug}, 60*time.Second)
			}
		}
	}
	m.pollers = map[string]context.CancelFunc{}
	e.post(m, "orchestrator", "task", "Stopped by user.")
	e.notifyFinished(m)
	return true
}

// Summary is taskSummary for the task id, or nil.
func (e *Engine) Summary(id string) *jsonv.Object {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.tasks[id]
	if m == nil {
		return nil
	}
	return e.summary(m)
}

// List is listTasks.
func (e *Engine) List() []any {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []any{}
	for _, id := range e.order {
		m := e.tasks[id]
		running, waiting := 0, 0
		for _, a := range m.nodes {
			if alive(a) {
				running++
			}
			if a.State == InputRequired {
				waiting++
			}
		}
		out = append(out, jsonv.NewObject("id", m.ID, "objective", m.Objective, "state", m.State, "createdAt", m.CreatedAt,
			"usage", SumUsage(m.nodes).value(), "execution", m.Execution, "resource", m.Resource, "runtime", m.Runtime,
			"agents", len(m.nodes), "running", running, "waiting", waiting))
	}
	return out
}

// Snapshot is a copy of a task's state for callers outside the engine.
type Snapshot struct {
	ID, State, Token, Runtime, Execution string
	Agents                               []Agent
	Board                                []BoardMsg
	Procs, Pollers                       int
}

// Get is a snapshot of the task id, or nil.
func (e *Engine) Get(id string) *Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.tasks[id]
	if m == nil {
		return nil
	}
	s := &Snapshot{ID: m.ID, State: m.State, Token: m.Token, Runtime: m.Runtime, Execution: m.Execution,
		Board: append([]BoardMsg(nil), m.board...), Procs: len(m.procs), Pollers: len(m.pollers)}
	for _, a := range m.nodes {
		c := *a
		c.order = append([]string(nil), a.order...)
		c.Approvals = append([]Approval(nil), a.Approvals...)
		if a.Approvals == nil {
			c.Approvals = nil
		}
		s.Agents = append(s.Agents, c)
	}
	return s
}

// JSNumber is Number(v) for a decoded JSON value.
func jsNumber(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		t := trimJS(x)
		if t == "" {
			return 0
		}
		n, err := strconv.ParseFloat(t, 64)
		if err != nil || strings.ContainsAny(t, "_xXpP") || strings.Contains(strings.ToLower(t), "inf") && t != "Infinity" && t != "-Infinity" && t != "+Infinity" || strings.EqualFold(t, "nan") {
			return math.NaN()
		}
		return n
	case []any:
		if len(x) == 0 {
			return 0
		}
		if len(x) == 1 {
			return jsNumber(jsonv.String(x[0]))
		}
	}
	return math.NaN()
}
