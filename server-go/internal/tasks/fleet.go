package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// Standing agents: the long-lived residents of the Agents tab. A standing
// agent has a goal, a persona, a budget and triggers, and lives in ticks;
// a tick is one bounded delegated task. Its memory is its journal on disk
// (INDEX_BASE/fleet/<id>/journal.jsonl), its record agent.json beside it.
//
// A record is kept as the JavaScript object Node keeps (a jsonv.Object
// read from and written to agent.json), so fields keep their order and
// values their types exactly as Node writes them.

var cadence = map[string]float64{"15m": 15 * 60_000, "30m": 30 * 60_000, "1h": 3_600_000, "2h": 7_200_000, "6h": 21_600_000, "12h": 43_200_000, "1d": 86_400_000, "manual": 0}

const journalTail = 12

type fleetTemplate struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Persona   string `json:"persona"`
	Every     string `json:"every"`
	OnRunEnd  bool   `json:"onRunEnd"`
	WatchPath any    `json:"watchPath"`
	Execution string `json:"execution"`
	Goal      string `json:"goal"`
}

// RunEnded is a platform run that ended (runs.ts's notifyRunEnded event).
type RunEnded struct {
	Slug, Workflow string
	Resource       string // "" for none
	State          string
}

// RunEndedSource is a run watcher that tells listeners when runs end; the
// fleet registers NotifyRunEnded on it (internal/workflows' OnRunEnded).
type RunEndedSource interface {
	OnRunEnded(fn func(RunEnded))
}

// Fleet is one process's standing agents.
type Fleet struct {
	e      *Engine
	dir    string
	kbRoot string
	log    func(string)

	mu     sync.Mutex
	loaded bool
	agents map[string]*jsonv.Object
	order  []string
	// stopScheduler ends a scheduler started without an App (tests).
	stopScheduler context.CancelFunc
}

