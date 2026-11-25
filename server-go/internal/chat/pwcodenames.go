package chat

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// A pw code chat and its session keep one name.
//
// The chat's title follows the session: whenever the Studio reads the
// session (its listing, its detail, its feed, the Code page's list), a name
// that changed there since it was last seen becomes the title. pw code
// names a session after its first turn, so a new chat, which the page
// titles with the start of its first message, takes that name. A session
// whose first message also carried the Studio's instructions is named by
// the Studio instead (nameTold).
//
// A rename of the chat renames the session, as the viewer, on the machine
// it runs on. The last rename wins, since each reaches the other side at
// once. Where a rename cannot reach the session (its machine is off, or
// the chat is another person's), the chat keeps the new title, the person
// is told why, and it is sent again with their next message. A name
// changed on the session meanwhile wins over one still waiting.

// renameLimit bounds sending a name to a session.
const renameLimit = 15 * time.Second

// sessionNamed keeps the name of chat c's session as v, its row, detail or
// state, has it. It reports whether the chat's title changed.
func (m *Module) sessionNamed(c *conversations.Conversation, v any) bool {
	if c == nil || c.ID == "" || c.PWCode == nil {
		return false
	}
	return m.Conv.SessionNamed(c.ID, c.PWCode.Session, jsonv.String(jsonv.Path(v, "name")), jsonv.String(jsonv.Path(v, "autoName")))
}

// renameSession gives the session its chat's name.
func (m *Module) renameSession(ctx context.Context, t *pwTarget, c *conversations.Conversation, title string) error {
	ctx, cancel := context.WithTimeout(ctx, renameLimit)
	defer cancel()
	_, err := t.client.Call(ctx, http.MethodPost, "/v1/sessions/"+url.PathEscape(c.PWCode.Session)+"/rename", jsonv.NewObject("name", title), renameLimit)
	m.Conv.TitleGiven(c.ID, c.PWCode.Session, title, err == nil)
	return err
}

// chatRenamed gives the title a person gave a chat to its session, and says
// why the session keeps its own name when it does.
func (m *Module) chatRenamed(r *http.Request, c *conversations.Conversation) string {
	title := strings.TrimSpace(deref(c.Title))
	if c.PWCode == nil || c.PWCode.Session == "" || title == "" {
		return ""
	}
	// The page titles a new chat itself, with its first message.
	if c.SessionName == nil && conversations.IsDefaultTitle(title, c.FirstMessage()) {
		return ""
	}
	where := firstNonEmpty(c.PWCode.MachineName, "its machine")
	if _, err := m.ownPWChat(r, c.ID); err != nil {
		return "The chat is renamed here. Its pw code session keeps its name: only the person whose chat this is can rename it."
	}
	t, err := m.pwcodeTarget(r.Context(), r, c.PWCode)
	if err == nil && t.site != c.PWCode.Site {
		err = pwErr(409, "this Studio does not reach "+where+" for you.")
	}
	if err != nil {
		m.Conv.TitleGiven(c.ID, c.PWCode.Session, title, false)
	} else {
		err = m.renameSession(r.Context(), t, c, title)
	}
	if err != nil {
		return "The chat is renamed here. Its pw code session keeps its name for now, and is renamed with your next message: " + apiMessage(err)
	}
	m.A.Log.Info("pw code chat: renamed the chat's session", "user", userID(r), "conversation", c.ID, "session", c.PWCode.Session, "site", c.PWCode.Site)
	return ""
}

// giveWaitingName sends a title that has not reached the chat's session, or
// one chosen before the session was made, along with a message.
func (m *Module) giveWaitingName(r *http.Request, c *conversations.Conversation, t *pwTarget) {
	now := m.Conv.Get(c.ID)
	if now == nil || now.PWCode == nil || now.PWCode.Session == "" {
		return
	}
	title := ""
	switch n := now.SessionName; {
	case n != nil && n.Session == now.PWCode.Session:
		title = n.Rename
	case n == nil && !now.PWCode.Continued && !conversations.IsDefaultTitle(deref(now.Title), now.FirstMessage()):
		title = deref(now.Title)
	}
	if strings.TrimSpace(title) == "" {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	go func() {
		if err := m.renameSession(ctx, t, now, title); err != nil {
			m.A.Log.Warn("pw code chat: the session did not take the chat's name", "conversation", now.ID, "session", now.PWCode.Session, "error", err)
		}
	}()
}

// nameTold names a session whose first message brought the Studio's
// instructions along (relayInstructions): pw code would name it after them,
// so the Studio names it as its chat, after the person's own words.
func (m *Module) nameTold(r *http.Request, c *conversations.Conversation, t *pwTarget, said string) {
	now := m.Conv.Get(c.ID)
	if now == nil || now.PWCode == nil {
		return
	}
	title := strings.TrimSpace(deref(now.Title))
	if title == "" || (conversations.IsDefaultTitle(title, "") && strings.TrimSpace(said) != "") {
		title = firstLine(strings.TrimSpace(said), 70)
	}
	if title == "" {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	go func() {
		if err := m.renameSession(ctx, t, now, title); err != nil {
			m.A.Log.Warn("pw code chat: the session did not take the chat's name", "conversation", now.ID, "session", now.PWCode.Session, "error", err)
			return
		}
		// Unless the person named the chat meanwhile.
		if cur := m.Conv.Get(now.ID); cur != nil && deref(cur.Title) == deref(now.Title) {
			m.Conv.SetTitle(now.ID, title)
		}
	}()
}

// pageListed keeps the names of the viewer's chats' sessions as the Code
// page's listing has them.
func (m *Module) pageListed(r *http.Request, rows []any) {
	own := m.ownChats(r)
	if len(own) == 0 {
		return
	}
	for _, s := range rows {
		m.sessionNamed(own[jsonv.String(jsonv.Path(s, "id"))], s)
	}
}

// pageRenamed keeps a name the Code page gave a session.
func (m *Module) pageRenamed(r *http.Request, id, name string) {
	m.sessionNamed(m.ownChats(r)[id], jsonv.NewObject("name", name))
}
