package chat

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// A chat's first message goes through the Studio, which makes its session.
// Later ones go to the session from its own view, where a line such as
// "/plan ..." is a command: sent as a message, the daemon refuses it. So
// the Studio only makes the session for one, and the page runs it there.

// slashLine is a line pw code reads as a command or a skill.
var slashLine = regexp.MustCompile(`^/[A-Za-z0-9_:-]+(\s|$)`)

// handFirst leaves a chat's first message to the page: to run in the
// session's view, or, refused, to put back in its box with why.
func (m *Module) handFirst(res *sseWriter, convID, msgID, text string, refused error) {
	m.Conv.DropUserTurn(convID, msgID)
	why := ""
	if refused != nil {
		_, why = problemOf(refused)
	}
	res.event("pwcode_first", reqbody.O("text", text, "refused", why))
	res.event("done", reqbody.O("finishReason", "stop", "model", nil))
}

// owedTurns is what a session a command is run in is owed, where the chat
// has turns it has not seen (carried).
func owedTurns(c *conversations.Conversation, fresh bool, carried, current string) *conversations.PWCodeOwed {
	if carried == "" {
		return nil
	}
	o := &conversations.PWCodeOwed{After: contextAfter(c, fresh)}
	for _, mm := range c.Messages {
		if mm.ID != current {
			o.Upto = mm.ID
		}
	}
	return o
}

// owedContext is the turns the session is owed, which go before text, the
// first message sent to it from its view. A line pw code reads as a skill
// is sent as it is.
func owedContext(c *conversations.Conversation, text string) string {
	text = strings.TrimSpace(text)
	if c.PWCode == nil || c.PWCode.Owed == nil || text == "" || slashLine.MatchString(text) {
		return ""
	}
	return contextBetween(c, c.PWCode.Owed.After, c.PWCode.Owed.Upto, "")
}

// untoldScope is the scope a session has not been told of, with the first
// message sent to it from its view: one a command started, whose chat has
// a scope. A line pw code reads as a skill is sent as it is.
func untoldScope(c *conversations.Conversation, t *pwTarget, text string) string {
	l := c.PWCode
	if c.ID == "" || t.relay || l.Continued || l.ScopeNote != "" || strings.TrimSpace(text) == "" || slashLine.MatchString(strings.TrimSpace(text)) {
		return ""
	}
	return scopeSaid(l.Library, l.LabelScope)
}

// pwNeedsAt is whether a viewer's pw code needed a model, when last asked.
type pwNeedsAt struct {
	needs bool
	at    time.Time
}

// How long the daemon's word on its own model is kept: a person without
// one may set it any time, in a terminal too.
const (
	needsModelKept = 15 * time.Second
	hasModelKept   = 10 * time.Minute
)

// probeEffort is an effort no pw code has.
const probeEffort = "studio-asks-for-the-model"

// pwcodeNeedsModel is whether pw code, on the machine the Studio drives for
// the viewer, has no model to start a session on: Settings names none, and
// the person has no default. No route reads the default, so a session is
// asked for with an effort there is not: the daemon refuses it, for want of
// a model where there is none, which it checks first.
func (m *Module) pwcodeNeedsModel(r *http.Request) bool {
	if m.eff().PWCodeModel != "" {
		return false
	}
	ctx, who := r.Context(), userID(r)
	m.pw.mu.Lock()
	had, ok := m.pw.needs[who]
	m.pw.mu.Unlock()
	if ok && time.Since(had.at) < hasModelKept && (!had.needs || time.Since(had.at) < needsModelKept) {
		return had.needs
	}
	h, err := m.pwcodeHereCached(ctx, r)
	if err != nil {
		return false
	}
	v, err := h.client.Call(auth.ContextWithUser(ctx, user(r)), http.MethodPost, "/v1/sessions", jsonv.NewObject("workspace", "/", "effort", probeEffort), 0)
	if err == nil {
		if id := jsonv.String(jsonv.Path(v, "id")); id != "" {
			_, _ = h.client.Call(ctx, http.MethodDelete, "/v1/sessions/"+id, nil, 0)
		}
	}
	needs := err != nil && err.Error() == pwcode.NoModel
	m.pw.mu.Lock()
	if m.pw.needs == nil {
		m.pw.needs = map[string]pwNeedsAt{}
	}
	m.pw.needs[who] = pwNeedsAt{needs, time.Now()}
	m.pw.mu.Unlock()
	return needs
}
