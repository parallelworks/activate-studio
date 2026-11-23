package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/codesessions"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// Continuing a pw code session in a Studio chat. The Code page lists the
// viewer's sessions on every machine through the platform; a session on
// the machine whose pw code daemon this Studio's chat reaches for them can
// go on in a Studio chat. The chat links to that session (no new one is
// made), starts with the session's earlier turns, and from then on sends
// each message, approval and stop through the pw code chat path.
//
// Whose session it is: the platform lists it as the viewer's, read with
// their own credential, on a machine whose daemon id is the id of the
// daemon the Studio reaches as them (this host's on a Studio one person
// uses, else their own daemon on the Studio's cluster with their own
// sign-in or key, never the Studio's account), and that daemon has it.
// Anything short of that keeps the session read only.
//
// The Studio's tools: pw code takes MCP servers when a session is made
// (agentsJson) or from config files, and has no way to add one to a
// session that exists. A session that already has the Studio's studio
// server (one a Studio chat made) is checked and reconnected as a chat's
// own; any other goes on with the tools it has, and the chat says so.

// pwHere is the daemon the viewer's pw code chat reaches: its machine id
// and host name as it reports them, and the site the Studio calls it.
type pwHere struct {
	client   pwcode.Client
	site     string
	daemonID string
	hostname string
	// status is what the daemon reported, such as what it allows.
	status any
}

// pwcodeDaemon is the viewer's pw code daemon and its site, as
// pwcodeTarget finds it, without a workspace.
func (m *Module) pwcodeDaemon(r *http.Request) (pwcode.Client, string, error) {
	s := pwSites()
	if s == nil {
		return nil, "", pwErr(409, "pw code is not set up on this Studio.")
	}
	if !m.A.Cfg.AuthEnabled() {
		return s.Daemon.Local(), m.pwSite(), nil
	}
	if user(r) == nil {
		return nil, "", pwErr(401, "Sign in to use pw code.")
	}
	key := m.viewerPlatformKey(r)
	if key == "" {
		return nil, "", pwErr(409, "pw code runs as you: sign in with ACTIVATE under Settings, Accounts.")
	}
	site := m.pwSite()
	if site == "" {
		return nil, "", pwErr(409, "This Studio does not know the cluster it runs on (STUDIO_HOME_CLUSTER), so it cannot reach your pw code.")
	}
	return s.Daemon.Remote(site, key), site, nil
}

// pwcodeHere is the viewer's daemon and who it says it is.
func (m *Module) pwcodeHere(ctx context.Context, r *http.Request) (*pwHere, error) {
	if why := m.pwcodeUnavailable(ctx, r); why != "" {
		return nil, pwErr(409, why)
	}
	client, site, err := m.pwcodeDaemon(r)
	if err != nil {
		return nil, err
	}
	st := pwSites().Daemon.Status(ctx, client)
	if st == nil {
		return nil, pwErr(502, "Your pw code daemon did not answer.")
	}
	return &pwHere{client: client, site: site,
		daemonID: jsonv.String(jsonv.Path(st, "machineId")), hostname: jsonv.String(jsonv.Path(st, "hostname")), status: st}, nil
}

type pwHereAt struct {
	h  *pwHere
	at time.Time
}

// pwcodeHereCached is pwcodeHere kept a minute per viewer, since the Code
// page asks every 20 seconds and a daemon on a cluster is a pw ssh away.
func (m *Module) pwcodeHereCached(ctx context.Context, r *http.Request) (*pwHere, error) {
	who := userID(r)
	m.pw.mu.Lock()
	if a, ok := m.pw.here[who]; ok && time.Since(a.at) < time.Minute {
		m.pw.mu.Unlock()
		return a.h, nil
	}
	m.pw.mu.Unlock()
	h, err := m.pwcodeHere(ctx, r)
	if err != nil {
		return nil, err
	}
	m.pw.mu.Lock()
	if m.pw.here == nil {
		m.pw.here = map[string]pwHereAt{}
	}
	m.pw.here[who] = pwHereAt{h: h, at: time.Now()}
	m.pw.mu.Unlock()
	return h, nil
}

// sameMachine: the platform's machine is the one whose daemon answered,
// by the daemon id the platform keeps for it, or by host name where the
// platform keeps none.
func sameMachine(mach *codesessions.Machine, h *pwHere) bool {
	if mach == nil || h == nil || forceRelay() {
		return false
	}
	if mach.DaemonID != "" || h.daemonID == "" {
		return mach.DaemonID != "" && mach.DaemonID == h.daemonID
	}
	return mach.Hostname != "" && strings.EqualFold(mach.Hostname, h.hostname)
}

