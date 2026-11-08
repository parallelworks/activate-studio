// Package runs is the run registry and watcher (runs.ts): every workflow
// run the Studio launches, kept in INDEX_BASE/runs.jsonl and followed to
// its end across restarts. When a run ends, the conversation that launched
// it gets a note, and whoever registered with OnRunEnded (the fleet) hears.
package runs

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/sessions"
)

// Record is runs.ts's RunRecord; the pointers are null when nil.
type Record struct {
	Slug           string  `json:"slug"`
	Workflow       string  `json:"workflow"`
	Resource       *string `json:"resource"`
	ConversationID *string `json:"conversationId"`
	Owner          *string `json:"owner"`
	LaunchedAt     string  `json:"launchedAt"`
	State          string  `json:"state"`
	EndedAt        *string `json:"endedAt"`
}

// Object is the record with runs.ts's keys in their order.
func (r *Record) Object() *jsonv.Object {
	return jsonv.NewObject("slug", r.Slug, "workflow", r.Workflow, "resource", sp(r.Resource), "conversationId", sp(r.ConversationID),
		"owner", sp(r.Owner), "launchedAt", r.LaunchedAt, "state", r.State, "endedAt", sp(r.EndedAt))
}

// MarshalJSON writes Object.
func (r *Record) MarshalJSON() ([]byte, error) { return jsonv.Marshal(r.Object()) }

