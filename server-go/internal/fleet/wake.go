package fleet

import (
	"context"
	"errors"
	"github.com/parallelworks/activate-studio/server-go/internal/mainlib"
	"math"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsmd"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// standingSection is the AGENTS.md section that holds an agent's goal.
const standingSection = "standing_agent"

// instructions is the standing section's text.
func instructions(a *Agent) string {
	var b strings.Builder
	b.WriteString("You are the standing agent \"" + a.Name + "\" of ACTIVATE Studio. Your goal, which persists across wake-ups:\n\n")
	b.WriteString(a.Goal + "\n\n")
	if r := str(a.Resource); r != "" {
		b.WriteString("The system you work on: " + r + ". Submit and follow runs there with the Studio's workflow tools.\n\n")
	}
	b.WriteString("Each message from the Studio is a wake-up and says what woke you. Do one bounded round of work toward the goal, write what you make in this folder or where the goal says, and end with a status a person can read in a minute: what you did, what changed, what is next. When you need a decision from a person, end with one line that starts with NEEDS DECISION: followed by the question, and stop.\n\n" + agentsmd.WorkingNote)
	return b.String()
}

// folder is where the agent works: its library folder when the main
// library can be written here and the person it acts for may change it
// (on a Studio with sign-in, an administrator), else a folder of its own
// under the index, which is no library.
func (f *Fleet) folder(a *Agent) (agentsmd.Folder, string, bool) {
	root := f.A.Cfg.KBRoot
	mayWrite, _ := mainlib.MayWrite(mainlib.User(f.A.Cfg.AuthEnabled(), actsAs(a)))
	if kb.Default != nil && mayWrite {
		if main, ok, err := kb.Default.Libs.Get(""); err == nil && ok && kb.RequireWritable(main) == nil {
			ctx := context.Background()
			return agentsmd.Local{Root: root, Rel: a.Folder, Label: main.Label, Index: func(rels []string) {
				for _, rel := range rels {
					kb.ReindexForFile(ctx, kb.ProviderIndexer{A: f.A}, rel)
				}
			}}, filepath.Join(root, filepath.FromSlash(a.Folder)), true
		}
	}
	own := f.dirOf(a.ID)
	return agentsmd.Local{Root: own, Rel: "work", Label: "the standing agent's folder"}, filepath.Join(own, "work"), false
}

// writeInstructions puts the persona and the goal into the folder's
// AGENTS.md, before every wake-up so an edited goal reaches the session.
func (f *Fleet) writeInstructions(a *Agent) (string, bool, error) {
	fd, abs, inLibrary := f.folder(a)
	if p := str(a.Persona); p != "" && agentsmd.Default != nil {
		if name, text, err := agentsmd.Default.Instructions(p); err == nil {
			if err := agentsmd.ApplyInstructions(fd, name, text); err != nil {
				return "", false, err
			}
		}
	}
	if err := agentsmd.ApplyInstructions(fd, standingSection, instructions(a)); err != nil {
		return "", false, err
	}
	return abs, inLibrary, nil
}

// wakeText is what the session is sent: what woke it, the person's
// messages, and, after a new session, the latest of its earlier results.
func (f *Fleet) wakeText(a *Agent, events []string, fresh bool) string {
	var b strings.Builder
	b.WriteString("Wake-up " + strconv.FormatFloat(a.Ticks, 'f', 0, 64) + ". What woke you:\n")
	for _, e := range events {
		b.WriteString("- " + e + "\n")
	}
	if fresh {
		var results []string
		for _, e := range f.Journal(a.ID, 40) {
			if e.Kind == "result" {
				results = append(results, "- ["+cut(e.At, 16)+"] "+cut(e.Text, 600))
			}
		}
		if len(results) > 4 {
			results = results[len(results)-4:]
		}
		if len(results) > 0 {
			b.WriteString("\nThis is a new session; your goal is in AGENTS.md. Your latest results before it:\n" + strings.Join(results, "\n") + "\n")
		}
	}
	b.WriteString("\nDo one bounded round of work toward your goal now, then end with your status.")
	return b.String()
}

func anyStrings(ss []string) []any {
	out := make([]any, len(ss))
	for i, x := range ss {
		out[i] = x
	}
	return out
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// roundGoal is the goal set for one wake-up.
func roundGoal(a *Agent) string {
	return "This wake-up's round of work toward the standing goal in AGENTS.md is done and its status reported. If this is not met within " + strconv.Itoa(turns(a)) + " turns, stop and say what remains."
}

// Tick wakes the agent now: the reason, with its inbox. It reports
// whether a wake-up started; force wakes a paused agent or one over its
// budget.
func (f *Fleet) Tick(id, reason string, force bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	return f.tick(id, reason, force)
}

func (f *Fleet) tick(id, reason string, force bool) bool {
	a := f.agents[id]
	if a == nil || f.busy[id] {
		return false
	}
	if a.State == "retired" || a.State == "working" || (!force && a.State == "paused") {
		return false
	}
	if used := budgetUsed(a); used != "" && !force {
		f.setState(a, "paused", used)
		return false
	}
	// On a Studio with sign-in, an agent acts for its owner; one with no
	// record of them would run without the Studio's tools (its token has
	// no person) while its note says it works in the library.
	if f.A.Cfg.AuthEnabled() && a.OwnerUser == nil {
		if a.State != "paused" || a.Note != NoOwner {
			f.setState(a, "paused", NoOwner)
		}
		return false
	}
	events := append([]string(nil), a.Inbox...)
	if reason != "" {
		events = append(events, reason)
	}
	a.Inbox = []string{}
	a.Ticks++
	t := now()
	a.LastTickAt = &t
	a.State, a.Note = "working", "wake-up "+strconv.FormatFloat(a.Ticks, 'f', 0, 64)+" starting"
	a.Starting = true
	f.busy[id] = true
	f.persist(a)
	snap := *a
	f.A.Go("fleet wake "+id, func(ctx context.Context) { f.wake(ctx, snap, events) })
	return true
}

// wake starts the round: instructions, the session (made or renewed),
// the message and its goal.
// SharedOff says why a standing agent does not wake on a Studio several
// people use: it would run on this host's pw code, which acts as the
// Studio's own account, and no person borrows the deployment's credential.
const SharedOff = "Standing agents run on this computer's pw code, which acts as the Studio's own account, so they do not run on a Studio several people use."

func (f *Fleet) wake(ctx context.Context, a Agent, events []string) {
	fail := func(why string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.busy, a.ID)
		if cur := f.agents[a.ID]; cur != nil {
			cur.Starting = false
			f.journal(a.ID, "error", cut(why, 400), cur.Session)
			f.scheduleNext(cur)
			f.setState(cur, "idle", "the wake-up could not start: "+cut(why, 160))
		}
	}
	if f.A.Cfg.AuthEnabled() {
		fail(SharedOff)
		return
	}
	c := f.Daemon()
	if c == nil {
		fail("pw code is not set up on this Studio")
		return
	}
	ws, inLibrary, err := f.writeInstructions(&a)
	if err != nil {
		fail("could not write its AGENTS.md: " + err.Error())
		return
	}
	session, fresh := a.Session, false
	if session != "" {
		d, err := pwcode.GetSession(ctx, c, session)
		at, _ := time.Parse(time.RFC3339Nano, a.SessionAt)
		switch {
		case err != nil || jsonv.String(jsonv.Path(d, "archivedAt")) != "":
			session = ""
		case ws != a.Workspace || time.Since(at) > f.Renew || rightsKey(actsAs(&a)) != a.SessionRights:
			// A new session also when the person's rights changed: its
			// Studio token is made with the rights of its start.
			pwcode.ArchiveSession(ctx, c, session)
			session = ""
		}
	}
	if session == "" {
		// It reads the library and lists its folder without asking
		// (agentsmd.ReadRules); out of the library, only its folder.
		top := ""
		if inLibrary {
			top = f.A.Cfg.KBRoot
		}
		rules := agentsmd.ReadRules(top)
		var owner = actsAs(&a)
		if f.Start != nil {
			session, err = f.Start(ctx, c, owner, "fleet:"+a.ID, ws, a.Model, f.Mode, rules)
		}
		if f.Start == nil || err != nil {
			why := ""
			if err != nil {
				why = err.Error()
			}
			req := jsonv.NewObject("workspace", ws, "remoteControl", true, "permissionMode", f.Mode, "allowedTools", anyStrings(rules))
			if a.Model != "" {
				req.Set("model", a.Model)
			}
			s, err2 := pwcode.CreateSession(ctx, c, req)
			if err2 != nil {
				fail("could not start its pw code session: " + err2.Error())
				return
			}
			session = jsonv.String(jsonv.Path(s, "id"))
			if why != "" {
				f.journal(a.ID, "error", "its session started without the Studio's tools: "+cut(why, 300), session)
			}
		}
		fresh = true
		f.moveModel(ctx, c, &a, owner, session)
	}
	// A new session is recorded before the message goes, and the wake-up
	// is recorded as working as soon as the message is in: a restart in
	// between leaves the record saying what the session is doing.
	if fresh {
		f.mu.Lock()
		if cur := f.agents[a.ID]; cur != nil {
			if cur.Session != "" && cur.Session != session {
				cur.Sessions = append(cur.Sessions, cur.Session)
			}
			cur.Session, cur.SessionAt, cur.SessionTokens, cur.Workspace = session, time.Now().UTC().Format(time.RFC3339Nano), 0, ws
			cur.SessionRights = rightsKey(actsAs(&a))
			f.persist(cur)
		}
		f.mu.Unlock()
	}
	if _, err := pwcode.SendMessage(ctx, c, session, f.wakeText(&a, events, fresh)); err != nil {
		fail("could not wake its session: " + err.Error())
		return
	}
	f.mu.Lock()
	if cur := f.agents[a.ID]; cur != nil {
		cur.Starting = false
		cur.Note = "wake-up " + strconv.FormatFloat(cur.Ticks, 'f', 0, 64) + " working"
		f.journal(a.ID, "tick", cut(strings.Join(events, " | "), 400), session)
		f.persist(cur)
	}
	f.mu.Unlock()
	if _, err := pwcode.SetGoal(ctx, c, session, roundGoal(&a)); err != nil {
		f.journal(a.ID, "error", "its round's goal was not set: "+cut(err.Error(), 300), session)
	}
	f.mu.Lock()
	delete(f.busy, a.ID)
	f.mu.Unlock()
}

/* ---- following a working session ---- */

var decisionRe = regexp.MustCompile(`(?m)NEEDS DECISION:\s*(.+)`)

// look is a session's status: settled, waiting for approval (with the
// first request), its last error, its last answer, and its tokens.
type look struct {
	settled, waiting bool
	ask, err, answer string
	tokens           float64
	gone             bool
}

func lookAt(ctx context.Context, c pwcode.Client, id string) (look, error) {
	d, err := pwcode.GetSession(ctx, c, id)
	if err != nil {
		var de *pwcode.DaemonError
		if errors.As(err, &de) && de.Status == 404 {
			return look{gone: true, settled: true}, nil
		}
		return look{}, err
	}
	l := look{err: text(jsonv.Path(d, "lastError")), tokens: num(jsonv.Path(d, "inputTokens")) + num(jsonv.Path(d, "outputTokens"))}
	status := text(jsonv.Path(d, "status"))
	approvals := jsonv.Arr(jsonv.Path(d, "approvals"))
	if status == "awaiting_approval" || len(approvals) > 0 {
		l.settled, l.waiting = true, true
		if len(approvals) > 0 {
			l.ask = approvalOf(approvals[0]).summary()
		}
	}
	hist := jsonv.Arr(jsonv.Path(d, "history"))
	for i := len(hist) - 1; i >= 0; i-- {
		k := text(jsonv.Path(hist[i], "kind"))
		if k == "user" {
			break
		}
		if k == "assistant" && l.answer == "" {
			l.answer = text(jsonv.Path(hist[i], "text"))
		}
	}
	if l.settled {
		return l, nil
	}
	if status == "running" {
		return l, nil
	}
	active := false
	if st, err := pwcode.SessionState(ctx, c, id); err == nil && st != nil {
		g := jsonv.Obj(jsonv.Path(st, "goal"))
		if g != nil {
			a, _ := g.Get("active")
			p, _ := g.Get("paused")
			active = a == true && p != true
		}
	}
	l.settled = !active
	return l, nil
}

func text(v any) string { s, _ := v.(string); return s }

func num(v any) float64 { n, _ := v.(float64); return n }

// follow looks at each working agent's session once and settles those
// that finished their round; with only, just those agents.
func (f *Fleet) follow(ctx context.Context, only ...string) {
	f.mu.Lock()
	f.load()
	type job struct{ id, session string }
	var jobs []job
	for _, id := range f.order {
		a := f.agents[id]
		if len(only) > 0 && !slices.Contains(only, id) {
			continue
		}
		if (a.State == "working" || a.State == "input-required") && a.Session != "" && !f.busy[id] {
			jobs = append(jobs, job{id, a.Session})
		}
	}
	f.mu.Unlock()
	c := f.Daemon()
	if c == nil {
		return
	}
	for _, j := range jobs {
		l, err := lookAt(ctx, c, j.session)
		if err != nil {
			continue
		}
		f.mu.Lock()
		if a := f.agents[j.id]; a != nil && a.Session == j.session {
			f.settle(a, l)
		}
		f.mu.Unlock()
	}
}

// settle records a look at the agent's session.
func (f *Fleet) settle(a *Agent, l look) {
	if l.tokens > a.SessionTokens {
		a.Tokens += l.tokens - a.SessionTokens
		a.SessionTokens = l.tokens
	}
	switch {
	case l.gone:
		a.Sessions = append(a.Sessions, a.Session)
		a.Session = ""
		f.scheduleNext(a)
		f.setState(a, "idle", "its session is no longer on this computer; the next wake-up starts another")
		return
	case !l.settled:
		if a.State == "input-required" {
			// The person answered on the Code page and it went on.
			f.setState(a, "working", "working again")
		}
		return
	case l.waiting:
		if a.State != "input-required" {
			f.setState(a, "input-required", cut("waiting for your approval: "+orStr(l.ask, "a request in its session"), 300))
		}
		return
	case a.State == "input-required" && decisionRe.MatchString(l.answer):
		// Still the same question.
		return
	}
	f.journal(a.ID, "result", cut(l.answer, 4000), a.Session)
	f.scheduleNext(a)
	// What changed under its watched folder while it worked was its own
	// round's doing (a summary beside the results it read), and it reads
	// the folder again next time: it does not wake it again. Its own
	// folder never does.
	if wp := str(a.Triggers.WatchPath); wp != "" {
		a.LastSeenMtime = math.Max(a.LastSeenMtime, f.newestMtime(wp, a.Folder))
	}
	if a.State == "paused" {
		f.persist(a)
		return
	}
	if d := decisionRe.FindStringSubmatch(l.answer); d != nil {
		f.setState(a, "input-required", cut(strings.TrimSpace(d[1]), 300))
		return
	}
	if l.err != "" {
		f.setState(a, "idle", "last wake-up stopped on an error: "+cut(l.err, 160))
		return
	}
	if used := budgetUsed(a); used != "" {
		f.setState(a, "paused", used)
	} else if a.NextTickAt != nil {
		// The time is the record's NextTickAt, which the page shows in the
		// reader's own time.
		f.setState(a, "idle", "waiting for its next wake-up")
	} else {
		f.setState(a, "idle", "waiting for an event or a message")
	}
}

/* ---- what a person does ---- */

// Pause stops the agent's wake-ups; a round under way finishes.
func (f *Fleet) Pause(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	a := f.agents[id]
	if a == nil || a.State == "retired" {
		return false
	}
	f.setState(a, "paused", "paused by user")
	return true
}

// Resume wakes the agent now.
func (f *Fleet) Resume(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	a := f.agents[id]
	if a == nil || a.State == "retired" {
		return false
	}
	a.NextTickAt = sp(now())
	f.setState(a, "idle", "resumed; next wake-up due now")
	return true
}

// Retire stops the agent for good and archives its session, which stays
// readable on the Code page with its archived sessions.
func (f *Fleet) Retire(id string) bool {
	f.mu.Lock()
	f.load()
	a := f.agents[id]
	if a == nil {
		f.mu.Unlock()
		return false
	}
	session := a.Session
	f.setState(a, "retired", "retired by user")
	f.mu.Unlock()
	if c := f.Daemon(); c != nil && session != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		pwcode.ArchiveSession(ctx, c, session)
	}
	return true
}