// forceRelay is for testing only: with STUDIO_PWCODE_FORCE_RELAY=1 a
// session on the Studio's own machine is continued through the platform
// relay too, as one on another machine is, so the relay can be exercised
// end to end with a single connected machine. Off by default.
func forceRelay() bool { return os.Getenv("STUDIO_PWCODE_FORCE_RELAY") == "1" }

// ownChats is the viewer's own conversations that run in a pw code
// session, by session id.
func (m *Module) ownChats(r *http.Request) map[string]*conversations.Conversation {
	me := ""
	if u := user(r); u != nil {
		me = u.Username
	}
	out := map[string]*conversations.Conversation{}
	for _, c := range m.Conv.List() {
		if c.PWCode == nil || c.PWCode.Session == "" {
			continue
		}
		if m.A.Cfg.AuthEnabled() && (me == "" || c.OwnerName() != me) {
			continue
		}
		if prev, ok := out[c.PWCode.Session]; !ok || c.UpdatedAt > prev.UpdatedAt {
			out[c.PWCode.Session] = c
		}
	}
	return out
}

// CodePageLink is the address of a pw code session on the Studio's Code
// page.
func CodePageLink(machine, session string) string {
	q := url.Values{"machine": {machine}, "session": {session}}
	return "#view=code?" + q.Encode()
}

// History limits for a continued session: its latest turns, each cut as a
// reply the Studio records is.
const (
	continueTurns     = 60
	continueTextChars = 20000
	continueToolChars = 4000
)

// continuedHistory is a session's history as chat messages: each turn the
// person's message, then one reply with the turn's text and tool calls, as
// the Studio records a pw code turn. The first is a note that says where
// the session came from; dropped is how many earlier turns are left out.
func continuedHistory(items []any, model string, now string) (msgs []conversations.Message, dropped int) {
	type turn struct {
		user  string
		text  []string
		parts []conversations.Part
	}
	var turns []*turn
	cur := func() *turn {
		if len(turns) == 0 {
			turns = append(turns, &turn{})
		}
		return turns[len(turns)-1]
	}
	for _, x := range items {
		o := jsonv.Obj(x)
		if o == nil {
			continue
		}
		str := func(k string) string { return jsonv.String(jsonv.Path(o, k)) }
		switch str("kind") {
		case "user":
			turns = append(turns, &turn{user: str("text")})
		case "assistant", "recap", "plan_view", "goal_view":
			if t := strings.TrimSpace(str("text")); t != "" {
				cur().text = append(cur().text, t)
			}
		case "error":
			if t := strings.TrimSpace(str("text")); t != "" {
				cur().text = append(cur().text, "pw code reported an error: "+t)
			}
		case "tool_call":
			t := cur()
			t.parts = append(t.parts, conversations.Part{Kind: "tool_call", ID: firstNonEmpty(str("callId"), fmt.Sprintf("h%d", len(t.parts))),
				Name: strings.TrimPrefix(str("toolName"), studioToolPrefix), Args: str("toolArgs"), Status: "ok"})
		case "tool_result":
			t := cur()
			id := str("callId")
			for i := len(t.parts) - 1; i >= 0; i-- {
				if t.parts[i].Result == nil && (id == "" || t.parts[i].ID == id) {
					res := sliceRunes(str("text"), continueToolChars)
					t.parts[i].Result = &res
					if strings.HasPrefix(res, "Error:") {
						t.parts[i].Status = "error"
					}
					break
				}
			}
		}
	}
	if len(turns) > continueTurns {
		dropped = len(turns) - continueTurns
		turns = turns[dropped:]
	}
	shown := "pw code"
	if model != "" {
		shown += " · " + model
	}
	var parent *string
	add := func(m conversations.Message) {
		m.ParentID, m.Timestamp = parent, now
		msgs = append(msgs, m)
		id := m.ID
		parent = &id
	}
	for i, t := range turns {
		if strings.TrimSpace(t.user) != "" {
			add(conversations.Message{ID: fmt.Sprintf("pwh-%d-u", i), Role: "user", Content: sp(sliceRunes(t.user, continueTextChars))})
		}
		if len(t.text) > 0 || len(t.parts) > 0 {
			add(conversations.Message{ID: fmt.Sprintf("pwh-%d-a", i), Role: "assistant", Content: sp(sliceRunes(strings.Join(t.text, "\n\n"), continueTextChars)),
				Model: sp(shown), HasModel: true, Parts: t.parts, FinishReason: "stop"})
		}
	}
	return msgs, dropped
}

