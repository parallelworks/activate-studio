// Package notices is what needs the person across the Studio: the chats
// that wait on them or have a reply they have not seen, the Code page's
// sessions and the standing agents that wait on them, delegated work that
// ended, transfers that failed and library indexes that could not be
// built. The menu's marks, the chat list's marks and the Notices page all
// read one answer, GET /api/notices, which also carries the Code page's
// listing, so none of them can disagree.
//
// Each person sees their own. What they have seen and dismissed is kept
// per person under INDEX_BASE/notices.
package notices

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/chat"
	"github.com/parallelworks/activate-studio/server-go/internal/delegation"
	"github.com/parallelworks/activate-studio/server-go/internal/fleet"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/transfers"
)

// Item is one notice.
type Item struct {
	// ID names what happened: an approval, a reply, an ended session. One
	// dismissed comes back only when something else happens there.
	ID string `json:"id"`
	// Kind is where it is from: chat, code, fleet, delegated, transfer or
	// library.
	Kind string `json:"kind"`
	// What is waiting (it goes on when they answer), reply, note, finished
	// or failed.
	What  string `json:"what"`
	Title string `json:"title"`
	Text  string `json:"text"`
	At    string `json:"at"`
	Href  string `json:"href"`
	// Conversation and Session are the chat and the pw code session it is
	// about, where it is about one.
	Conversation string `json:"conversation,omitempty"`
	Session      string `json:"session,omitempty"`
	// Nav is the menu entries that count it.
	Nav []string `json:"nav"`
}

// Chat is the state of one of the person's chats, for its row in the list.
type Chat struct {
	Session string `json:"session,omitempty"`
	// Machine and MachineName are that session's machine as the chat knows
	// it, for one whose session is not listed now.
	Machine     string `json:"machine,omitempty"`
	MachineName string `json:"machineName,omitempty"`
	// Local: the session is on this computer.
	Local bool `json:"local,omitempty"`
	// Row is its session as pw code lists it, where the listing beside it
	// does not have it (a session no platform lists).
	Row     *jsonv.Object `json:"row,omitempty"`
	Working bool          `json:"working,omitempty"`
	Waiting bool          `json:"waiting,omitempty"`
	// Unread is reply or note, for one they have not seen.
	Unread string `json:"unread,omitempty"`
}

// Answer is GET /api/notices. Listed is whether the Code page's sessions
// could be read; Machines and Sessions are then that listing.
type Answer struct {
	Items    []Item          `json:"items"`
	Chats    map[string]Chat `json:"chats"`
	Listed   bool            `json:"listed"`
	Machines []any           `json:"machines"`
	Sessions []any           `json:"sessions"`
	// Transfers is the Transfers entry's summary, which the page would
	// otherwise ask for as well; absent where it cannot be read.
	Transfers *transfers.Summary `json:"transfers,omitempty"`
}

// Sources is where the notices come from, each for the person a request
// is from.
type Sources struct {
	Listing   func(r *http.Request) (machines, sessions []any, err error)
	Chats     func(r *http.Request) []chat.ChatState
	Local     func(r *http.Request, ids map[string]bool) map[string]*jsonv.Object
	Fleet     func(r *http.Request) []fleet.Agent
	Delegated func(r *http.Request) []delegation.Record
	Transfers func(r *http.Request) []transfers.Transfer
	// Summary is the Transfers entry's, or nil.
	Summary func(r *http.Request) *transfers.Summary
	// TransferSeen marks a failed transfer seen, as its details do.
	TransferSeen func(r *http.Request, id string)
	Libraries    func(r *http.Request) []Library
}

// Library is a library whose index could not be built or updated.
type Library struct {
	ID, Label, Error string
	Built            bool
}