// StopScheduler ends a scheduler Start ran without an App.
func (f *Fleet) StopScheduler() {
	f.mu.Lock()
	stop := f.stopScheduler
	f.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// NewFleet is the fleet over e; it listens for e's finished tasks.
func NewFleet(e *Engine, log func(string)) *Fleet {
	if log == nil {
		log = func(string) {}
	}
	f := &Fleet{e: e, dir: filepath.Join(e.cfg.IndexBase, "fleet"), kbRoot: e.cfg.KBRoot, log: log, agents: map[string]*jsonv.Object{}}
	e.OnFinished(f.onFinished)
	return f
}

/* ---- records ---- */

func gs(a *jsonv.Object, k string) string { v, _ := a.Get(k); s, _ := v.(string); return s }
func gv(a *jsonv.Object, k string) any    { v, _ := a.Get(k); return v }
func gn(a *jsonv.Object, k string) float64 {
	v, _ := a.Get(k)
	return jsNumber(v)
}

func (f *Fleet) load() {
	if f.loaded {
		return
	}
	f.loaded = true
	ents, err := os.ReadDir(f.dir)
	if err != nil {
		return
	}
	for _, d := range ents {
		b, err := os.ReadFile(filepath.Join(f.dir, d.Name(), "agent.json"))
		if err != nil {
			continue
		}
		v, err := jsonv.Decode(b)
		if err != nil {
			continue
		}
		a := jsonv.Obj(v)
		if a == nil {
			continue
		}
		f.set(jsonv.String(gv(a, "id")), a)
	}
}

func (f *Fleet) set(id string, a *jsonv.Object) {
	if _, ok := f.agents[id]; !ok {
		f.order = append(f.order, id)
	}
	f.agents[id] = a
}

func (f *Fleet) dirOf(id string) string { return filepath.Join(f.dir, id) }

// persist writes agent.json as JSON.stringify(a, null, 1).
func (f *Fleet) persist(a *jsonv.Object) {
	a.Set("updatedAt", now())
	id := jsonv.String(gv(a, "id"))
	if err := os.MkdirAll(f.dirOf(id), 0o755); err != nil {
		return
	}
	b, err := jsonv.Marshal(a)
	if err != nil {
		return
	}
	var out bytes.Buffer
	if json.Indent(&out, b, "", " ") != nil {
		return
	}
	os.WriteFile(filepath.Join(f.dirOf(id), "agent.json"), out.Bytes(), 0o644)
}

func (f *Fleet) journalAppend(id, kind, text string, taskID ...string) {
	e := jsonv.NewObject("at", now(), "kind", kind, "text", text)
	if len(taskID) > 0 {
		e.Set("taskId", taskID[0])
	}
	if os.MkdirAll(f.dirOf(id), 0o755) != nil {
		return
	}
	b, _ := jsonv.Marshal(e)
	if fh, err := os.OpenFile(filepath.Join(f.dirOf(id), "journal.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		fh.Write(append(b, '\n'))
		fh.Close()
	}
}

// JournalTail is journalTail: the last n entries that parse.
func (f *Fleet) JournalTail(id string, n int) []any {
	b, err := os.ReadFile(filepath.Join(f.dirOf(id), "journal.jsonl"))
	if err != nil {
		return []any{}
	}
	lines := strings.Split(trimJS(string(b)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := []any{}
	for _, l := range lines {
		if v, err := jsonv.DecodeString(l); err == nil && jsonv.Truthy(v) {
			out = append(out, v)
		}
	}
	return out
}

// List is listAgents: newest first.
func (f *Fleet) List() []*jsonv.Object {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	out := make([]*jsonv.Object, 0, len(f.order))
	for _, id := range f.order {
		out = append(out, f.agents[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return gs(out[i], "createdAt") > gs(out[j], "createdAt") })
	return out
}

// Get is getAgent.
func (f *Fleet) Get(id string) *jsonv.Object {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	return f.agents[id]
}

// Create is createAgent: input is the request body as given, owner the
// viewer's id.
func (f *Fleet) Create(input *jsonv.Object, owner any) (*jsonv.Object, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	in := func(k string) any { v, _ := input.Get(k); return v }
	nameV, ok := in("name").(string)
	if !ok {
		return nil, errors.New("Cannot read properties of undefined (reading 'trim')")
	}
	name := slice(trimJS(nameV), 60)
	if name == "" {
		return nil, errors.New("an agent needs a name and a goal")
	}
	goalV, ok := in("goal").(string)
	if !ok {
		return nil, errors.New("Cannot read properties of undefined (reading 'trim')")
	}
	if trimJS(goalV) == "" {
		return nil, errors.New("an agent needs a name and a goal")
	}
	if in("execution") == "campaign" && !jsonv.Truthy(in("resource")) {
		return nil, errors.New("campaign execution needs a resource")
	}
	every := "1h"
	if ev := in("every"); jsonv.Truthy(ev) {
		if _, ok := cadence[jsonv.String(ev)]; ok {
			every = jsonv.String(ev)
		}
	}
	var watchPath any
	switch w := in("watchPath").(type) {
	case nil:
	case string:
		if t := trimJS(w); t != "" {
			watchPath = t
		}
	default:
		return nil, errors.New("input.watchPath?.trim is not a function")
	}
	id := slugN(name, 32, "agent") + "-" + rand36(4)
	t := now()
	persona := in("persona")
	if !jsonv.Truthy(persona) {
		persona = nil
	}
	model, hasModel := input.Get("model")
	if !hasModel {
		model = jsonv.Undefined
	}
	execution := "local"
	if in("execution") == "campaign" {
		execution = "campaign"
	}
	orNull := func(k string) any { return in(k) }
	wp, _ := watchPath.(string)
	a := jsonv.NewObject(
		"id", id, "name", name, "goal", slice(trimJS(goalV), 4000), "persona", persona, "model", model,
		"execution", execution, "resource", orNull("resource"),
		"triggers", jsonv.NewObject("every", every, "onRunEnd", jsonv.Truthy(in("onRunEnd")), "watchPath", watchPath),
		"budget", jsonv.NewObject("maxTicks", orNull("maxTicks"), "maxTokens", orNull("maxTokens")),
		"state", "idle", "note", "created; first tick is due now",
		"createdAt", t, "updatedAt", t, "lastTickAt", nil, "nextTickAt", t,
		"ticks", 0.0, "tokens", 0.0, "currentTaskId", nil, "inbox", []any{}, "lastSeenMtime", f.newestMtime(wp),
		"owner", owner, "conversationId", orNull("conversationId"),
	)
	f.set(id, a)
	f.persist(a)
	f.journalAppend(id, "created", "Goal: "+gs(a, "goal"))
	return a, nil
}

func (f *Fleet) newestMtime(rel string) float64 {
	if rel == "" {
		return 0
	}
	newest := 0.0
	var walk func(d string, depth int)
	walk = func(d string, depth int) {
		if depth > 4 {
			return
		}
		ents, err := os.ReadDir(d)
		if err != nil {
			return // not there yet, or vanished
		}
		for _, ent := range ents {
			p := filepath.Join(d, ent.Name())
			if ent.IsDir() {
				walk(p, depth+1)
			} else if st, err := os.Stat(p); err == nil {
				newest = math.Max(newest, float64(st.ModTime().UnixNano())/1e6)
			}
		}
	}
	walk(filepath.Join(f.kbRoot, rel), 0)
	return newest
}

func (f *Fleet) setState(a *jsonv.Object, state, note string) {
	a.Set("state", state)
	a.Set("note", note)
	f.journalAppend(gs(a, "id"), "state", state+": "+note)
	f.persist(a)
}

// Pause is pauseAgent.
func (f *Fleet) Pause(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	a := f.agents[id]
	if a == nil || gs(a, "state") == "retired" {
		return false
	}
	f.setState(a, "paused", "paused by user")
	return true
}

// Resume is resumeAgent.
func (f *Fleet) Resume(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	a := f.agents[id]
	if a == nil || gs(a, "state") == "retired" {
		return false
	}
	a.Set("nextTickAt", now())
	f.setState(a, "idle", "resumed; next tick due now")
	return true
}

// Retire is retireAgent.
func (f *Fleet) Retire(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	a := f.agents[id]
	if a == nil {
		return false
	}
	f.setState(a, "retired", "retired by user")
	return true
}

// Message is messageAgent: a human message is an event, answered on the
// next tick, which is now.
func (f *Fleet) Message(id, text string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	a := f.agents[id]
	if a == nil || gs(a, "state") == "retired" {
		return false
	}
	f.journalAppend(id, "message", text)
	a.Set("inbox", append(jsonv.Arr(gv(a, "inbox")), "Message from the user: "+text))
	if s := gs(a, "state"); s == "input-required" || s == "paused" {
		a.Set("state", "idle")
	}
	a.Set("nextTickAt", now())
	f.persist(a)
	return true
}

func budgetLeft(a *jsonv.Object) string {
	b := jsonv.Obj(gv(a, "budget"))
	if mt, _ := b.Get("maxTicks"); mt != nil && gn(a, "ticks") >= jsNumber(mt) {
		return "tick budget of " + jsonv.String(mt) + " used"
	}
	if mt, _ := b.Get("maxTokens"); mt != nil && gn(a, "tokens") >= jsNumber(mt) {
		return "token budget of " + jsonv.String(mt) + " used"
	}
	return ""
}

func (f *Fleet) tickObjective(a *jsonv.Object, events []string) string {
	var tail []string
	for _, x := range f.JournalTail(gs(a, "id"), journalTail) {
		e := jsonv.Obj(x)
		if gs(e, "kind") == "tick" {
			continue
		}
		tail = append(tail, "- ["+slice(jsonv.String(gv(e, "at")), 16)+" "+jsonv.String(gv(e, "kind"))+"] "+slice(jsonv.String(gv(e, "text")), 400))
	}
	woke := "What woke you this tick: the schedule."
	if len(events) > 0 {
		ev := make([]string, len(events))
		for i, e := range events {
			ev[i] = "- " + e
		}
		woke = "What woke you this tick:\n" + strings.Join(ev, "\n")
	}
	journal := "Your journal is empty; this is your first tick."
	if len(tail) > 0 {
		journal = "Your journal (most recent last):\n" + strings.Join(tail, "\n")
	}
	return strings.Join([]string{
		`You are the standing agent "` + gs(a, "name") + `". Your goal, which persists across ticks:`,
		jsonv.String(gv(a, "goal")),
		"",
		woke,
		"",
		journal,
		"",
		"Do one bounded round of work toward the goal now. Use the tools you have to look, act, and check. Then end with a status a person can read in a minute: what you did, what changed, what is next, and, if you need a decision from a person, one line starting with NEEDS DECISION: followed by the question. Do not repeat the journal back.",
	}, "\n")
}

// Tick is tickAgent: one tick now; the task id, or "" when the agent
// cannot tick.
func (f *Fleet) Tick(id, reason string, force bool) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	return f.tick(id, reason, force)
}

func (f *Fleet) tick(id, reason string, force bool) string {
	a := f.agents[id]
	if a == nil {
		return ""
	}
	if s := gs(a, "state"); s == "retired" || s == "working" || (!force && s == "paused") {
		return ""
	}
	if exhausted := budgetLeft(a); exhausted != "" && !force {
		f.setState(a, "paused", exhausted)
		return ""
	}
	var events []string
	for _, x := range append(jsonv.Arr(gv(a, "inbox")), reason) {
		if jsonv.Truthy(x) {
			events = append(events, jsonv.String(x))
		}
	}
	a.Set("inbox", []any{})
	persona := ""
	if p := gv(a, "persona"); p != nil {
		persona = jsonv.String(p)
	}
	execution := gs(a, "execution")
	m, err := f.e.Create(CreateOptions{
		Objective: f.tickObjective(a, events), Model: jsonv.String(gv(a, "model")),
		Agents:    []AgentSpec{{Persona: persona, Objective: "Tick for standing agent " + gs(a, "name") + ": " + slice(gs(a, "goal"), 200)}},
		MaxAgents: 1.0, MaxDepth: 0.0, Execution: execution, Resource: gv(a, "resource"),
	})
	if err != nil {
		f.journalAppend(id, "error", slice(err.Error(), 400))
		f.setState(a, "idle", "tick failed to start: "+slice(err.Error(), 120))
		f.scheduleNext(a)
		return ""
	}
	t := now()
	a.Set("currentTaskId", m.ID)
	a.Set("lastTickAt", t)
	a.Set("ticks", gn(a, "ticks")+1)
	a.Set("state", "working")
	a.Set("note", "tick "+num(gn(a, "ticks"))+" running")
	f.journalAppendAt(id, t, "tick", slice(strings.Join(events, " | "), 400), m.ID)
	f.persist(a)
	return m.ID
}

func (f *Fleet) journalAppendAt(id, at, kind, text, taskID string) {
	e := jsonv.NewObject("at", at, "kind", kind, "text", text, "taskId", taskID)
	if os.MkdirAll(f.dirOf(id), 0o755) != nil {
		return
	}
	b, _ := jsonv.Marshal(e)
	if fh, err := os.OpenFile(filepath.Join(f.dirOf(id), "journal.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		fh.Write(append(b, '\n'))
		fh.Close()
	}
}

func (f *Fleet) scheduleNext(a *jsonv.Object) {
	tr := jsonv.Obj(gv(a, "triggers"))
	ms := cadence[jsonv.String(gv(tr, "every"))]
	if ms > 0 {
		a.Set("nextTickAt", platform.ISO(time.Now().Add(time.Duration(ms)*time.Millisecond)))
	} else {
		a.Set("nextTickAt", nil)
	}
}

func (f *Fleet) onFinished(m Finished) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	for _, id := range f.order {
		a := f.agents[id]
		if gv(a, "currentTaskId") != m.ID {
			continue
		}
		a.Set("currentTaskId", nil)
		result := ""
		if m.First != nil {
			result = m.First.Note
			if rp := m.First.resultPath(); rp != "" {
				if b, err := os.ReadFile(filepath.Join(f.kbRoot, rp)); err == nil {
					result = string(b)
				}
			}
		}
		if m.Usage != nil && m.Usage.Total != 0 {
			a.Set("tokens", gn(a, "tokens")+m.Usage.Total)
		}
		f.journalAppendAt(id, now(), "result", slice(result, 4000), m.ID)
		decision := ""
		if d := decisionR.FindStringSubmatch(result); d != nil {
			decision = trimJS(d[1])
		}
		f.scheduleNext(a)
		switch {
		case m.State == Canceled:
			f.setState(a, "paused", "last tick was stopped")
		case decision != "":
			f.setState(a, "input-required", slice(decision, 300))
		case m.First != nil && m.First.State == Failed:
			f.setState(a, "idle", "last tick failed: "+slice(m.First.Note, 160))
		default:
			if exhausted := budgetLeft(a); exhausted != "" {
				f.setState(a, "paused", exhausted)
			} else if next, _ := gv(a, "nextTickAt").(string); next != "" {
				f.setState(a, "idle", "next tick "+slice(next, 16))
			} else {
				f.setState(a, "idle", "waiting for an event or a message")
			}
		}
	}
}

// NotifyRunEnded wakes the agents that asked to hear about ended runs.
func (f *Fleet) NotifyRunEnded(ev RunEnded) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	text := "Run " + ev.Slug + " of " + ev.Workflow
	if ev.Resource != "" {
		text += " on " + ev.Resource
	}
	text += " ended: " + ev.State
	for _, id := range f.order {
		a := f.agents[id]
		tr := jsonv.Obj(gv(a, "triggers"))
		if gs(a, "state") == "retired" || !jsonv.Truthy(gv(tr, "onRunEnd")) {
			continue
		}
		f.journalAppend(id, "event", text)
		switch gs(a, "state") {
		case "working":
			a.Set("inbox", append(jsonv.Arr(gv(a, "inbox")), text))
			f.persist(a)
			continue
		case "paused":
			continue
		}
		f.tick(id, text, false)
	}
}

// Pass is one scheduler pass: watched paths and due ticks.
func (f *Fleet) Pass() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	nowMs := float64(time.Now().UnixMilli())
	for _, id := range append([]string(nil), f.order...) {
		a := f.agents[id]
		if gs(a, "state") != "idle" {
			continue
		}
		tr := jsonv.Obj(gv(a, "triggers"))
		if wp := gs(tr, "watchPath"); wp != "" {
			if newest := f.newestMtime(wp); newest > gn(a, "lastSeenMtime") {
				a.Set("lastSeenMtime", newest)
				f.persist(a)
				f.journalAppend(id, "event", "New or changed files under "+wp)
				f.tick(id, "New or changed files under "+wp, false)
				continue
			}
		}
		if next, _ := gv(a, "nextTickAt").(string); next != "" {
			if t, ok := platform.ParseISO(next); ok && float64(t.UnixMilli()) <= nowMs {
				f.tick(id, "the schedule", false)
			}
		}
	}
}

// Start is startFleet: a tick that was running when the last process
// stopped becomes idle, and the scheduler runs every STUDIO_FLEET_TICK_MS.
func (f *Fleet) Start(a *app.App, every time.Duration) int {
	f.mu.Lock()
	f.load()
	n := 0
	for _, id := range f.order {
		x := f.agents[id]
		if gs(x, "state") == "working" {
			x.Set("currentTaskId", nil)
			x.Set("state", "idle")
			x.Set("note", "a tick was interrupted by a restart; waiting for the next trigger")
			if gv(x, "nextTickAt") == nil {
				f.scheduleNext(x)
			}
			f.persist(x)
		}
		if gs(x, "state") != "retired" {
			n++
		}
	}
	f.mu.Unlock()
	run := func(ctx context.Context) {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				f.Pass()
			}
		}
	}
	if a != nil {
		a.Go("fleet scheduler", run)
	} else {
		ctx, cancel := context.WithCancel(context.Background())
		f.mu.Lock()
		f.stopScheduler = cancel
		f.mu.Unlock()
		go run(ctx)
	}
	if n > 0 {
		f.log("fleet: " + num(float64(n)) + " standing agent(s) loaded")
	}
	return n
}