// Message gives the agent a person's words: straight into its session
// while it works, else as the reason for a wake-up now.
func (f *Fleet) Message(id, msg string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	a := f.agents[id]
	if a == nil || a.State == "retired" {
		return false
	}
	f.journal(id, "message", msg, a.Session)
	if a.State == "working" && a.Session != "" && !f.busy[id] {
		if c := f.Daemon(); c != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if _, err := pwcode.SteerSession(ctx, c, a.Session, "Message from the person who runs you: "+msg); err == nil {
				return true
			}
		}
	}
	a.Inbox = append(a.Inbox, "Message from the person who runs you: "+msg)
	if a.State == "input-required" || a.State == "paused" {
		a.State = "idle"
	}
	a.NextTickAt = sp(now())
	f.persist(a)
	return true
}

// RunEnded is a platform run that ended.
type RunEnded struct{ Slug, Workflow, Resource, State string }

// NotifyRunEnded wakes the agents that asked to hear about ended runs.
func (f *Fleet) NotifyRunEnded(ev RunEnded) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	msg := "Run " + ev.Slug + " of " + ev.Workflow
	if ev.Resource != "" {
		msg += " on " + ev.Resource
	}
	msg += " ended: " + ev.State
	for _, id := range f.order {
		a := f.agents[id]
		if a.State == "retired" || !a.Triggers.OnRunEnd {
			continue
		}
		f.journal(id, "event", msg, a.Session)
		switch a.State {
		case "working", "paused", "input-required":
			a.Inbox = append(a.Inbox, msg)
			f.persist(a)
			continue
		}
		f.tick(id, msg, false)
	}
}