func sp(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// Str is a nullable string: "" is null.
func Str(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// RunEnded is what the fleet hears when a run ends (fleet.ts notifyRunEnded).
type RunEnded struct {
	Slug, Workflow string
	Resource       *string
	State          string
}

var (
	hooksMu sync.Mutex
	hooks   []func(RunEnded)
)

// OnRunEnded registers fn to hear every run's end.
func OnRunEnded(fn func(RunEnded)) {
	hooksMu.Lock()
	hooks = append(hooks, fn)
	hooksMu.Unlock()
}

var terminal = map[string]bool{"completed": true, "error": true, "failed": true, "canceled": true, "cancelled": true}

// IsTerminal is isTerminalRunState.
func IsTerminal(s string) bool { return terminal[strings.ToLower(s)] }

// Registry is the registry of one index base.
type Registry struct {
	p    *platform.Platform
	file string

	mu        sync.Mutex
	recs      map[string]*Record
	order     []string
	stamp     string
	loaded    bool
	shown     map[string]bool
	watches   map[string]context.CancelFunc
	appendErr error

	// CLI runs `pw` for the watcher (the deployment's credential).
	CLI func(ctx context.Context, args []string) (string, error)
	// Go runs a background job until the server stops (app.Go).
	Go func(name string, fn func(ctx context.Context))
	// Log reports the watcher's events.
	Log func(string)
	// Note appends a note to a conversation (Node's bridge).
	Note func(conversationID, content string)
	// SessionKey is the key a run's sessions are listed with, for its owner.
	SessionKey func(owner *string) string
	// Sessions lists a key's sessions.
	Sessions func(ctx context.Context, key string) ([]*sessions.SessionRow, error)
}

// New is the registry under p's index base, with the CLI, bridge and
// session list of p.
func New(p *platform.Platform) *Registry {
	r := &Registry{p: p, file: filepath.Join(p.IndexBase(), "runs.jsonl"), watches: map[string]context.CancelFunc{}, shown: map[string]bool{}}
	r.CLI = func(ctx context.Context, args []string) (string, error) {
		out, err := p.Exec(ctx, args, platform.ExecOptions{Timeout: 60 * time.Second, MaxBuffer: 4 << 20})
		if err != nil {
			return "", plainErr(platform.ErrText(err))
		}
		return out, nil
	}
	r.Go = func(name string, fn func(ctx context.Context)) { go fn(context.Background()) }
	r.Log = func(msg string) { p.Log().Info(msg) }
	r.Note = func(id, content string) {
		b := p.Node()
		if b == nil {
			r.Log("run note for conversation " + id + " not delivered: no Node server")
			return
		}
		if err := b.Background(http.MethodPost, platform.BridgePrefix+"conversation-note", jsonv.NewObject("conversationId", id, "content", content), nil); err != nil {
			r.Log("run note for conversation " + id + " not delivered: " + err.Error())
		}
	}
	r.SessionKey = func(owner *string) string {
		if owner == nil {
			// resolveUserCred(null): the single owner's stored key with
			// sign-in off, else the deployment's.
			req, _ := http.NewRequest(http.MethodGet, "/", nil)
			v, err := p.ViewerOf(req)
			if err == nil {
				k, _ := p.KeyFor(v)
				return k
			}
		}
		// A username names no stored key; a live personal key is Node's.
		return p.GatewayKey()
	}
	r.Sessions = func(ctx context.Context, key string) ([]*sessions.SessionRow, error) {
		return sessions.List(ctx, p, key)
	}
	return r
}

type plainErr string

func (e plainErr) Error() string { return string(e) }

// File is the registry file.
func (r *Registry) File() string { return r.file }

func stampOf(file string) string {
	st, err := os.Stat(file)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(st.Size(), 10) + ":" + strconv.FormatInt(st.ModTime().UnixNano(), 10)
}

// loadLocked reads the file again when it changed: Node appends to it too.
func (r *Registry) loadLocked() {
	st := stampOf(r.file)
	if r.loaded && st == r.stamp {
		return
	}
	r.loaded, r.stamp = true, st
	r.recs, r.order = map[string]*Record{}, nil
	r.shown = map[string]bool{}
	b, err := os.ReadFile(r.file)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev struct {
			T              string  `json:"t"`
			Slug           string  `json:"slug"`
			Workflow       string  `json:"workflow"`
			Resource       *string `json:"resource"`
			ConversationID *string `json:"conversationId"`
			Owner          *string `json:"owner"`
			LaunchedAt     string  `json:"launchedAt"`
			State          string  `json:"state"`
			EndedAt        *string `json:"endedAt"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue // a torn line is skipped
		}
		switch ev.T {
		case "launch":
			r.setLocked(&Record{Slug: ev.Slug, Workflow: ev.Workflow, Resource: ev.Resource, ConversationID: ev.ConversationID, Owner: ev.Owner, LaunchedAt: ev.LaunchedAt, State: "running"})
		case "end":
			if rec := r.recs[ev.Slug]; rec != nil {
				rec.State, rec.EndedAt = ev.State, ev.EndedAt
			}
		case "session":
			r.shown[ev.Slug] = true
		}
	}
}

func (r *Registry) setLocked(rec *Record) {
	if _, ok := r.recs[rec.Slug]; !ok {
		r.order = append(r.order, rec.Slug)
	}
	r.recs[rec.Slug] = rec
}

func (r *Registry) appendLocked(ev *jsonv.Object) {
	os.MkdirAll(filepath.Dir(r.file), 0o755)
	f, err := os.OpenFile(r.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return // the in-memory record still drives this process
	}
	f.Write(append(jsonv.MustMarshal(ev), '\n'))
	f.Close()
	r.stamp = stampOf(r.file)
}

// Launch is the launch line: {t: 'launch', ...record}.
func launchLine(rec *Record) *jsonv.Object {
	o := jsonv.NewObject("t", "launch")
	ro := rec.Object()
	for _, k := range ro.Keys() {
		v, _ := ro.Get(k)
		o.Set(k, v)
	}
	return o
}

// RecordInput is what a launch knows.
type RecordInput struct {
	Slug, Workflow                  string
	Resource, ConversationID, Owner *string
	// LaunchedAt is now when empty (Node sends its own).
	LaunchedAt string
}

// RecordRun is recordRun: the run registered and watched.
func (r *Registry) RecordRun(in RecordInput) *Record {
	at := in.LaunchedAt
	if at == "" {
		at = platform.NowISO()
	}
	rec := &Record{Slug: in.Slug, Workflow: in.Workflow, Resource: in.Resource, ConversationID: in.ConversationID, Owner: in.Owner, LaunchedAt: at, State: "running"}
	r.mu.Lock()
	r.loadLocked()
	r.setLocked(rec)
	r.appendLocked(launchLine(rec))
	r.mu.Unlock()
	r.Watch(rec.Slug)
	c := *rec
	return &c
}

// List is listRuns(limit): newest first.
func (r *Registry) List(limit int) []*Record {
	r.mu.Lock()
	r.loadLocked()
	out := make([]*Record, 0, len(r.order))
	for _, s := range r.order {
		c := *r.recs[s]
		out = append(out, &c)
	}
	r.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool { return out[i].LaunchedAt > out[j].LaunchedAt })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Get is one run's record, or nil.
func (r *Registry) Get(slug string) *Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadLocked()
	if rec := r.recs[slug]; rec != nil {
		c := *rec
		return &c
	}
	return nil
}

// MarkEnded is markRunEnded.
func (r *Registry) MarkEnded(slug, state string) {
	r.mu.Lock()
	r.loadLocked()
	rec := r.recs[slug]
	if rec == nil || rec.EndedAt != nil {
		r.mu.Unlock()
		return
	}
	now := platform.NowISO()
	rec.State, rec.EndedAt = state, &now
	r.appendLocked(jsonv.NewObject("t", "end", "slug", slug, "state", state, "endedAt", now))
	if cancel := r.watches[slug]; cancel != nil {
		cancel()
		delete(r.watches, slug)
	}
	c := *rec
	r.mu.Unlock()
	if c.ConversationID != nil {
		where := ""
		if c.Resource != nil && *c.Resource != "" {
			where = " on " + *c.Resource
		}
		tail := " and why it failed"
		if state == "completed" {
			tail = ""
		}
		r.Note(*c.ConversationID, "Run "+slug+" of "+c.Workflow+where+" finished: "+state+". Ask for its detail (workflow_run_detail "+slug+") to see the output"+tail+".")
	}
	r.Log("run " + slug + " ended: " + state)
	hooksMu.Lock()
	hs := append([]func(RunEnded){}, hooks...)
	hooksMu.Unlock()
	for _, h := range hs {
		h(RunEnded{Slug: slug, Workflow: c.Workflow, Resource: c.Resource, State: state})
	}
}

// showSession is showRunSession: a run launched from chat that opens a
// session shows it in that conversation once it is running.
func (r *Registry) showSession(ctx context.Context, slug string) {
	r.mu.Lock()
	r.loadLocked()
	rec := r.recs[slug]
	if rec == nil || rec.ConversationID == nil || r.shown[slug] {
		r.mu.Unlock()
		return
	}
	c := *rec
	r.mu.Unlock()
	key := r.SessionKey(c.Owner)
	if key == "" {
		return
	}
	rows, err := r.Sessions(ctx, key)
	if err != nil {
		return
	}
	// Polls overlap when the platform answers slower than the poll interval.
	r.mu.Lock()
	done := r.shown[slug]
	r.mu.Unlock()
	if done {
		return
	}
	var s *sessions.SessionRow
	for _, x := range rows {
		if sessions.OfRun(x, slug) && x.Status == "running" {
			s = x
			break
		}
	}
	if s == nil {
		return
	}
	r.mu.Lock()
	r.shown[slug] = true
	r.appendLocked(jsonv.NewObject("t", "session", "slug", slug, "name", s.Name))
	r.mu.Unlock()
	r.Note(*c.ConversationID, "Run "+slug+" of "+c.Workflow+" opened the session "+s.Name+".\n\n"+sessions.Embed(s))
	r.Log("run " + slug + ": session " + s.Name + " shown in its conversation")
}

func (r *Registry) pollEvery() time.Duration {
	if n, err := strconv.ParseFloat(strings.TrimSpace(r.p.Getenv("STUDIO_RUN_POLL_MS")), 64); err == nil && n > 0 {
		return time.Duration(n * float64(time.Millisecond))
	}
	return 60 * time.Second
}

var trailingNotice = regexp.MustCompile(`\n[^{}\[\]]*$`)

// parseView is the watcher's reading of `pw workflows runs view -o json`.
func parseView(out string) (string, error) {
	start := strings.IndexAny(out, "[{")
	if start < 0 {
		start = 0
	}
	v, err := jsonv.DecodeString(trailingNotice.ReplaceAllString(out[start:], ""))
	if err != nil {
		return "", err
	}
	st := jsonv.Path(v, "status")
	if !jsonv.Truthy(st) {
		return "", nil
	}
	return jsonv.String(st), nil
}

// Watch is watchRun: the run checked now and every STUDIO_RUN_POLL_MS
// until it ends, or 30 checks in a row fail.
func (r *Registry) Watch(slug string) {
	r.mu.Lock()
	if r.watches[slug] != nil {
		r.mu.Unlock()
		return
	}
	wctx, cancel := context.WithCancel(context.Background())
	r.watches[slug] = cancel
	r.mu.Unlock()
	every := r.pollEvery()
	r.Go("run watcher "+slug, func(ctx context.Context) {
		stop := context.AfterFunc(ctx, cancel) // the server's stop ends it too
		defer stop()
		defer cancel()
		misses := 0
		check := func() bool {
			out, err := r.CLI(wctx, []string{"workflows", "runs", "view", slug, "-o", "json"})
			status := ""
			if err == nil {
				status, err = parseView(out)
			}
			if wctx.Err() != nil {
				return false
			}
			if err != nil {
				// A run the platform no longer knows, or a CLI without
				// credentials, is not worth polling forever.
				misses++
				if misses >= 30 {
					r.mu.Lock()
					delete(r.watches, slug)
					r.mu.Unlock()
					r.Log("run " + slug + ": stopped watching after " + strconv.Itoa(misses) + " failed checks")
					return false
				}
				return true
			}
			misses = 0
			if status != "" && IsTerminal(status) {
				r.MarkEnded(slug, strings.ToLower(status))
				return false
			}
			r.showSession(wctx, slug)
			return true
		}
		if !check() {
			return
		}
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-wctx.Done():
				return
			case <-t.C:
				if !check() {
					return
				}
			}
		}
	})
}

// WatchRegistered is watchRegisteredRuns: every run that had not ended is
// watched again; the count is returned.
func (r *Registry) WatchRegistered() int {
	r.mu.Lock()
	r.loadLocked()
	var open []string
	for _, s := range r.order {
		rec := r.recs[s]
		if rec.EndedAt == nil && !IsTerminal(rec.State) {
			open = append(open, s)
		}
	}
	r.mu.Unlock()
	for _, s := range open {
		r.Watch(s)
	}
	return len(open)
}

// Watching reports whether slug is being watched (tests).
func (r *Registry) Watching(slug string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.watches[slug] != nil
}

// Stop ends every watch (tests).
func (r *Registry) Stop() {
	r.mu.Lock()
	ws := r.watches
	r.watches = map[string]context.CancelFunc{}
	r.mu.Unlock()
	for _, c := range ws {
		c()
	}
}
