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
// (enabled, in the picker's order) or whose call failed for this person in
// the last ten minutes (failing), to chosen when offered and not failing,
// else to the first such platform model.
func pickPWCodeModel(current, chosen string, enabled []string, failing map[string]bool) string {
	if !platformModel(current) || len(enabled) == 0 {
		return ""
	}
	if listed(current, enabled) && !failing[current] {
		return ""
	}
	usable := func(id string) bool { return platformModel(id) && !failing[id] && id != current }
	if listed(chosen, enabled) && usable(chosen) {
		return chosen
	}
	for _, id := range enabled {
		if usable(id) {
			return id
		}
	}
	return ""
}

func listed(id string, enabled []string) bool {
	for _, e := range enabled {
		if e == id {
			return true
		}
	}
	return false
}

// enabledModels is the models the viewer's AI connection offers, as the
// chat's picker lists them, and those of them whose call failed for this
// person (with the key they call with) in the last ten minutes; ok is
// false when the list cannot be read, and then nothing is changed.
func (m *Module) enabledModels(r *http.Request) (enabled []string, failing map[string]bool, ok bool) {
	cred := m.cred(r)
	if m.deploymentModelsKey() == "" && cred == nil && !m.LLM.SidecarConfigured() || m.personalRequired(cred) {
		return nil, nil, false
	}
	viewer := ""
	if u := user(r); u != nil {
		viewer = strings.ToLower(u.Username)
	}
	key := m.listingKey(cred, viewer)
	c := m.listings
	c.mu.Lock()
	hit, has := c.hits[key]
	c.mu.Unlock()
	l := hit.v
	if !has || time.Since(hit.at) >= listingKeep {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
		defer cancel()
		v, err := m.computeListing(ctx, cred, r)
		if err != nil || v == nil {
			return nil, nil, false
		}
		c.mu.Lock()
		c.hits[key] = listingHit{time.Now(), v}
		c.mu.Unlock()
		l = v
	}
	enabled, failing = []string{}, map[string]bool{}
	for _, mo := range l.models {
		id := objStr(mo, "id")
		if id == "" {
			continue
		}
		enabled = append(enabled, id)
		if f, has := m.LLM.ModelFailure(m.callKey(cred, id), id); has {
			if t, ok := parseDate(f.At); ok && time.Since(t) < 10*time.Minute {
				failing[id] = true
			}
		}
	}
	return enabled, failing, true
}

// pwcodeModel moves a chat's session off a platform model the viewer's
// AI connection does not offer, or one whose call just failed for them,
// and notes on the link which model the chat runs on and why. A model
// Settings names, and a session continued from the Code page (the
// person's own), are kept as they are.
func (m *Module) pwcodeModel(ctx context.Context, r *http.Request, t *pwTarget, link *conversations.PWCodeLink) {
	if link == nil || link.Continued || link.Session == "" || m.eff().PWCodeModel != "" || t.picked != "" || !platformModel(link.Model) {
		return
	}
	enabled, failing, ok := m.enabledModels(r)
	if !ok {
		return
	}
	pick := pickPWCodeModel(link.Model, t.chatModel, enabled, failing)
	if pick == "" {
		return
	}
	why := "is not enabled for you"
	if listed(link.Model, enabled) {
		why = "failed for you in the last ten minutes"
	}
	// Two short sentences by the models' short names; the menu shows the
	// full ids (modelFrom, model) on hover, and links the setting.
	const other = " Choose another under Settings, External access, pw code model."
	if _, err := pwcode.SetSessionModel(ctx, t.client, link.Session, pick); err != nil {
		m.A.Log.Warn("pw code chat: the session's model could not be changed", "session", link.Session, "from", link.Model, "to", pick, "err", err)
		// Said in the chat too: the turn is about to fail on that model.
		link.ModelNote = shortModel(link.Model) + " " + why + ", and pw code did not take " + shortModel(pick) + " in its place: " + oneLineErr(err) + "." + other
		t.modelStuck = true
		return
	}
	link.ModelNote = "Runs on " + shortModel(pick) + " because " + shortModel(link.Model) + " " + why + "." + other
	link.ModelFrom = link.Model
	link.Model = pick
	t.modelMoved = true
}

// oneLineErr is an error's text on one short line.
func oneLineErr(err error) string {
	s := strings.Join(strings.Fields(err.Error()), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:199]) + "…"
	}
	return strings.TrimRight(s, ".")
}

// agentModel is app.Providers.AgentModel: pickPWCodeModel for an agent
// session the Studio starts, leaving it on a model Settings names.
func (m *Module) agentModel(r *http.Request, current, chosen string) (string, string) {
	if m.eff().PWCodeModel != "" || !platformModel(current) {
		return "", ""
	}
	enabled, failing, ok := m.enabledModels(r)
	if !ok {
		return "", ""
	}
	pick := pickPWCodeModel(current, chosen, enabled, failing)
	if pick == "" {
		return "", ""
	}
	why := "is not enabled for the AI connection"
	if listed(current, enabled) {
		why = "failed when last used, in the last ten minutes"
	}
	return pick, "pw code's model " + current + " " + why + ", so the session runs on " + pick
}