// Follow brings the agents up to date with their sessions now, as a page
// reads them, so a round that ended shows without waiting for the next
// scheduler pass; with ids, just those agents.
func (f *Fleet) Follow(ctx context.Context, ids ...string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	f.follow(ctx, ids...)
}

// Pass is one scheduler pass: working sessions followed, watched folders
// looked at, and due wake-ups started.
func (f *Fleet) Pass(ctx context.Context) {
	f.follow(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	nowT := time.Now()
	for _, id := range append([]string(nil), f.order...) {
		a := f.agents[id]
		if a.State != "idle" {
			continue
		}
		if wp := str(a.Triggers.WatchPath); wp != "" {
			if newest := f.newestMtime(wp, a.Folder); newest > a.LastSeenMtime {
				a.LastSeenMtime = newest
				f.persist(a)
				why := "New or changed files under " + wp
				f.journal(id, "event", why, a.Session)
				f.tick(id, why, false)
				continue
			}
		}
		if a.NextTickAt != nil {
			if t, err := time.Parse(time.RFC3339Nano, *a.NextTickAt); err == nil && !t.After(nowT) {
				// A wake-up a message or an event asked for says only that.
				reason := "the schedule"
				if len(a.Inbox) > 0 {
					reason = ""
				}
				f.tick(id, reason, false)
			}
		}
	}
}

// Run is the scheduler: a pass every interval until ctx ends.
func (f *Fleet) Run(ctx context.Context, every time.Duration) {
	f.mu.Lock()
	f.load()
	n := 0
	for _, id := range f.order {
		if f.agents[id].State != "retired" {
			n++
		}
	}
	f.mu.Unlock()
	if n > 0 {
		f.A.Log.Info("fleet: standing agents loaded", "count", n)
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.Pass(ctx)
		}
	}
}

