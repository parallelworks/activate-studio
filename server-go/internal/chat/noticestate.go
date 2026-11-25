package chat

import (
	"net/http"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// What the Notices page and the chat list's marks read of the person's
// chats: which are being answered, which wait on them, and the last reply
// of each. The server says it, so a chat that is not on screen, and one
// whose session no platform lists, has the same state everywhere.

// ChatState is one of the person's own chats.
type ChatState struct {
	ID, Title, UpdatedAt string
	// Session is the pw code session it runs in, "" for none, and Machine
	// that session's machine where the chat knows it.
	Session, Machine string
	// Local: the session is on this computer's pw code daemon.
	Local bool
	// Working: a reply is being written.
	Working bool
	// Asks is what it waits on the person for: the approvals a pw code
	// turn followed here has asked for, or the calls the Studio assistant
	// holds.
	Asks []ChatAsk
	// Reply is its last reply or note, with no ID where it has none.
	Reply ChatReply
}

// ChatAsk is one request that waits on the person. Approval is pw code's
// own record of it, nil for a call the Studio assistant holds (Tool).
type ChatAsk struct {
	ID, At, Tool string
	Approval     *jsonv.Object
}

// ChatReply is a chat's last reply. Note: a note the Studio wrote (a run
// or a transfer that ended) rather than an answer.
type ChatReply struct {
	ID, At string
	Note   bool
}

// ask and answered keep the turn's open approvals.
func (t *pwTurn) ask(a *jsonv.Object) {
	id := jsonv.String(jsonv.Path(a, "id"))
	t.m.pw.mu.Lock()
	defer t.m.pw.mu.Unlock()
	for _, x := range t.asks {
		if jsonv.String(jsonv.Path(x, "id")) == id {
			return
		}
	}
	t.asks = append(t.asks, a)
}

func (t *pwTurn) answered(id string) {
	t.m.pw.mu.Lock()
	defer t.m.pw.mu.Unlock()
	var kept []*jsonv.Object
	for _, x := range t.asks {
		if jsonv.String(jsonv.Path(x, "id")) != id {
			kept = append(kept, x)
		}
	}
	t.asks = kept
}

// isEngineNote is a note that says which engine answered: it comes with a
// reply, and is not news of its own.
func isEngineNote(id string) bool { return len(id) > 12 && id[:12] == "note-engine-" }

// ChatStates is the state of the chats of the person r is from: their own
// only, whoever else's they may read.
func (m *Module) ChatStates(r *http.Request) []ChatState {
	me := ""
	if u := user(r); u != nil {
		me = u.Username
	}
	auth := m.A.Cfg.AuthEnabled()
	if auth && me == "" {
		return nil
	}
	now := m.held.now()
	type turn struct{ asks []ChatAsk }
	turns := map[string]turn{}
	m.pw.mu.Lock()
	for id, t := range m.pw.turns {
		var x turn
		for _, a := range t.asks {
			x.asks = append(x.asks, ChatAsk{ID: jsonv.String(jsonv.Path(a, "id")), At: jsonv.String(jsonv.Path(a, "createdAt")), Approval: a})
		}
		turns[id] = x
	}
	m.pw.mu.Unlock()
	held := m.held.waiting(now)
	out := []ChatState{}
	for _, c := range m.Conv.List() {
		if auth && c.OwnerName() != me {
			continue
		}
		s := ChatState{ID: c.ID, UpdatedAt: c.UpdatedAt}
		if c.Title != nil {
			s.Title = *c.Title
		}
		if c.PWCode != nil {
			s.Session, s.Machine = c.PWCode.Session, c.PWCode.Machine
			s.Local = !c.PWCode.Relay && c.PWCode.Site == agentsites.Local
		}
		t, following := turns[c.ID]
		s.Working = following || m.Conv.Answering(c.ID)
		s.Asks = append(t.asks, held[c.ID]...)
		s.Reply = lastReply(c)
		out = append(out, s)
	}
	return out
}

func lastReply(c *conversations.Conversation) ChatReply {
	for i := len(c.Messages) - 1; i >= 0; i-- {
		x := c.Messages[i]
		if x.Role != "assistant" || isEngineNote(x.ID) {
			continue
		}
		return ChatReply{ID: x.ID, At: x.Timestamp, Note: len(x.ID) > 5 && x.ID[:5] == "note-"}
	}
	return ChatReply{}
}

// waiting is the held calls that can still be answered, by conversation.
func (s *heldStore) waiting(now time.Time) map[string][]ChatAsk {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	out := map[string][]ChatAsk{}
	for _, r := range s.recs {
		if t, ok := parseISO(r.ExpiresAt); ok && !t.After(now) {
			continue
		}
		out[r.ConversationID] = append(out[r.ConversationID], ChatAsk{ID: r.ToolCallID, At: r.HeldAt, Tool: r.Tool})
	}
	return out
}

// LocalSessions is the sessions of this computer's pw code that the
// person's chats run in, of those ids: the ones a platform does not list,
// whose state is otherwise not known. With sign-in each person's pw code
// runs on their own account, a connection away, and the platform's listing
// is what is read of it.
func (m *Module) LocalSessions(r *http.Request, ids map[string]bool) map[string]*jsonv.Object {
	out := map[string]*jsonv.Object{}
	s := pwSites()
	if len(ids) == 0 || m.A.Cfg.AuthEnabled() || s == nil || m.localPWCodeRefused() != nil {
		return out
	}
	list, err := pwcode.ListSessions(r.Context(), s.Daemon.Local())
	if err != nil {
		return out
	}
	rows := jsonv.Arr(list)
	if rows == nil {
		rows = jsonv.Arr(jsonv.Path(list, "sessions"))
	}
	for _, x := range rows {
		if o := jsonv.Obj(x); o != nil && ids[jsonv.String(jsonv.Path(o, "id"))] {
			o.Set("machineId", directMachine)
			out[jsonv.String(jsonv.Path(o, "id"))] = o
		}
	}
	return out
}
