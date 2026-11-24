package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// replyLimit is how long a Studio assistant reply may run once the page
// that asked for it has gone.
const replyLimit = 2 * time.Hour

// liveTurn is a Studio assistant reply running in a conversation. It runs
// on past the request that asked for it, so a closed tab or a reload does
// not end it; a page that opens the conversation follows what it has
// written so far (GET .../generating), and Stop ends it (POST .../stop).
type liveTurn struct {
	id     string
	convID string
	// who started it: only they stop it ("" when the Studio has no sign-in).
	who    string
	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.Mutex
	content   strings.Builder
	reasoning strings.Builder
}

// record keeps what the reply has shown, from its stream's events.
func (t *liveTurn) record(name string, data []byte) {
	if name != "content" && name != "reasoning" {
		return
	}
	var v struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(data, &v) != nil || v.Text == "" {
		return
	}
	t.mu.Lock()
	if name == "content" {
		t.content.WriteString(v.Text)
	} else {
		t.reasoning.WriteString(v.Text)
	}
	t.mu.Unlock()
}

// generating is the reply so far, as the chat package watches it.
func (t *liveTurn) generating() any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return reqbody.O("messageId", t.id, "content", t.content.String(), "reasoning", t.reasoning.String())
}

type liveTurns struct {
	mu    sync.Mutex
	turns map[string]*liveTurn
}

func (l *liveTurns) begin(convID, who string, cancel context.CancelFunc) *liveTurn {
	t := &liveTurn{id: "live-" + strconv.FormatInt(time.Now().UnixNano(), 36), convID: convID, who: who, cancel: cancel, done: make(chan struct{})}
	l.mu.Lock()
	if l.turns == nil {
		l.turns = map[string]*liveTurn{}
	}
	l.turns[convID] = t
	l.mu.Unlock()
	return t
}

func (l *liveTurns) end(t *liveTurn) {
	l.mu.Lock()
	if l.turns[t.convID] == t {
		delete(l.turns, t.convID)
	}
	l.mu.Unlock()
}

func (l *liveTurns) get(convID string) *liveTurn {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.turns[convID]
}

// answerOnPast writes the Studio assistant's reply in a conversation so
// that it runs to its end when the page that asked for it goes: the
// request writes the reply's events while it lasts, and what the reply
// shows is kept for a page that opens the conversation meanwhile. A
// reply outside a conversation has nowhere to be stored or followed, and
// ends with its request.
func (m *Module) answerOnPast(r *http.Request, res *sseWriter, body *streamBody, convID, model string) error {
	if convID == "" {
		return m.answer(r, res, *body, convID, model)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), replyLimit)
	t := m.live.begin(convID, userID(r), cancel)
	res.setTap(t.record)
	rr := r.WithContext(ctx)
	go func() {
		defer close(t.done)
		defer m.live.end(t)
		defer cancel()
		defer m.A.Stream()()
		defer m.Conv.BeginAnswer(convID)()
		defer m.notes.begin(convID)()
		if err := m.answer(rr, res, *body, convID, model); err != nil {
			ref := shortID()
			m.A.Log.Error("chat stream failed", "err", err.Error(), "errorId", ref)
			res.event("error", reqbody.O("message", fmt.Sprintf("The reply failed with an internal error (ref %s).", ref), "kind", "error"))
		}
	}()
	select {
	case <-t.done:
	case <-r.Context().Done():
		m.A.Log.Info("chat reply runs on without its page", "conversation", convID)
	}
	return nil
}

// liveRoutes: the reply running in a conversation, and Stop.
func (m *Module) liveRoutes(h func(pattern string, fn func(http.ResponseWriter, *http.Request) error)) {
	h("GET /api/chat/conversations/{id}/generating", func(w http.ResponseWriter, r *http.Request) error {
		c, err := m.visibleChat(r, r.PathValue("id"))
		if err != nil {
			return err
		}
		if t := m.live.get(c.ID); t != nil {
			return reqbody.OK(w, t.generating())
		}
		return reqbody.OK(w, nil)
	})
	h("POST /api/chat/conversations/{id}/stop", func(w http.ResponseWriter, r *http.Request) error {
		c, err := m.visibleChat(r, r.PathValue("id"))
		if err != nil {
			return err
		}
		t := m.live.get(c.ID)
		if t == nil {
			// A pw code turn is stopped by its interrupt, which the page
			// sends beside this; the page reads the chat again once this
			// answers, so it waits a little for the turn to be stored.
			m.pw.mu.Lock()
			pt := m.pw.turns[c.ID]
			m.pw.mu.Unlock()
			if pt != nil {
				select {
				case <-pt.done:
				case <-time.After(10 * time.Second):
				}
			}
			return reqbody.OK(w, reqbody.O("stopped", false))
		}
		if t.who != "" && t.who != userID(r) {
			return apierr.New(403, "only the person who asked for this reply can stop it")
		}
		t.cancel()
		select {
		case <-t.done:
		case <-time.After(10 * time.Second):
		}
		return reqbody.OK(w, reqbody.O("stopped", true))
	})
}