// continueNote opens a continued chat: where the session came from, and a
// link to all of it on the Code page.
func continueNote(v *codesessions.View, dropped int, extra ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Continued from the pw code session **%s** on %s", strings.ReplaceAll(v.Title, "*", ""), v.Machine)
	if v.Workspace != "" {
		fmt.Fprintf(&b, ", in `%s`", strings.ReplaceAll(shortWorkspace(v.Workspace), "`", ""))
	}
	b.WriteString(". Messages you send here go to that session.")
	for _, e := range extra {
		b.WriteString(e)
	}
	if dropped > 0 {
		s := "s"
		if dropped == 1 {
			s = ""
		}
		fmt.Fprintf(&b, " Its %d earlier turn%s are not copied here.", dropped, s)
	}
	fmt.Fprintf(&b, " [See the session on the Code page](%s)", CodePageLink(v.MachineID, v.ID))
	return b.String()
}

// shortWorkspace is a workspace path by its last two folders, as the Code
// page's list names it.
func shortWorkspace(p string) string {
	parts := strings.FieldsFunc(p, func(r rune) bool { return r == '/' })
	if len(parts) <= 2 {
		return p
	}
	return "…/" + strings.Join(parts[len(parts)-2:], "/")
}

// continueConfirm is what the person is asked before the chat takes a
// session: the daemon cannot say whether a terminal has it open, so the
// question is always asked, and says more when the session is open on
// another host or at work.
func continueConfirm(status, liveHost string) string {
	base := "Your messages from this chat go to the same session. If it is also open in a terminal or in PW Code on the web, messages from there go to it too."
	switch status {
	case "live_elsewhere":
		where := "another machine"
		if liveHost != "" {
			where = liveHost
		}
		return "This session is open on " + where + ". Continuing it here takes it over from there. " + base
	case "running":
		return "This session is working on a reply now. The chat follows from your next message. " + base
	case "awaiting_approval":
		return "This session is waiting for an approval, which you can answer in the chat. " + base
	}
	return base
}

