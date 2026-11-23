// Package fleet keeps the standing agents of the Agents tab. A standing
// agent is a long-lived pw code session on this Studio's host, working in
// a library folder of its own (agents/fleet/<id>/) whose AGENTS.md
// carries its instructions and its goal. It wakes on a schedule, when a
// platform run ends, when files change under a watched folder, or when a
// person writes to it; each wake-up is a message to the session with a
// goal for that round, bounded by a number of turns. The session's
// history is its journal; the Studio keeps a short event log beside the
// record (INDEX_BASE/fleet/<id>/journal.jsonl) with each wake-up and what
// came of it. A question for a person is the session waiting for an
// approval, or a line starting NEEDS DECISION: in its status.
//
// Records written by the earlier engine (a tick was one delegated task)
// are taken over in place the first time they are read: goal, persona,
// triggers, budget and journal carry over, and what does not is written
// into the record's migrated note and its journal.
package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsmd"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// Cadence is how often an agent wakes on its own, by name.
var Cadence = map[string]time.Duration{"15m": 15 * time.Minute, "30m": 30 * time.Minute, "1h": time.Hour, "2h": 2 * time.Hour, "6h": 6 * time.Hour, "12h": 12 * time.Hour, "1d": 24 * time.Hour, "manual": 0}

// DefaultTurns bounds one wake-up when the agent sets no bound.
const DefaultTurns = 10

// Triggers are what wake an agent besides a person.
type Triggers struct {
	Every     string  `json:"every"`
	OnRunEnd  bool    `json:"onRunEnd"`
	WatchPath *string `json:"watchPath"`
}

// Budget bounds an agent: wake-ups, pw code tokens, and turns per
// wake-up. Nil is unbounded (turns: DefaultTurns).
type Budget struct {
	MaxTicks  *float64 `json:"maxTicks"`
	MaxTokens *float64 `json:"maxTokens"`
	MaxTurns  *float64 `json:"maxTurns,omitempty"`
}

// Agent is a standing agent's record (agent.json).
type Agent struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Goal     string   `json:"goal"`
	Persona  *string  `json:"persona"`
	Model    string   `json:"model"`
	Resource *string  `json:"resource"`
	Triggers Triggers `json:"triggers"`
	Budget   Budget   `json:"budget"`
	// State is idle, working, input-required, paused or retired.
	State         string   `json:"state"`
	Note          string   `json:"note"`
	CreatedAt     string   `json:"createdAt"`
	UpdatedAt     string   `json:"updatedAt"`
	LastTickAt    *string  `json:"lastTickAt"`
	NextTickAt    *string  `json:"nextTickAt"`
	Ticks         float64  `json:"ticks"`
	Tokens        float64  `json:"tokens"`
	Inbox         []string `json:"inbox"`
	LastSeenMtime float64  `json:"lastSeenMtime"`
	Owner         *string  `json:"owner"`
	// OwnerUser is the person the agent acts for, for the Studio's tools
	// in its session; nil on a Studio without sign-in.
	OwnerUser *auth.User `json:"ownerUser,omitempty"`
	// OwnerSeenAt is when that record was last brought up to date from
	// the person's own sign-in (made, or their visit to the Fleet page).
	OwnerSeenAt string `json:"ownerSeenAt,omitempty"`
	// SessionRights is the rights the current session's Studio token was
	// made with (rightsKey); a change starts a new session.
	SessionRights  string  `json:"sessionRights,omitempty"`
	ConversationID *string `json:"conversationId"`

	// Engine is "pwcode" for a record this package keeps.
	Engine string `json:"engine"`
	// Folder is the library folder the agent works in.
	Folder string `json:"folder"`
	// Workspace is where its session works on this host.
	Workspace string `json:"workspace,omitempty"`
	// Session is its pw code session; SessionAt when it was made, and
	// SessionTokens the tokens counted from it so far.
	Session       string  `json:"session,omitempty"`
	SessionAt     string  `json:"sessionAt,omitempty"`
	SessionTokens float64 `json:"sessionTokens,omitempty"`
	// Sessions are its earlier sessions, oldest first.
	Sessions []string `json:"sessions,omitempty"`
	// Starting is set while a wake-up starts its session, and cleared when
	// it is working or failed; one still set when the Studio reads the
	// record was cut off by a restart.
	Starting bool `json:"starting,omitempty"`
	// Migrated says what did not carry over from the earlier engine.
	Migrated string `json:"migrated,omitempty"`

	// Execution and CurrentTaskID are the earlier engine's; read so a
	// record can be taken over, never written again.
	Execution     string  `json:"execution,omitempty"`
	CurrentTaskID *string `json:"currentTaskId,omitempty"`
}