// Recent is the end of the agent's current session, as lines.
func (f *Fleet) Recent(ctx context.Context, a *Agent, n int) []string {
	c := f.Daemon()
	if c == nil || a.Session == "" {
		return []string{}
	}
	d, err := pwcode.GetSession(ctx, c, a.Session)
	if err != nil {
		return []string{}
	}
	hist := jsonv.Arr(jsonv.Path(d, "history"))
	out := []string{}
	for _, h := range hist {
		k, t := text(jsonv.Path(h, "kind")), strings.TrimSpace(text(jsonv.Path(h, "text")))
		switch k {
		case "user":
			out = append(out, "> "+cut(t, 600))
		case "assistant":
			if t != "" {
				out = append(out, cut(t, 2000))
			}
		case "tool_call":
			out = append(out, "· "+text(jsonv.Path(h, "toolName")))
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// moveModel moves a standing agent's new session off a model its owner's
// AI connection does not offer (app.Providers.AgentModel), before its
// first message, and journals the move.
func (f *Fleet) moveModel(ctx context.Context, c pwcode.Client, a *Agent, owner *auth.User, session string) {
	pick := f.A.Providers.AgentModel
	if pick == nil {
		return
	}
	d, err := pwcode.GetSession(ctx, c, session)
	if err != nil {
		return
	}
	cur := text(jsonv.Path(d, "model"))
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, "/api/fleet", nil)
	if err != nil {
		return
	}
	if owner != nil {
		r = auth.WithUser(r, owner)
	}
	to, why := pick(r, cur, a.Model)
	if to == "" {
		return
	}
	if _, err := pwcode.SetSessionModel(ctx, c, session, to); err != nil {
		f.journal(a.ID, "error", "its model "+cur+" is not one the AI connection offers, and its session could not be moved to "+to+": "+cut(err.Error(), 300), session)
		return
	}
	f.journal(a.ID, "note", why, session)
}
