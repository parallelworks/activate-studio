package notices

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/chat"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/delegation"
	"github.com/parallelworks/activate-studio/server-go/internal/fleet"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/transfers"
	"github.com/parallelworks/activate-studio/server-go/internal/userlibs"
)

// person is what the sources hold of one person.
type person struct {
	machines, sessions []any
	listErr            error
	chats              []chat.ChatState
	local              map[string]*jsonv.Object
	agents             []fleet.Agent
	work               []delegation.Record
	fails              []transfers.Transfer
	libs               []Library
	shares             []userlibs.SharedNew
	looking            bool
}

type studio struct {
	t       *testing.T
	dir     string
	authOn  bool
	people  map[string]*person
	now     time.Time
	srv     *httptest.Server
	seenXfr []string
	asked   []map[string]bool
}

func (s *studio) of(r *http.Request) *person {
	id := ""
	if u := auth.UserOf(r); u != nil {
		id = u.ID
	}
	if p := s.people[id]; p != nil {
		return p
	}
	return &person{}
}

// start serves the notices, as a Studio that has just started does.
func (s *studio) start() {
	if s.srv != nil {
		s.srv.Close()
	}
	svc := New(s.dir, func() bool { return s.authOn }, Sources{
		Listing: func(r *http.Request) ([]any, []any, error) {
			p := s.of(r)
			return p.machines, p.sessions, p.listErr
		},
		Chats: func(r *http.Request) []chat.ChatState { return s.of(r).chats },
		Local: func(r *http.Request, ids map[string]bool) map[string]*jsonv.Object {
			s.asked = append(s.asked, ids)
			return s.of(r).local
		},
		Fleet:     func(r *http.Request) []fleet.Agent { return s.of(r).agents },
		Delegated: func(r *http.Request) []delegation.Record { return s.of(r).work },
		Transfers: func(r *http.Request) []transfers.Transfer {
			out := []transfers.Transfer{}
			for _, x := range s.of(r).fails {
				if !contains(s.seenXfr, x.ID) {
					out = append(out, x)
				}
			}
			return out
		},
		Summary: func(r *http.Request) *transfers.Summary {
			return &transfers.Summary{FailedLast24h: len(s.of(r).fails)}
		},
		TransferSeen: func(r *http.Request, id string) { s.seenXfr = append(s.seenXfr, id) },
		Libraries:    func(r *http.Request) []Library { return s.of(r).libs },
		Shares: func(r *http.Request) ([]userlibs.SharedNew, bool) {
			return s.of(r).shares, s.of(r).looking
		},
	})
	svc.now = func() time.Time { return s.now }
	mux := http.NewServeMux()
	svc.Routes(mux, &app.App{Cfg: config.Config{}, Log: slog.New(slog.DiscardHandler)})
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := r.Header.Get("as"); id != "" {
			r = auth.WithUser(r, &auth.User{ID: id, Username: id})
		}
		mux.ServeHTTP(w, r)
	}))
	s.t.Cleanup(s.srv.Close)
}

func newStudio(t *testing.T, authOn bool) *studio {
	s := &studio{t: t, dir: t.TempDir(), authOn: authOn, people: map[string]*person{}, now: at(0)}
	s.start()
	return s
}

func at(min int) time.Time {
	return time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC).Add(time.Duration(min) * time.Minute)
}