// Starter makes the session a standing agent works in: on this host's
// daemon, in workspace, acting for owner under grant (the chat module's
// StudioSession, which carries the Studio's tools), with allow, the
// pw code rules it may use without asking.
type Starter func(ctx context.Context, c pwcode.Client, owner *auth.User, grant, workspace, model, mode string, allow []string) (string, error)

// Fleet is this process's standing agents.
type Fleet struct {
	A      *app.App
	Daemon func() pwcode.Client
	// Start makes a session with the Studio's tools; when it is nil or
	// fails, a plain session is made.
	Start Starter
	// Renew is how old a session may get before a wake-up replaces it
	// (its token for the Studio's tools runs out).
	Renew time.Duration
	// Mode is the permission mode of the agents' sessions.
	Mode string
	// Refused says why no agent runs here now ("" when they may): on a
	// Studio of one, this computer's pw code needs ACTIVATE.
	Refused func() string

	dir    string
	mu     sync.Mutex
	loaded bool
	agents map[string]*Agent
	order  []string
	// busy marks agents whose session is being started or checked.
	busy map[string]bool
}

// New is the fleet kept under a's index folder.
func New(a *app.App, daemon func() pwcode.Client) *Fleet {
	return &Fleet{A: a, Daemon: daemon, Renew: 6 * 24 * time.Hour, Mode: "accept-edits",
		dir: filepath.Join(a.Cfg.IndexBase, "fleet"), agents: map[string]*Agent{}, busy: map[string]bool{}}
}

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

func sp(s string) *string { return &s }

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

/* ---- records ---- */

func (f *Fleet) dirOf(id string) string { return filepath.Join(f.dir, id) }

// An agent's record and journal hold its owner's sign-in record, its goal,
// the messages it was sent and what its rounds found, which can quote a
// private library: they are the Studio account's alone, files 0600 in
// folders 0700, as the delegation and agent instruction records are.
const (
	privateDir  = 0o700
	privateFile = 0o600
)

// closeUp makes records written before that private: the fleet folder,
// each agent's folder, and the files in it.
func (f *Fleet) closeUp() {
	os.Chmod(f.dir, privateDir)
	ents, err := os.ReadDir(f.dir)
	if err != nil {
		return
	}
	for _, d := range ents {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(f.dir, d.Name())
		os.Chmod(dir, privateDir)
		for _, n := range []string{"agent.json", "agent.json.part", "journal.jsonl"} {
			if st, err := os.Lstat(filepath.Join(dir, n)); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o077 != 0 {
				os.Chmod(filepath.Join(dir, n), privateFile)
			}
		}
	}
}

func (f *Fleet) load() {
	if f.loaded {
		return
	}
	f.loaded = true
	f.closeUp()
	ents, err := os.ReadDir(f.dir)
	if err != nil {
		return
	}
	for _, d := range ents {
		b, err := os.ReadFile(filepath.Join(f.dir, d.Name(), "agent.json"))
		if err != nil {
			continue
		}
		var a Agent
		if json.Unmarshal(b, &a) != nil || a.ID == "" {
			continue
		}
		if a.Engine != "pwcode" {
			f.takeOver(&a)
		}
		// A record from before owners' visits and sessions' rights were
		// kept: its owner counts as seen now, and its session as made with
		// the rights it acts with now, so nothing changes at the upgrade
		// until the first real check (a visit, or 7 days).
		if a.OwnerUser != nil && a.OwnerSeenAt == "" {
			a.OwnerSeenAt = now()
			if a.Session != "" && a.SessionRights == "" {
				a.SessionRights = rightsKey(actsAs(&a))
			}
			f.persist(&a)
		}
		if a.Starting {
			// The Studio stopped while this wake-up started: nothing is
			// working, and nothing would follow it.
			a.Starting = false
			f.scheduleNext(&a)
			f.setState(&a, "idle", "its wake-up was cut off when the Studio stopped; it wakes on its next trigger")
		}
		f.set(&a)
	}
}