func (m *Module) pwcodeContinueRoutes(h func(pattern string, fn func(http.ResponseWriter, *http.Request) error)) {
	// Which machine's sessions this viewer can continue here, and which of
	// their chats already run in a session.
	h("GET /api/chat/pwcode/here", func(w http.ResponseWriter, r *http.Request) error {
		chats := []any{}
		for id, c := range m.ownChats(r) {
			title := ""
			if c.Title != nil {
				title = *c.Title
			}
			chats = append(chats, reqbody.O("session", id, "conversationId", c.ID, "title", title, "continued", c.PWCode.Continued))
		}
		// Sessions on the viewer's other machines go through the relay.
		var relayWhy any
		if why := m.relayUnavailable(r); why != "" {
			relayWhy = why
		}
		relay := relayWhy == nil
		here, err := m.pwcodeHereCached(r.Context(), r)
		if err != nil {
			return reqbody.OK(w, reqbody.O("available", false, "reason", apiMessage(err), "chats", chats, "relay", relay, "relayReason", relayWhy))
		}
		return reqbody.OK(w, reqbody.O("available", true, "daemonId", here.daemonID, "hostname", here.hostname, "chats", chats, "relay", relay, "relayReason", relayWhy))
	})
	// Continue one of the viewer's sessions in a new chat. The first call
	// checks and answers with what to ask the person; the call with
	// confirmed makes the chat.
	h("POST /api/chat/pwcode/continue", func(w http.ResponseWriter, r *http.Request) error {
		var in struct {
			Machine   string `json:"machine"`
			Session   string `json:"session"`
			Confirmed bool   `json:"confirmed"`
		}
		rb, err := reqbody.Read(r)
		if err != nil {
			return err
		}
		if !rb.Present || json.Unmarshal(rb.Raw, &in) != nil {
			return pwErr(400, "machine and session are required")
		}
		if c := m.ownChats(r)[in.Session]; c != nil && in.Session != "" {
			return reqbody.OK(w, reqbody.O("conversationId", c.ID, "existing", true))
		}
		// The viewer's own pw code chat, or failing that the relay, must be
		// theirs before the platform is read: nothing is read as the
		// Studio's account.
		ctx := r.Context()
		here, hereErr := m.pwcodeHere(ctx, r)
		if hereErr != nil && m.relayUnavailable(r) != "" {
			return hereErr
		}
		mach, v, err := codesessions.Find(r, m.pf, in.Machine, in.Session)
		if err != nil {
			return err
		}
		if hereErr != nil || !sameMachine(mach, here) {
			return m.continueRelayed(w, r, mach, v, in.Session, in.Confirmed)
		}
		d, err := pwcode.GetSession(ctx, here.client, in.Session)
		var de *pwcode.DaemonError
		if errors.As(err, &de) && de.Status == http.StatusNotFound {
			return pwErr(404, "pw code on "+firstNonEmpty(here.hostname, mach.Name)+" does not have this session for you.")
		} else if err != nil {
			return err
		}
		if jsonv.String(jsonv.Path(d, "archivedAt")) != "" {
			return pwErr(409, archivedWhy)
		}
		status, liveHost := jsonv.String(jsonv.Path(d, "status")), jsonv.String(jsonv.Path(d, "liveHost"))
		if !in.Confirmed {
			return reqbody.OK(w, reqbody.O("confirm", continueConfirm(status, liveHost), "status", status, "title", v.Title))
		}
		// The session is loaded into this daemon, which takes it from a
		// daemon on another host that had it open.
		if _, err := pwcode.AttachSession(ctx, here.client, in.Session); err != nil {
			return pwErr(502, "pw code did not open the session here: "+apiMessage(err))
		}
		model := jsonv.String(jsonv.Path(d, "model"))
		link := &conversations.PWCodeLink{Site: here.site, Session: in.Session, Workspace: jsonv.String(jsonv.Path(d, "workspace")),
			URL: v.URL, Model: model, CreatedAt: m.Conv.Now(), Continued: true, Machine: mach.ID, MachineName: mach.Name}
		if st, err := pwcode.SessionState(ctx, here.client, in.Session); err == nil {
			if u := jsonv.String(jsonv.Path(st, "remoteUrl")); u != "" {
				link.URL = u
			}
			link.Model = firstNonEmpty(jsonv.String(jsonv.Path(st, "model")), model)
		}
		// The Studio's tools, when the session has the studio server.
		tools := reqbody.O("connected", false, "count", 0, "note", "This session began outside the Studio, so it has its own tools and not the Studio's library tools: pw code cannot add them to a session that already exists.")
		if s, err := studioMCPState(ctx, here.client, in.Session); err == nil && s != nil {
			if ck := ensureStudioMCP(ctx, here.client, in.Session, specNames(m.chatTools(r, true))); ck.OK {
				link.MCPURL = jsonv.String(jsonv.Path(s, "url"))
				tools = reqbody.O("connected", true, "count", ck.Tools)
			} else {
				tools = reqbody.O("connected", false, "count", 0, "note", "The session's connection to the Studio's tools did not work: "+ck.Detail)
			}
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		hist, dropped := continuedHistory(jsonv.Arr(jsonv.Path(d, "history")), link.Model, now)
		note := conversations.Message{ID: fmt.Sprintf("note-continued-%d", time.Now().UnixMilli()), Role: "assistant", Content: sp(continueNote(v, dropped)), Timestamp: now}
		if len(hist) > 0 {
			hist[0].ParentID = &note.ID
		}
		msgs := append([]conversations.Message{note}, hist...)
		link.LastMessage = msgs[len(msgs)-1].ID
		owner := ""
		if u := user(r); u != nil {
			owner = u.Username
		}
		title := v.Title
		c := m.Conv.CreateWith(&title, owner, msgs, link)
		m.A.Log.Info("pw code chat: continued a session from the Code page", "session", in.Session, "conversation", c.ID, "status", status)
		return reqbody.OK(w, reqbody.O("conversationId", c.ID, "tools", tools))
	})
}

// continuedSession is a continued chat's session, ready for the next
// message: still there, not archived, not taken by a daemon on another
// host since, and open in this daemon. Its studio server, when it has
// one, is checked as a chat's own is; it is never archived or replaced.
// A session on another machine (relayed) is only read: its machine
// decides the rest.
func (m *Module) continuedSession(ctx context.Context, r *http.Request, t *pwTarget, l *conversations.PWCodeLink) error {
	where := firstNonEmpty(l.MachineName, l.Site)
	if l.Site != t.site {
		return pwErr(409, "This chat continues a pw code session on "+where+", which this Studio does not reach for you.")
	}
	d, err := pwcode.GetSession(ctx, t.client, l.Session)
	var de *pwcode.DaemonError
	if errors.As(err, &de) && de.Status == http.StatusNotFound {
		return pwErr(409, "The pw code session this chat continues is no longer on "+where+", or ACTIVATE does not reach that machine for you now. Start a new chat to go on.")
	} else if err != nil {
		return err
	}
	if jsonv.String(jsonv.Path(d, "archivedAt")) != "" {
		return pwErr(409, "The pw code session this chat continues was archived. Restore it in ACTIVATE to go on here, or start a new chat.")
	}
	if t.relay {
		return nil
	}
	if jsonv.String(jsonv.Path(d, "status")) == "live_elsewhere" {
		return pwErr(409, "The pw code session this chat continues is now open on "+firstNonEmpty(jsonv.String(jsonv.Path(d, "liveHost")), "another machine")+". Continue it again from the Code page to take it back.")
	}
	if _, err := pwcode.SessionMCP(ctx, t.client, l.Session); err != nil {
		var de *pwcode.DaemonError
		if errors.As(err, &de) && notLiveMessage(de.Message) {
			if _, err := pwcode.AttachSession(ctx, t.client, l.Session); err != nil {
				return err
			}
		}
	}
	if s, err := studioMCPState(ctx, t.client, l.Session); err == nil && s != nil {
		if ck := ensureStudioMCP(ctx, t.client, l.Session, specNames(m.chatTools(r, true))); !ck.OK {
			m.A.Log.Warn("pw code chat: a continued session's studio server did not connect", "session", l.Session, "detail", ck.Detail)
		}
	}
	return nil
}

func notLiveMessage(s string) bool { return strings.Contains(strings.ToLower(s), "not live") }

// archivedWhy is why an archived session is not continued, and what still
// works with it here.
const archivedWhy = "This session is archived, so it cannot be continued in a chat; you can still read it on the Code page. Restore it in ACTIVATE to continue it here."

// continueRelayed is the continue route for a session on another of the
// viewer's machines: it is read through the platform's relay, as them,
// and the chat made drives it there, so nothing is attached or moved.
func (m *Module) continueRelayed(w http.ResponseWriter, r *http.Request, mach *codesessions.Machine, v *codesessions.View, session string, confirmed bool) error {
	ctx := r.Context()
	client, err := m.relayClient(r, mach.ID)
	if err != nil {
		return err
	}
	d, err := pwcode.GetSession(ctx, client, session)
	var de *pwcode.DaemonError
	if errors.As(err, &de) && de.Status == http.StatusNotFound {
		return pwErr(404, "ACTIVATE does not reach this session on "+mach.Name+" for you now. You can still read it on the Code page, and Approve or Deny what it waits on, when its machine answers.")
	} else if err != nil {
		return pwErr(502, "ACTIVATE did not read the session on "+mach.Name+": "+apiMessage(err)+" You can still read it on the Code page, or open it in ACTIVATE.")
	}
	if jsonv.String(jsonv.Path(d, "archivedAt")) != "" {
		return pwErr(409, archivedWhy)
	}
	workspace := jsonv.String(jsonv.Path(d, "workspace"))
	status, liveHost := jsonv.String(jsonv.Path(d, "status")), jsonv.String(jsonv.Path(d, "liveHost"))
	if !confirmed {
		return reqbody.OK(w, reqbody.O("confirm", relayConfirm(mach.Name, workspace, status, liveHost, m.A.Cfg.AuthEnabled()), "status", status, "title", v.Title, "relay", true))
	}
	model := jsonv.String(jsonv.Path(d, "model"))
	link := &conversations.PWCodeLink{Site: pwcode.RelaySite(mach.ID), Relay: true, Session: session, Workspace: workspace,
		URL: v.URL, Model: model, CreatedAt: m.Conv.Now(), Continued: true, Machine: mach.ID, MachineName: mach.Name}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	hist, dropped := continuedHistory(jsonv.Arr(jsonv.Path(d, "history")), model, now)
	note := conversations.Message{ID: fmt.Sprintf("note-continued-%d", time.Now().UnixMilli()), Role: "assistant", Content: sp(continueNote(v, dropped, relayNote(mach.Name))), Timestamp: now}
	if len(hist) > 0 {
		hist[0].ParentID = &note.ID
	}
	msgs := append([]conversations.Message{note}, hist...)
	link.LastMessage = msgs[len(msgs)-1].ID
	owner := ""
	if u := user(r); u != nil {
		owner = u.Username
	}
	title := v.Title
	c := m.Conv.CreateWith(&title, owner, msgs, link)
	m.A.Log.Info("pw code chat: continued a session on another machine through the platform", "session", session, "conversation", c.ID, "status", status)
	return reqbody.OK(w, reqbody.O("conversationId", c.ID, "relay", true,
		"tools", reqbody.O("connected", false, "count", 0, "note", "This session runs on "+mach.Name+" and keeps its own tools there, not the Studio's library tools.")))
}