// build is the answer for the person r is from, whose record is st. It
// notes in st when an item with no time of its own was first seen.
func build(r *http.Request, src Sources, st *record, now time.Time) Answer {
	a := Answer{Chats: map[string]Chat{}, Machines: []any{}, Sessions: []any{}, Transfers: src.Summary(r)}
	listed := map[string]*jsonv.Object{}
	if ms, ss, err := src.Listing(r); err == nil {
		a.Listed, a.Machines, a.Sessions = true, ms, ss
		for _, s := range ss {
			if o := jsonv.Obj(s); o != nil {
				listed[str(o, "id")] = o
			}
		}
	}
	items := []Item{}
	// covered is the sessions an item is already about.
	covered := map[string]bool{}
	// code is the Code entry too, for a session its page lists.
	code := func(session string, nav ...string) []string {
		if o := listed[session]; o != nil && Waits(o) {
			nav = append(nav, "code")
		}
		return nav
	}

	chats := src.Chats(r)
	unlisted := map[string]bool{}
	for _, c := range chats {
		if c.Session != "" && c.Local && listed[c.Session] == nil {
			unlisted[c.Session] = true
		}
	}
	local := src.Local(r, unlisted)
	for _, c := range chats {
		state := Chat{Session: c.Session, Machine: c.Machine, MachineName: c.MachineName, Local: c.Local, Working: c.Working}
		row := listed[c.Session]
		if row == nil && c.Session != "" {
			row = local[c.Session]
			state.Row = row
		}
		asks := []string{}
		text, at := "", ""
		for _, x := range c.Asks {
			asks = append(asks, x.ID)
			if text == "" {
				text, at = askText(x.Approval, x.Tool), x.At
			}
		}
		if row != nil {
			if len(asks) > 0 && !Waits(row) {
				// The turn followed here asked a moment ago, and the listing
				// is from before it.
				row.Set("status", "awaiting_approval")
			}
			for _, x := range approvals(row) {
				if id := str(x, "id"); !contains(asks, id) {
					asks = append(asks, id)
				}
				if text == "" {
					text, at = askText(x, ""), str(x, "createdAt")
				}
			}
			state.Working = state.Working || str(row, "status") == "running"
		}
		state.Waiting = len(asks) > 0 || (row != nil && Waits(row))
		if state.Waiting {
			state.Working = false
			if text == "" {
				text = askText(nil, "")
			}
			sort.Strings(asks)
			covered[c.Session] = true
			items = append(items, Item{ID: "chat:" + c.ID + ":" + strings.Join(asks, ","), Kind: "chat", What: "waiting", Title: titled(c.Title),
				Text: text, At: first(at, updated(row), c.UpdatedAt), Href: "#chat=" + c.ID, Conversation: c.ID, Session: c.Session, Nav: code(c.Session, "chat")})
		}
		if !state.Working && st.unread(c) {
			state.Unread = "reply"
			words := "New reply"
			if c.Reply.Note {
				state.Unread, words = "note", "New note"
			}
			if !state.Waiting {
				items = append(items, Item{ID: "reply:" + c.ID + ":" + c.Reply.ID, Kind: "chat", What: state.Unread, Title: titled(c.Title),
					Text: words, At: c.Reply.At, Href: "#chat=" + c.ID, Conversation: c.ID, Session: c.Session, Nav: []string{"chat"}})
			}
		}
		if state != (Chat{}) {
			a.Chats[c.ID] = state
		}
	}

	for _, g := range src.Fleet(r) {
		if g.State != "input-required" {
			continue
		}
		if g.Session != "" {
			if covered[g.Session] {
				continue
			}
			covered[g.Session] = true
		}
		text := first(g.Note, "Asks for a decision")
		items = append(items, Item{ID: "fleet:" + g.ID + ":" + sum(g.Session+"\n"+text), Kind: "fleet", What: "waiting", Title: first(g.Name, "Agent"),
			Text: text, Href: "#view=agents:fleet?agent=" + esc(g.ID), Session: g.Session, Nav: code(g.Session, "agents")})
	}

	for _, d := range src.Delegated(r) {
		it := Item{Kind: "delegated", Title: first(line(d.Objective), "Delegated work"), Session: d.Session, Href: "#view=code?session=" + esc(d.Session), At: d.Finished}
		switch d.State {
		case "waiting":
			if covered[d.Session] {
				continue
			}
			covered[d.Session] = true
			it.What, it.Text = "waiting", askText(firstApproval(listed[d.Session]), "")
			it.Nav = code(d.Session)
			it.ID = "delegated:" + d.Session + ":waiting:" + approvalIDs(listed[d.Session])
		case "finished", "stopped":
			if !after(d.Finished, st.Baseline) {
				continue
			}
			it.What, it.Text = "finished", "Delegated work finished"
			if d.State == "stopped" {
				it.What, it.Text = "failed", "Delegated work stopped: "+first(line(d.Error), "an error")
			}
			it.ID = "delegated:" + d.Session + ":" + d.State + ":" + d.Finished
		default:
			continue
		}
		items = append(items, it)
	}

	names := map[string]string{}
	for _, m := range a.Machines {
		if o := jsonv.Obj(m); o != nil {
			names[str(o, "id")] = str(o, "name")
		}
	}
	for _, s := range a.Sessions {
		o := jsonv.Obj(s)
		if o == nil || !Waits(o) || covered[str(o, "id")] {
			continue
		}
		id := str(o, "id")
		text := askText(firstApproval(o), "")
		if n := names[str(o, "machineId")]; n != "" {
			text += " · " + n
		}
		items = append(items, Item{ID: "code:" + id + ":" + approvalIDs(o), Kind: "code", What: "waiting",
			Title: first(str(o, "name"), str(o, "autoName"), line(str(o, "preview")), "pw code session"), Text: text,
			At: first(str(firstApproval(o), "createdAt"), str(o, "updatedAt")), Href: chat.CodePageLink(str(o, "machineId"), id), Session: id, Nav: []string{"code"}})
	}

	for _, t := range src.Transfers(r) {
		items = append(items, Item{ID: "transfer:" + t.ID, Kind: "transfer", What: "failed",
			Title: first(t.Label, t.Source.Label+" to "+t.Destination.Label), Text: "Transfer failed: " + first(line(t.Error), "no reason was given"),
			At: t.EndedAt, Href: "#view=transfers?transfer=" + esc(t.ID), Nav: []string{"transfers"}})
	}
	for _, l := range src.Libraries(r) {
		did := "could not be built"
		if l.Built {
			did = "could not be updated"
		}
		items = append(items, Item{ID: "library:" + l.ID + ":" + sum(l.Error), Kind: "library", What: "failed", Title: first(l.Label, l.ID),
			Text: "The index " + did + ": " + line(l.Error), Href: "#view=settings:libraries"})
	}

	kept := map[string]bool{}
	a.Items = []Item{}
	for _, it := range items {
		if it.At == "" {
			it.At = st.first(it.ID, now)
			kept[it.ID] = true
		}
		if _, gone := st.Dismissed[it.ID]; gone {
			continue
		}
		if it.Nav == nil {
			it.Nav = []string{}
		}
		a.Items = append(a.Items, it)
	}
	st.keepFirst(kept)
	rank := func(it Item) int {
		if it.What == "waiting" {
			return 0
		}
		return 1
	}
	sort.SliceStable(a.Items, func(i, j int) bool {
		x, y := a.Items[i], a.Items[j]
		if rank(x) != rank(y) {
			return rank(x) < rank(y)
		}
		return parse(x.At).After(parse(y.At))
	})
	return a
}

