package chat

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// A pw code chat session runs on the model Settings names for pw code or,
// without one, on pw code's own default. That default can be a platform
// model (an id such as "owner:provider/model") the person's AI connection
// does not offer, and then every turn fails with "This model is not
// enabled for the AI connection". So the Studio checks the session's model
// against the models the connection offers, the ones the chat's model
// picker lists, and moves the session to one it offers: the model chosen
// under the message box, else the first one listed. The chat says which.

// platformModel reports whether a model id is one of the platform's: a
// connection's model, "<connection>:<provider>/<model>". pw code's own
// backends (claude-code/haiku, say) are not, and are left alone.
func platformModel(id string) bool {
	colon, slash := strings.Index(id, ":"), strings.Index(id, "/")
	return colon > 0 && slash > colon
}

// pickPWCodeModel is the model a session on current moves to, or "" to
// stay: it moves only from a platform model the connection does not offer
// (enabled, in the picker's order), to chosen when offered, else to the
// first platform model offered.
func pickPWCodeModel(current, chosen string, enabled []string) string {
	if !platformModel(current) || len(enabled) == 0 {
		return ""
	}
	for _, id := range enabled {
		if id == current {
			return ""
		}
	}
	for _, id := range enabled {
		if id == chosen && platformModel(id) {
			return id
		}
	}
	for _, id := range enabled {
		if platformModel(id) {
			return id
		}
	}
	return ""
}

// enabledModels is the models the viewer's AI connection offers, as the
// chat's picker lists them, less those whose call failed in the last ten
// minutes; ok is false when the list cannot be read, and then nothing is
// changed.
func (m *Module) enabledModels(r *http.Request) ([]string, bool) {
	cred := m.cred(r)
	if m.GatewayKey() == "" && cred == nil && !m.LLM.SidecarConfigured() {
		return nil, false
	}
	viewer := ""
	if u := user(r); u != nil {
		viewer = strings.ToLower(u.Username)
	}
	key := m.listingKey(cred, viewer)
	c := m.listings
	c.mu.Lock()
	hit, ok := c.hits[key]
	c.mu.Unlock()
	l := hit.v
	if !ok || time.Since(hit.at) >= listingKeep {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
		defer cancel()
		v, err := m.computeListing(ctx, cred, r)
		if err != nil || v == nil {
			return nil, false
		}
		c.mu.Lock()
		c.hits[key] = listingHit{time.Now(), v}
		c.mu.Unlock()
		l = v
	}
	out := []string{}
	for _, mo := range l.models {
		id := objStr(mo, "id")
		if id == "" {
			continue
		}
		if f, has := m.LLM.ModelFailure(id); has {
			if t, ok := parseDate(f.At); ok && time.Since(t) < 10*time.Minute {
				continue
			}
		}
		out = append(out, id)
	}
	return out, true
}

// pwcodeModel moves a chat's session off a platform model the viewer's
// AI connection does not offer, and notes on the link which model the chat
// runs on and why. A model Settings names, and a session continued from
// the Code page (the person's own), are kept as they are.
func (m *Module) pwcodeModel(ctx context.Context, r *http.Request, t *pwTarget, link *conversations.PWCodeLink) {
	if link == nil || link.Continued || link.Session == "" || m.eff().PWCodeModel != "" || !platformModel(link.Model) {
		return
	}
	enabled, ok := m.enabledModels(r)
	if !ok {
		return
	}
	pick := pickPWCodeModel(link.Model, t.chatModel, enabled)
	if pick == "" {
		return
	}
	if _, err := pwcode.SetSessionModel(ctx, t.client, link.Session, pick); err != nil {
		m.A.Log.Warn("pw code chat: the session's model could not be changed", "session", link.Session, "from", link.Model, "to", pick, "err", err)
		return
	}
	why := "the first model it offers"
	if pick == t.chatModel {
		why = "the model chosen for the Studio assistant"
	}
	link.ModelNote = "pw code's own model, " + link.Model + ", is not enabled for your AI connection, so this chat runs on " + pick + ", " + why + ". Settings, External access, pw code model names another."
	link.Model = pick
	t.modelMoved = true
}