func (s *studio) ask(as, method, path string, in any) (int, Answer) {
	s.t.Helper()
	var rd bytes.Buffer
	if in != nil {
		json.NewEncoder(&rd).Encode(in)
	}
	req, _ := http.NewRequest(method, s.srv.URL+path, &rd)
	if as != "" {
		req.Header.Set("as", as)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer res.Body.Close()
	var out Answer
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func (s *studio) read(as string) Answer {
	s.t.Helper()
	code, a := s.ask(as, "GET", "/api/notices", nil)
	if code != 200 {
		s.t.Fatalf("notices for %q: %d", as, code)
	}
	return a
}

// ids is the notices as "kind what title [nav]".
func ids(a Answer) string {
	var out []string
	for _, i := range a.Items {
		out = append(out, i.Kind+" "+i.What+" "+i.Title+" ["+strings.Join(i.Nav, " ")+"]")
	}
	return strings.Join(out, "; ")
}

func session(id, status string, approvals ...any) *jsonv.Object {
	o := jsonv.NewObject("id", id, "machineId", "m1", "name", "work "+id, "status", status, "updatedAt", iso(at(-5)))
	if len(approvals) > 0 {
		o.Set("approvals", approvals)
	}
	return o
}

func approval(id, kind string) *jsonv.Object {
	return jsonv.NewObject("id", id, "kind", kind, "tool", "bash", "command", "make test\nmore", "createdAt", iso(at(-1)))
}

func chatOf(id, reply string, when time.Time) chat.ChatState {
	return chat.ChatState{ID: id, Title: "Chat " + id, UpdatedAt: iso(when), Reply: chat.ChatReply{ID: reply, At: iso(when)}}
}

func TestAReplyIsNewsUntilItIsSeen(t *testing.T) {
	s := newStudio(t, false)
	p := &person{chats: []chat.ChatState{chatOf("c1", "r1", at(-60))}}
	s.people[""] = p
	// What was there before is not news.
	if a := s.read(""); len(a.Items) != 0 || a.Chats["c1"].Unread != "" {
		t.Fatalf("at first: %s %+v", ids(a), a.Chats)
	}
	// A reply being written is not either; once written, it is.
	s.now = at(3)
	p.chats[0] = chatOf("c1", "r2", at(2))
	p.chats[0].Working = true
	if a := s.read(""); len(a.Items) != 0 || !a.Chats["c1"].Working {
		t.Errorf("while it is written: %s", ids(a))
	}
	p.chats[0].Working = false
	a := s.read("")
	if ids(a) != "chat reply Chat c1 [chat]" || a.Items[0].Href != "#chat=c1" || a.Items[0].At != iso(at(2)) || a.Chats["c1"].Unread != "reply" {
		t.Errorf("written: %s %+v", ids(a), a.Chats)
	}
	// Seeing it answers with the notices as they are, and lasts a restart.
	if _, a := s.ask("", "POST", "/api/notices/seen", map[string]any{"conversations": []string{"c1"}}); len(a.Items) != 0 {
		t.Errorf("seen: %s", ids(a))
	}
	s.start()
	if a := s.read(""); len(a.Items) != 0 || a.Chats["c1"].Unread != "" {
		t.Errorf("after a restart: %s", ids(a))
	}
	// The next is news again, and so is a note the Studio writes.
	p.chats[0] = chatOf("c1", "note-x", at(6))
	p.chats[0].Reply.Note = true
	if a := s.read(""); ids(a) != "chat note Chat c1 [chat]" || a.Items[0].Text != "New note" {
		t.Errorf("a note: %s", ids(a))
	}
	// The last reply deleted leaves one that was seen.
	p.chats[0] = chatOf("c1", "r1", at(-60))
	if a := s.read(""); len(a.Items) != 0 {
		t.Errorf("an older reply: %s", ids(a))
	}
}

func TestEachPersonSeesTheirOwn(t *testing.T) {
	s := newStudio(t, true)
	if code, _ := s.ask("", "GET", "/api/notices", nil); code != 401 {
		t.Errorf("not signed in: %d", code)
	}
	s.people["ana"] = &person{chats: []chat.ChatState{chatOf("c1", "r1", at(-60))}}
	s.people["ben"] = &person{chats: []chat.ChatState{chatOf("c2", "r2", at(-60))}}
	s.read("ana")
	s.read("ben")
	s.now = at(5)
	s.people["ana"].chats[0] = chatOf("c1", "r3", at(4))
	s.people["ben"].chats[0] = chatOf("c2", "r4", at(4))
	if a := s.read("ana"); ids(a) != "chat reply Chat c1 [chat]" {
		t.Errorf("ana: %s", ids(a))
	}
	// One person cannot mark or dismiss what is another's.
	s.ask("ben", "POST", "/api/notices/seen", map[string]any{"conversations": []string{"c1"}})
	s.ask("ben", "POST", "/api/notices/dismiss", map[string]any{"ids": []string{"reply:c1:r3"}})
	if a := s.read("ana"); ids(a) != "chat reply Chat c1 [chat]" {
		t.Errorf("ana, after ben: %s", ids(a))
	}
	if a := s.read("ben"); ids(a) != "chat reply Chat c2 [chat]" {
		t.Errorf("ben: %s", ids(a))
	}
}

func TestWhatWaitsIsListedOnce(t *testing.T) {
	s := newStudio(t, false)
	p := &person{
		machines: []any{jsonv.NewObject("id", "m1", "name", "example-box")},
		sessions: []any{
			session("s1", "awaiting_approval", approval("ap1", "confirm")),
			session("s2", "awaiting_approval", approval("ap2", "ask")),
			session("s3", "awaiting_approval", approval("ap3", "plan")),
			session("s4", "awaiting_approval", approval("ap4", "confirm")),
			session("s5", "running"),
		},
		agents: []fleet.Agent{{ID: "a1", Name: "Reporter", State: "input-required", Note: "Which table?", Session: "s2"}, {ID: "a2", State: "idle"}},
		work:   []delegation.Record{{Session: "s3", Objective: "Draft the report", State: "waiting"}},
	}
	c1, c5 := chatOf("c1", "r1", at(-60)), chatOf("c5", "r5", at(-60))
	c1.Session, c5.Session = "s1", "s5"
	p.chats = []chat.ChatState{c1, c5}
	s.people[""] = p
	a := s.read("")
	// The chat, the agent and the delegated work each have their own
	// session's; only s4 is a Code page session with nothing else to name.
	// Every one the Code page lists as waiting counts on Code.
	got := map[string]bool{}
	code := 0
	for _, i := range a.Items {
		got[i.Kind+" "+i.Session] = true
		if contains(i.Nav, "code") {
			code++
		}
	}
	if len(a.Items) != 4 || !got["chat s1"] || !got["fleet s2"] || !got["delegated s3"] || !got["code s4"] || code != 4 {
		t.Errorf("items: %s", ids(a))
	}
	for _, i := range a.Items {
		if i.Kind == "chat" && i.Text != "Waits for your approval: make test" || i.Kind == "code" && i.Text != "Waits for your approval: make test · example-box" ||
			i.Kind == "delegated" && i.Text != "Has a plan for you to approve" || i.Kind == "fleet" && (i.Text != "Which table?" || i.At != iso(at(0))) {
			t.Errorf("%s: %q at %s", i.Kind, i.Text, i.At)
		}
	}
	if !a.Listed || len(a.Sessions) != 5 || !a.Chats["c1"].Waiting || !a.Chats["c5"].Working {
		t.Errorf("listing %v %d, chats %+v", a.Listed, len(a.Sessions), a.Chats)
	}

	// Dismissed, it stays away until it asks something else.
	_, a = s.ask("", "POST", "/api/notices/dismiss", map[string]any{"ids": []string{"chat:c1:ap1", "code:s4:ap4"}})
	if strings.Contains(ids(a), "Chat c1") || strings.Contains(ids(a), "work s4") || len(a.Items) != 2 || !a.Chats["c1"].Waiting {
		t.Errorf("dismissed: %s", ids(a))
	}
	p.sessions[0] = session("s1", "awaiting_approval", approval("ap9", "confirm"))
	if a := s.read(""); !strings.Contains(ids(a), "chat waiting Chat c1 [chat code]") {
		t.Errorf("asked again: %s", ids(a))
	}
	// Answered, it is gone.
	p.sessions[0] = session("s1", "idle")
	if a := s.read(""); strings.Contains(ids(a), "Chat c1") || a.Chats["c1"].Waiting {
		t.Errorf("answered: %s", ids(a))
	}
}

func TestAChatWaitsWithoutAPlatformListing(t *testing.T) {
	s := newStudio(t, false)
	c1, c2, c3 := chatOf("c1", "r1", at(-60)), chatOf("c2", "r2", at(-60)), chatOf("c3", "r3", at(-60))
	// A turn the Studio follows asked, a call the Studio assistant holds,
	// and a session only this computer's daemon knows.
	c1.Session, c1.Asks = "s1", []chat.ChatAsk{{ID: "ap1", At: iso(at(-1)), Approval: approval("ap1", "ask")}}
	c2.Asks = []chat.ChatAsk{{ID: "call1", At: iso(at(-2)), Tool: "write_kb_file"}}
	c3.Session, c3.Local, c3.Machine, c3.MachineName = "s3", true, "d3", "this-host"
	s.people[""] = &person{listErr: errors.New("no sign-in"), chats: []chat.ChatState{c1, c2, c3},
		local: map[string]*jsonv.Object{"s3": session("s3", "awaiting_approval", approval("ap3", "confirm"))}}
	a := s.read("")
	if ids(a) != "chat waiting Chat c1 [chat]; chat waiting Chat c3 [chat]; chat waiting Chat c2 [chat]" || a.Listed || a.Items[0].Text != "Asks you a question" || a.Items[2].Text != "Waits for your approval: write_kb_file" {
		t.Errorf("items: %s", ids(a))
	}
	if a.Chats["c3"].Row == nil || len(s.asked) != 1 || !s.asked[0]["s3"] || len(s.asked[0]) != 1 {
		t.Errorf("local rows: %+v, asked %v", a.Chats["c3"], s.asked)
	}
	// A row says which machine its session is on, as the chat knows it.
	if c := a.Chats["c3"]; !c.Local || c.Machine != "d3" || c.MachineName != "this-host" || a.Chats["c2"].MachineName != "" {
		t.Errorf("machines: %+v", c)
	}
	// Where the listing is from before the ask, its session says so too.
	p := s.people[""]
	p.listErr, p.sessions = nil, []any{session("s1", "running")}
	a = s.read("")
	if a.Items[0].Nav[1] != "code" || jsonv.String(jsonv.Path(jsonv.Obj(p.sessions[0]), "status")) != "awaiting_approval" {
		t.Errorf("listed before the ask: %s", ids(a))
	}
}

func TestWhatEndedOrFailedIsNews(t *testing.T) {
	s := newStudio(t, false)
	p := &person{}
	s.people[""] = p
	s.read("")
	s.now = at(10)
	p.work = []delegation.Record{
		{Session: "d0", Objective: "Old work", State: "finished", Finished: iso(at(-30))},
		{Session: "d1", Objective: "Draft the report\nin detail", State: "finished", Finished: iso(at(3))},
		{Session: "d2", Objective: "Run the sweep", State: "stopped", Finished: iso(at(4)), Error: "no model"},
		{Session: "d3", Objective: "Busy", State: "working"},
	}
	p.fails = []transfers.Transfer{{ID: "t1", Label: "Nightly copy", State: "failed", Error: "connection refused", EndedAt: iso(at(5))}}
	p.libs = []Library{{ID: "l1", Label: "Notes", Error: "no space left"}}
	a := s.read("")
	if a.Transfers == nil || a.Transfers.FailedLast24h != 1 {
		t.Errorf("the Transfers entry's summary: %v", a.Transfers)
	}
	if ids(a) != "library failed Notes []; transfer failed Nightly copy [transfers]; delegated failed Run the sweep []; delegated finished Draft the report []" {
		t.Fatalf("items: %s", ids(a))
	}
	// The library's has no time of its own: it is when it was first listed.
	s.now = at(20)
	if a := s.read(""); a.Items[0].At != iso(at(10)) {
		t.Errorf("library at %s", a.Items[0].At)
	}
	// Opening the session is dealing with its end; a dismissed transfer is
	// a seen one, so Transfers' own mark goes.
	if _, a := s.ask("", "POST", "/api/notices/seen", map[string]any{"session": "d1"}); strings.Contains(ids(a), "Draft") {
		t.Errorf("opened: %s", ids(a))
	}
	_, a = s.ask("", "POST", "/api/notices/dismiss", map[string]any{"ids": []string{"transfer:t1", a.Items[0].ID}})
	if ids(a) != "delegated failed Run the sweep []" || len(s.seenXfr) != 1 {
		t.Errorf("dismissed: %s, seen %v", ids(a), s.seenXfr)
	}
	// A new error of the same library is a new notice.
	p.libs[0].Error = "permission denied"
	if a := s.read(""); !strings.Contains(ids(a), "library failed Notes") {
		t.Errorf("another error: %s", ids(a))
	}
	if code, _ := s.ask("", "POST", "/api/notices/dismiss", map[string]any{"ids": []string{}}); code != 400 {
		t.Errorf("nothing to dismiss: %d", code)
	}
}

// TestAFolderSharedIsANotice: one for each folder shared with the person or
// a group of theirs, with what Add to my libraries needs, whenever it was
// shared, until it is taken back, added or dismissed. Given more access, it
// is another.
func TestAFolderSharedIsANotice(t *testing.T) {
	s := newStudio(t, true)
	bob := &person{looking: true}
	s.people["bob"] = bob
	if a := s.read("bob"); len(a.Items) != 0 || !a.Looking {
		t.Fatalf("while it is being asked: %s, looking %v", ids(a), a.Looking)
	}
	proj := userlibs.SharedNew{ID: "s-1", From: "ada", Label: "proj", Folder: "/home/ada/studio/proj", Site: "pw://ada/example-box", System: "example-box", Access: "read", At: iso(at(-9000))}
	team := userlibs.SharedNew{ID: "s-2", From: "ada", Label: "runs", Folder: "/home/ada/studio/runs", Site: "cluster-a", System: "cluster-a", Group: "team", Access: "write", At: iso(at(-60))}
	bob.looking, bob.shares = false, []userlibs.SharedNew{proj, team}
	a := s.read("bob")
	if ids(a) != "share shared ada shared runs with your group team []; share shared ada shared proj with you []" || a.Looking {
		t.Fatalf("items: %s", ids(a))
	}
	if got := a.Items[0].Text; got != "Read and write · /home/ada/studio/runs · cluster-a" {
		t.Errorf("text: %s", got)
	}
	want := Share{ID: "s-1", From: "ada", Label: "proj", Folder: "/home/ada/studio/proj", Site: "pw://ada/example-box", System: "example-box", Access: "read"}
	if a.Items[1].Share == nil || *a.Items[1].Share != want {
		t.Errorf("share: %+v", a.Items[1].Share)
	}
	for _, who := range []string{"ada", "eve"} {
		if a := s.read(who); len(a.Items) != 0 {
			t.Errorf("%s sees %s", who, ids(a))
		}
	}
	// Dismissed, it stays so, also long after; with more access it is another.
	s.ask("bob", "POST", "/api/notices/dismiss", map[string]any{"ids": []string{a.Items[1].ID}})
	s.now = at(200 * 24 * 60)
	s.ask("bob", "POST", "/api/notices/dismiss", map[string]any{"ids": []string{"other"}})
	s.start()
	if a := s.read("bob"); ids(a) != "share shared ada shared runs with your group team []" {
		t.Errorf("dismissed: %s", ids(a))
	}
	bob.shares[0].Access = "write"
	if a := s.read("bob"); !strings.Contains(ids(a), "proj") {
		t.Errorf("more access: %s", ids(a))
	}
	bob.shares = nil
	if a := s.read("bob"); len(a.Items) != 0 {
		t.Errorf("taken back: %s", ids(a))
	}
}