func (f *Fleet) set(a *Agent) {
	if _, ok := f.agents[a.ID]; !ok {
		f.order = append(f.order, a.ID)
	}
	f.agents[a.ID] = a
}

func (f *Fleet) persist(a *Agent) {
	a.UpdatedAt = now()
	if os.MkdirAll(f.dirOf(a.ID), privateDir) != nil {
		return
	}
	if a.Inbox == nil {
		a.Inbox = []string{}
	}
	b, err := json.Marshal(a)
	if err != nil {
		return
	}
	var out bytes.Buffer
	if json.Indent(&out, b, "", " ") != nil {
		return
	}
	tmp := filepath.Join(f.dirOf(a.ID), "agent.json.part")
	if os.WriteFile(tmp, out.Bytes(), privateFile) == nil {
		os.Rename(tmp, filepath.Join(f.dirOf(a.ID), "agent.json"))
	}
}

// Entry is a line of an agent's event log.
type Entry struct {
	At      string `json:"at"`
	Kind    string `json:"kind"`
	Text    string `json:"text"`
	TaskID  string `json:"taskId,omitempty"`
	Session string `json:"session,omitempty"`
}

func (f *Fleet) journal(id, kind, text, session string) {
	if os.MkdirAll(f.dirOf(id), privateDir) != nil {
		return
	}
	b, _ := json.Marshal(Entry{At: now(), Kind: kind, Text: text, Session: session})
	if fh, err := os.OpenFile(filepath.Join(f.dirOf(id), "journal.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, privateFile); err == nil {
		fh.Write(append(b, '\n'))
		fh.Close()
	}
}

// Journal is the last n entries of an agent's event log.
func (f *Fleet) Journal(id string, n int) []Entry {
	b, err := os.ReadFile(filepath.Join(f.dirOf(id), "journal.jsonl"))
	if err != nil {
		return []Entry{}
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := []Entry{}
	for _, l := range lines {
		var e Entry
		if json.Unmarshal([]byte(l), &e) == nil && e.Kind != "" {
			out = append(out, e)
		}
	}
	return out
}

// pwModelRe is a pw code model id: provider/model, or a platform model
// (provider:host/model).
var pwModelRe = regexp.MustCompile(`^([a-z0-9][a-z0-9_-]*/\S+|[^\s:]+:\S+/\S+)$`)

// takeOver moves a record of the earlier engine onto a pw code session
// and says what did not carry over.
func (f *Fleet) takeOver(a *Agent) {
	var lost []string
	if a.Execution == "campaign" {
		lost = append(lost, "it ran its ticks as platform workflow runs on "+orStr(str(a.Resource), "a system")+"; it now runs as a pw code session on this computer, with that system named in its AGENTS.md so it can still submit runs there")
	}
	if a.Model != "" && !pwModelRe.MatchString(a.Model) {
		lost = append(lost, "its model "+a.Model+" is a Studio model, not a pw code one, so its session uses pw code's default model")
		a.Model = ""
	}
	if a.Tokens > 0 {
		lost = append(lost, "its "+strconv.FormatFloat(a.Tokens, 'f', 0, 64)+" tokens so far were the Studio model's; from now on its pw code session's tokens add to them")
	}
	if a.CurrentTaskID != nil && *a.CurrentTaskID != "" {
		lost = append(lost, "the tick that was running ("+*a.CurrentTaskID+") is not resumed; its result, if any, stays under tasks/ in the library")
	}
	lost = append(lost, "its ticks used the task board's tools (posting to the board, spawning helpers); a pw code session has its own tools and the Studio's, and does its own splitting")
	a.Engine = "pwcode"
	a.Folder = "agents/fleet/" + a.ID
	a.Execution, a.CurrentTaskID = "", nil
	if a.State == "working" {
		a.State, a.Note = "idle", "taken over from the earlier engine while a tick ran; waiting for the next trigger"
	}
	a.Migrated = "Moved to a pw code session working in " + a.Folder + ". Goal, persona, triggers, budget and journal carried over. Not carried over: " + strings.Join(lost, "; ") + "."
	f.persist(a)
	f.journal(a.ID, "migrated", a.Migrated, "")
}

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// List is the agents, newest first.
func (f *Fleet) List() []Agent {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	out := make([]Agent, 0, len(f.order))
	for _, id := range f.order {
		out = append(out, *f.agents[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// Get is one agent, or nil.
func (f *Fleet) Get(id string) *Agent {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	if a := f.agents[id]; a != nil {
		c := *a
		return &c
	}
	return nil
}

// Input is what a person gives a new agent.
type Input struct {
	Name, Goal, Persona, Model, Resource, Every, WatchPath, ConversationID string
	OnRunEnd                                                               bool
	MaxTicks, MaxTokens, MaxTurns                                          *float64
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugOf(name string) string {
	s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 32 {
		s = strings.TrimRight(s[:32], "-")
	}
	if s == "" {
		s = "agent"
	}
	const abc = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 4)
	for i := range b {
		b[i] = abc[rand.IntN(len(abc))]
	}
	return s + "-" + string(b)
}

// Create makes an agent; its first wake-up is due now.
func (f *Fleet) Create(in Input, owner *auth.User) (*Agent, error) {
	name, goal := strings.TrimSpace(in.Name), strings.TrimSpace(in.Goal)
	if name == "" || goal == "" {
		return nil, errors.New("an agent needs a name and a goal")
	}
	if len([]rune(name)) > 60 {
		name = string([]rune(name)[:60])
	}
	if len([]rune(goal)) > 4000 {
		goal = string([]rune(goal)[:4000])
	}
	model := strings.TrimSpace(in.Model)
	if model != "" && !pwModelRe.MatchString(model) {
		return nil, errors.New("model: a pw code model id, such as claude-code/sonnet, or none for pw code's default")
	}
	every := "1h"
	if _, ok := Cadence[in.Every]; ok {
		every = in.Every
	}
	var watch *string
	if w := strings.Trim(strings.TrimSpace(in.WatchPath), "/"); w != "" {
		c, err := agentsmd.CleanFolder(w)
		if err != nil {
			return nil, errors.New("watch folder: a folder in the library, relative to its top")
		}
		watch = &c
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	id := slugOf(name)
	t := now()
	a := &Agent{ID: id, Name: name, Goal: goal, Model: model,
		Triggers: Triggers{Every: every, OnRunEnd: in.OnRunEnd, WatchPath: watch},
		Budget:   Budget{MaxTicks: in.MaxTicks, MaxTokens: in.MaxTokens, MaxTurns: in.MaxTurns},
		State:    "idle", Note: "created; first wake-up is due now", CreatedAt: t, NextTickAt: sp(t), Inbox: []string{},
		LastSeenMtime: f.newestMtime(str(watch), "agents/fleet/"+id), OwnerUser: owner, OwnerSeenAt: now(), Engine: "pwcode", Folder: "agents/fleet/" + id}
	if p := strings.TrimSpace(in.Persona); p != "" {
		a.Persona = &p
	}
	if r := strings.TrimSpace(in.Resource); r != "" {
		a.Resource = &r
	}
	if owner != nil && owner.ID != "" {
		a.Owner = sp(owner.ID)
	}
	if in.ConversationID != "" {
		a.ConversationID = sp(in.ConversationID)
	}
	f.set(a)
	f.persist(a)
	f.journal(id, "created", "Goal: "+goal, "")
	// Its folder and AGENTS.md are there from the start, as the page
	// says; each wake-up writes them again with the goal as it is then.
	if _, _, err := f.writeInstructions(a); err != nil {
		f.journal(id, "error", "its AGENTS.md was not written yet: "+cut(err.Error(), 300), "")
	}
	return a, nil
}

func (f *Fleet) newestMtime(rel string, skip ...string) float64 {
	if rel == "" {
		return 0
	}
	skipped := map[string]bool{}
	for _, s := range skip {
		if s != "" {
			skipped[filepath.Join(f.A.Cfg.KBRoot, filepath.FromSlash(s))] = true
		}
	}
	newest := 0.0
	var walk func(d string, depth int)
	walk = func(d string, depth int) {
		if depth > 4 {
			return
		}
		ents, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			p := filepath.Join(d, e.Name())
			if e.IsDir() {
				if !skipped[p] {
					walk(p, depth+1)
				}
			} else if st, err := e.Info(); err == nil {
				newest = math.Max(newest, float64(st.ModTime().UnixNano())/1e6)
			}
		}
	}
	walk(filepath.Join(f.A.Cfg.KBRoot, filepath.FromSlash(rel)), 0)
	return newest
}

// NoOwner is the note on an agent that would wake on a Studio with
// sign-in without the record of the person it acts for: one taken over
// from the earlier engine, which kept only their id.
const NoOwner = "waiting for its owner: it was made before agents acted for the person who made them; it wakes again once its owner opens the Fleet page"

// Seen brings the record of the person u acts for up to date in each of
// their agents (their groups and sign-in, which decide what the agent may
// change), and wakes again the agents that waited for it.
func (f *Fleet) Seen(u *auth.User) {
	if u == nil || u.ID == "" || !f.A.Cfg.AuthEnabled() {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	for _, id := range f.order {
		a := f.agents[id]
		if a.Owner == nil || *a.Owner != u.ID || a.State == "retired" {
			continue
		}
		seen, _ := time.Parse(time.RFC3339Nano, a.OwnerSeenAt)
		if sameRights(a.OwnerUser, u) && a.Note != NoOwner && time.Since(seen) < time.Hour {
			continue
		}
		c := *u
		a.OwnerUser, a.OwnerSeenAt = &c, now()
		if a.State == "paused" && a.Note == NoOwner {
			f.scheduleNext(a)
			f.setState(a, "idle", "its owner is known again; waiting for the next trigger")
			continue
		}
		f.persist(a)
	}
}

// groupsFor is how long after the owner was last seen the groups in their
// record count: a group the person has since left would otherwise keep
// its rights in an agent they no longer look at. Names (STUDIO_ADMINS,
// PW_USER) are read at each wake-up and do not age.
const groupsFor = 7 * 24 * time.Hour

// actsAs is the person a acts for now: the owner's record, without its
// groups when the owner has not been seen for groupsFor.
func actsAs(a *Agent) *auth.User {
	if a.OwnerUser == nil {
		return nil
	}
	u := *a.OwnerUser
	if seen, err := time.Parse(time.RFC3339Nano, a.OwnerSeenAt); err != nil || time.Since(seen) > groupsFor {
		u.Groups = nil
	}
	return &u
}

// rightsKey is what decides a person's rights in u, as one string.
func rightsKey(u *auth.User) string {
	if u == nil {
		return ""
	}
	g := slices.Clone(u.Groups)
	slices.Sort(g)
	e, _ := u.Claims["email"].(string)
	return u.ID + "\x00" + u.Username + "\x00" + e + "\x00" + strings.Join(g, ",")
}

// sameRights says whether two records of a person decide the same
// rights: the same id, name, e-mail and groups. A new sign-in's other
// claims (its times) do not count, so a list does not rewrite records.
func sameRights(a, b *auth.User) bool {
	if a == nil || b == nil {
		return a == b
	}
	email := func(u *auth.User) string { e, _ := u.Claims["email"].(string); return e }
	return a.ID == b.ID && a.Username == b.Username && email(a) == email(b) && slices.Equal(a.Groups, b.Groups)
}

func (f *Fleet) setState(a *Agent, state, note string) {
	a.State, a.Note = state, note
	f.journal(a.ID, "state", state+": "+note, "")
	f.persist(a)
}

func (f *Fleet) scheduleNext(a *Agent) {
	if d := Cadence[a.Triggers.Every]; d > 0 {
		a.NextTickAt = sp(time.Now().Add(d).UTC().Format("2006-01-02T15:04:05.000Z"))
	} else {
		a.NextTickAt = nil
	}
}

func budgetUsed(a *Agent) string {
	if m := a.Budget.MaxTicks; m != nil && a.Ticks >= *m {
		return "wake-up budget of " + strconv.FormatFloat(*m, 'f', -1, 64) + " used"
	}
	if m := a.Budget.MaxTokens; m != nil && a.Tokens >= *m {
		return "token budget of " + strconv.FormatFloat(*m, 'f', -1, 64) + " used"
	}
	return ""
}

func turns(a *Agent) int {
	if m := a.Budget.MaxTurns; m != nil && *m >= 1 {
		return int(math.Min(*m, 200))
	}
	return DefaultTurns
}