/* ---- routes ---- */

// Routes registers the fleet's routes.
func (f *Fleet) Routes(mux *http.ServeMux, a *app.App, p *platform.Platform, delegation func() bool) {
	h := func(fn func(w http.ResponseWriter, r *http.Request) (any, error)) http.HandlerFunc {
		return platform.Handler(a.Log, fn)
	}
	// The gate's 403 is a plain Error with a statusCode, which Node's error
	// handler answers as a 500, as here.
	gate := func() error {
		if !delegation() {
			return errors.New("delegation is disabled on this deployment")
		}
		return nil
	}
	mux.HandleFunc("GET /api/fleet/templates", h(func(w http.ResponseWriter, r *http.Request) (any, error) {
		return jsonv.NewObject("templates", fleetTemplates), nil
	}))
	mux.HandleFunc("GET /api/fleet", h(func(w http.ResponseWriter, r *http.Request) (any, error) {
		if err := gate(); err != nil {
			return nil, err
		}
		return jsonv.NewObject("agents", f.List()), nil
	}))
	mux.HandleFunc("POST /api/fleet", h(func(w http.ResponseWriter, r *http.Request) (any, error) {
		if err := gate(); err != nil {
			return nil, err
		}
		b, err := platform.BodyObject(r)
		if err != nil {
			return nil, err
		}
		var owner any
		if v, err := p.ViewerOf(r); err == nil && v.UserID() != "" {
			owner = v.UserID()
		}
		ag, err := f.Create(b, owner)
		if err != nil {
			return nil, err
		}
		return jsonv.NewObject("agent", ag), nil
	}))
	mux.HandleFunc("GET /api/fleet/{id}", h(func(w http.ResponseWriter, r *http.Request) (any, error) {
		if err := gate(); err != nil {
			return nil, err
		}
		ag := f.Get(r.PathValue("id"))
		if ag == nil {
			return sendError(w, 404, "no such agent")
		}
		var current any
		if tid, _ := gv(ag, "currentTaskId").(string); tid != "" {
			if s := f.e.Get(tid); s != nil {
				current = jsonv.NewObject("id", s.ID, "state", s.State)
			}
		}
		return jsonv.NewObject("agent", ag, "journal", f.JournalTail(gs(ag, "id"), 60), "currentTask", current), nil
	}))
	act := func(name string, fn func(id string, body *jsonv.Object) any) {
		mux.HandleFunc("POST /api/fleet/{id}/"+name, h(func(w http.ResponseWriter, r *http.Request) (any, error) {
			if err := gate(); err != nil {
				return nil, err
			}
			b, err := platform.BodyObject(r)
			if err != nil {
				return nil, err
			}
			res := fn(r.PathValue("id"), b)
			if res == false || res == nil {
				return sendError(w, 409, "cannot "+name+" this agent in its current state")
			}
			return jsonv.NewObject("ok", true, "result", res), nil
		}))
	}
	act("tick", func(id string, _ *jsonv.Object) any {
		if t := f.Tick(id, "the user asked for a tick now", true); t != "" {
			return t
		}
		return nil
	})
	act("pause", func(id string, _ *jsonv.Object) any { return f.Pause(id) })
	act("resume", func(id string, _ *jsonv.Object) any { return f.Resume(id) })
	act("retire", func(id string, _ *jsonv.Object) any { return f.Retire(id) })
	act("message", func(id string, b *jsonv.Object) any {
		t, _ := b.Get("text")
		return f.Message(id, slice(jsonv.String(t), 2000))
	})
}