// Waits: a session as pw code lists it waits on its person (waiting.ts's
// waitsOnYou).
func Waits(s *jsonv.Object) bool {
	if str(s, "archivedAt") != "" {
		return false
	}
	return str(s, "status") == "awaiting_approval" || len(approvals(s)) > 0
}

func approvals(s *jsonv.Object) []*jsonv.Object {
	if s == nil {
		return nil
	}
	var out []*jsonv.Object
	for _, x := range jsonv.Arr(jsonv.Path(s, "approvals")) {
		if o := jsonv.Obj(x); o != nil {
			out = append(out, o)
		}
	}
	return out
}

func firstApproval(s *jsonv.Object) *jsonv.Object {
	if as := approvals(s); len(as) > 0 {
		return as[0]
	}
	return nil
}

func approvalIDs(s *jsonv.Object) string {
	var ids []string
	for _, x := range approvals(s) {
		ids = append(ids, str(x, "id"))
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// askText is what a request asks of the person, in a line: pw code's
// approval a, or a call of tool the Studio assistant holds.
func askText(a *jsonv.Object, tool string) string {
	switch str(a, "kind") {
	case "ask":
		if qs := jsonv.Arr(jsonv.Path(a, "questions")); len(qs) > 0 {
			if q := line(str(jsonv.Obj(qs[0]), "question")); q != "" {
				return "Asks: " + q
			}
		}
		return "Asks you a question"
	case "plan":
		return "Has a plan for you to approve"
	}
	what := first(tool, line(str(a, "command")), str(a, "tool"))
	if what == "" {
		return "Waits for your approval"
	}
	return "Waits for your approval: " + what
}

func str(o *jsonv.Object, k string) string {
	if o == nil {
		return ""
	}
	return jsonv.String(jsonv.Path(o, k))
}

func updated(o *jsonv.Object) string { return str(o, "updatedAt") }

func titled(t string) string { return first(line(t), "Untitled chat") }

// line is the first line of s, cut to a readable length.
func line(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if r := []rune(s); len(r) > 200 {
		return string(r[:199]) + "…"
	}
	return s
}

func first(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func contains(vs []string, v string) bool {
	for _, x := range vs {
		if x == v {
			return true
		}
	}
	return false
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:6])
}

func parse(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// after: time a is later than b.
func after(a, b string) bool { return parse(a).After(parse(b)) }

// failed is a library whose build failed, or ok false.
func failed(id, label, state string, err *string) (Library, bool) {
	if err == nil || *err == "" || (state != "failed" && state != "ready") {
		return Library{}, false
	}
	return Library{ID: id, Label: label, Error: *err, Built: state == "ready"}, true
}
