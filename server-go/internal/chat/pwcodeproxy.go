package chat

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/codesessions"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
	"github.com/parallelworks/activate-studio/server-go/internal/sessions"
)

// A pw code chat is shown with @parallelworks/code's session view, the one
// PW Code on the web shows a session with: its transcript, its live feed,
// its approvals, asks and plans, and its composer with the session's mode,
// model and effort. That view calls the platform's own routes for the
// session (/api/code/machines/{machine}/sessions/{id}...) and opens its
// event feed. The Studio serves both at its own origin, for the viewer's
// own pw code chats only, and sends each on: through the platform's relay
// as the viewer for a session on another machine, or to the daemon the
// Studio reaches for a session it drives directly. The platform passes the
// daemon's bodies through unchanged, so the shapes are the same either way.
//
// What the view may send is pwcode.ProxyRoute's list. A change of the
// session's permission mode is the viewer's own, made with their own
// credential, logged, and held to the machine's remote ceiling for a
// relayed session; the Studio's own account never changes a mode.

// directMachine is the machine id the Studio's pages name a session it
// reaches directly with: its own daemon, or the person's on its cluster.
const directMachine = "studio"

// proxyMachine is the machine id a chat's session is shown under.
func proxyMachine(l *conversations.PWCodeLink) string {
	if l.Relay {
		return l.Machine
	}
	return directMachine
}

// The permission modes in order of authority. plan is not set this way.
var modeRank = map[string]int{"read-only": 0, "accept-edits": 1, "auto": 2, "bypass-permissions": 3}

// defaultRemoteCeiling is the ceiling a machine that names none holds a
// remote caller to (pw code's permissions.DefaultRemoteCeiling).
const defaultRemoteCeiling = "accept-edits"

// settingsFields are the session settings the Studio's view changes.
var settingsFields = map[string]bool{"permissionMode": true, "model": true, "effort": true}

// writeProblem answers as the platform does (application/problem+json), so
// the view's error words read it.
func writeProblem(w http.ResponseWriter, status int, msg string) error {
	if status < 400 {
		status = http.StatusBadGateway
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	b, _ := jsonv.Marshal(jsonv.NewObject("title", http.StatusText(status), "status", status, "detail", msg))
	_, _ = w.Write(b)
	return nil
}

// problemOf is an error's status and words for the reader.
func problemOf(err error) (int, string) {
	var de *pwcode.DaemonError
	if errors.As(err, &de) {
		return de.Status, strings.TrimPrefix(de.Message, "pw code daemon: ")
	}
	status := http.StatusBadGateway
	if s := apiStatus(err); s != 0 {
		status = s
	}
	return status, apiMessage(err)
}

// proxiedSession is the viewer's own chat with the session the path names,
// and its daemon reached as them.
func (m *Module) proxiedSession(r *http.Request) (*conversations.Conversation, *pwTarget, error) {
	id := r.PathValue("id")
	c := m.ownChats(r)[id]
	if c == nil || c.PWCode == nil || r.PathValue("machine") != proxyMachine(c.PWCode) {
		return nil, nil, pwErr(404, "This Studio has no chat of yours with that pw code session.")
	}
	if _, err := m.ownPWChat(r, c.ID); err != nil {
		return nil, nil, err
	}
	t, err := m.pwcodeTarget(r.Context(), r, c.PWCode)
	if err != nil {
		return nil, nil, err
	}
	if t.site != c.PWCode.Site {
		return nil, nil, pwErr(409, "This chat's pw code session is on "+firstNonEmpty(c.PWCode.MachineName, c.PWCode.Site)+", which this Studio does not reach for you.")
	}
	return c, t, nil
}

func (m *Module) pwcodeProxyRoutes(h func(pattern string, fn func(http.ResponseWriter, *http.Request) error)) {
	for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
		h(method+" /api/code/machines/{machine}/sessions/{id}", m.codeProxy)
		h(method+" /api/code/machines/{machine}/sessions/{id}/{rest...}", m.codeProxy)
	}
	h("GET /api/pwcode-feed/machines/{machine}/sessions/{id}/events", m.codeFeed)
	h("GET /api/pwcode-feed/sessions", m.codeListFeed)
	h("GET /api/chat/pwcode/{id}/machine", m.codeMachine)
	h("GET /api/code/sessions", m.codeListing)
}

// codeProxy sends one of the view's requests on to the session.
func (m *Module) codeProxy(w http.ResponseWriter, r *http.Request) error {
	c, t, err := m.proxiedSession(r)
	if err != nil {
		return writeErr(w, err)
	}
	ctx := r.Context()
	id := c.PWCode.Session
	suffix := ""
	if rest := r.PathValue("rest"); rest != "" {
		suffix = "/" + rest
	}
	key := pwcode.ProxyRoute(r.Method, suffix)
	if key == "" {
		return writeProblem(w, http.StatusNotFound, "The Studio does not send this to a pw code session; do it in PW Code on the web or where the session runs.")
	}
	var body any
	if r.Method == http.MethodPost || r.Method == http.MethodPatch {
		rb, err := reqbody.Read(r)
		if err != nil {
			return writeProblem(w, http.StatusBadRequest, apiMessage(err))
		}
		if rb.Present {
			if body, err = jsonv.Decode(rb.Raw); err != nil {
				return writeProblem(w, http.StatusBadRequest, "The body is not JSON.")
			}
		} else {
			body = jsonv.NewObject()
		}
	}
	var lastSeq int64
	switch key {
	case "PATCH /settings":
		if err := m.checkSettings(r, c, t, body); err != nil {
			return writeErr(w, err)
		}
	case "POST /approvals/{approval}":
		// A plan's answer names the mode the session leaves plan mode
		// into: held to the same rules as a settings change.
		if o := jsonv.Obj(body); o != nil && o.Has("mode") {
			if err := m.checkMode(r, c, t, jsonv.String(jsonv.Path(o, "mode"))); err != nil {
				return writeErr(w, err)
			}
		}
	case "POST /messages":
		m.studioToolsFor(ctx, r, c, t)
		if d, err := pwcode.GetSession(ctx, t.client, id); err == nil {
			if n, ok := jsonv.Num(jsonv.Path(d, "lastSeq")); ok {
				lastSeq = int64(n)
			}
		}
	}
	route := "/v1/sessions/" + url.PathEscape(id) + suffix
	if q := r.URL.RawQuery; q != "" {
		route += "?" + q
	}
	v, err := t.client.Call(ctx, r.Method, route, body, 0)
	if err != nil {
		return writeErr(w, err)
	}
	switch key {
	case "GET ":
		if o := jsonv.Obj(v); o != nil && !t.relay {
			o.Set("machineId", directMachine)
		}
	case "PATCH /settings":
		if mode := jsonv.String(jsonv.Path(body, "permissionMode")); mode != "" {
			m.logModeChange(r, c, t, mode, "settings")
		}
	case "POST /approvals/{approval}":
		if mode := jsonv.String(jsonv.Path(body, "mode")); mode != "" {
			m.logModeChange(r, c, t, mode, "plan answer")
		}
	case "POST /messages":
		m.recordSent(r, c, t, jsonv.String(jsonv.Path(body, "text")), lastSeq)
	}
	if v == nil {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	return reqbody.OK(w, v)
}

// checkSettings refuses a settings change the Studio does not make: a
// field other than mode, model and effort, a mode it does not know, or,
// for a relayed session, a mode above what the machine takes from
// ACTIVATE.
func (m *Module) checkSettings(r *http.Request, c *conversations.Conversation, t *pwTarget, body any) error {
	o := jsonv.Obj(body)
	if o == nil || o.Len() == 0 {
		return pwErr(400, "Name the mode, model or effort to change.")
	}
	for _, k := range o.Keys() {
		if !settingsFields[k] {
			return pwErr(400, "The Studio changes a session's mode, model and effort only; change "+k+" where the session runs.")
		}
	}
	if !o.Has("permissionMode") {
		return nil
	}
	return m.checkMode(r, c, t, jsonv.String(jsonv.Path(o, "permissionMode")))
}

// checkMode refuses a permission mode the Studio does not know, or, for a
// relayed session, one above what the machine takes from ACTIVATE. It
// fails closed: when the platform's listing cannot be read, or names a
// ceiling the Studio does not rank, the machine is taken to allow
// read-only, and the refusal says why.
func (m *Module) checkMode(r *http.Request, c *conversations.Conversation, t *pwTarget, mode string) error {
	if _, ok := modeRank[mode]; !ok {
		return pwErr(400, "Not a permission mode: "+mode+".")
	}
	if !t.relay {
		return nil
	}
	ceiling, name, why := defaultRemoteCeiling, firstNonEmpty(c.PWCode.MachineName, c.PWCode.Machine), ""
	mach, err := codesessions.PlatformMachine(r, m.pf, c.PWCode.Machine)
	if err != nil || mach == nil {
		ceiling, why = "read-only", " (the Studio could not read the machine's limit from ACTIVATE, so it allows read-only)"
	} else {
		if s := jsonv.String(jsonv.Path(mach, "remoteMaxPermissionMode")); s != "" {
			ceiling = s
		}
		name = firstNonEmpty(jsonv.String(jsonv.Path(mach, "name")), name)
	}
	if _, ok := modeRank[ceiling]; !ok {
		ceiling, why = "read-only", " (ACTIVATE names a limit the Studio does not know, "+ceiling+", so it allows read-only)"
	}
	if modeRank[mode] > modeRank[ceiling] {
		return pwErr(403, name+" takes at most "+ceiling+" from ACTIVATE"+why+". A wider mode is set on the machine itself, in pw code there.")
	}
	return nil
}

// logModeChange records a permission mode change the Studio sent, and how.
func (m *Module) logModeChange(r *http.Request, c *conversations.Conversation, t *pwTarget, mode, via string) {
	m.A.Log.Info("pw code chat: permission mode changed", "user", userID(r), "conversation", c.ID, "session", c.PWCode.Session,
		"site", c.PWCode.Site, "relay", t.relay, "mode", mode, "via", via)
}

// studioToolsFor makes sure, before a message, that a session the Studio
// drives directly has the Studio's tools: a session the daemon let go is
// taken up again (the daemon restarted, or evicted it while idle), and a
// studio server that is down is reconnected, once more after a wait. What
// failed is kept on the chat, where its note says so; a relayed session
// cannot be given them yet (relayToolsNote).
func (m *Module) studioToolsFor(ctx context.Context, r *http.Request, c *conversations.Conversation, t *pwTarget) {
	l := c.PWCode
	if t.relay {
		return
	}
	if _, err := pwcode.SessionMCP(ctx, t.client, l.Session); err != nil {
		var de *pwcode.DaemonError
		if errors.As(err, &de) && notLiveMessage(de.Message) {
			if _, err := pwcode.AttachSession(ctx, t.client, l.Session); err != nil {
				m.setToolsNote(c, "pw code did not take the session up again: "+apiMessage(err))
				return
			}
		}
	}
	s, err := studioMCPState(ctx, t.client, l.Session)
	if err == nil && s == nil {
		if l.Continued {
			m.setToolsNote(c, "")
			return
		}
		m.setToolsNote(c, "the session has no studio MCP server")
		return
	}
	want := specNames(m.chatTools(r, true))
	ck := ensureStudioMCP(ctx, t.client, l.Session, want)
	if !ck.OK {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		ck = ensureStudioMCP(ctx, t.client, l.Session, want)
	}
	if !ck.OK {
		m.A.Log.Warn("pw code chat: the session's studio server did not connect", "session", l.Session, "detail", ck.Detail)
		m.setToolsNote(c, ck.Detail)
		return
	}
	m.setToolsNote(c, "")
}

// setToolsNote keeps why the chat's session lacks the Studio's tools after
// a retry, or clears it.
func (m *Module) setToolsNote(c *conversations.Conversation, why string) {
	if c.PWCode == nil || c.PWCode.ToolsError == why {
		return
	}
	l := *c.PWCode
	l.ToolsError = why
	m.Conv.SetPWCode(c.ID, &l)
}

// recordSent keeps a message the view sent in the Studio's own copy of the
// chat, which its list and search read, and records the session's reply
// there when it ends.
func (m *Module) recordSent(r *http.Request, c *conversations.Conversation, t *pwTarget, text string, after int64) {
	if strings.TrimSpace(text) == "" {
		return
	}
	owner := ""
	if u := user(r); u != nil {
		owner = u.Username
	}
	msgID := "pwu-" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	var parent *string
	if n := len(c.Messages); n > 0 {
		p := c.Messages[n-1].ID
		parent = &p
	}
	m.Conv.RecordUserTurn(c.ID, msgID, text, parent, owner)
	if m.following(c.ID) {
		return
	}
	link := *c.PWCode
	turn := &pwTurn{m: m, convID: c.ID, link: &link, parent: &msgID, started: time.Now(), done: make(chan struct{})}
	m.follow(turn)
	tctx, cancel := context.WithTimeout(context.Background(), pwcodeTurnLimit)
	events := make(chan *jsonv.Object, 256)
	followErr := make(chan error, 1)
	go func() {
		followErr <- pwcode.Follow(tctx, t.client, link.Session, after, func(ev *jsonv.Object) bool {
			select {
			case events <- ev:
				return true
			case <-tctx.Done():
				return false
			}
		})
		close(events)
	}()
	go func() {
		defer cancel()
		turn.run(events, followErr)
	}()
}

// codeMachine is the chat's machine as the view reads it (an
// AgentMachine): the platform's for a relayed session, with its remote
// ceiling, or the Studio's own for one it drives directly.
func (m *Module) codeMachine(w http.ResponseWriter, r *http.Request) error {
	c, err := m.ownPWChat(r, r.PathValue("id"))
	if err != nil {
		return err
	}
	if c.PWCode == nil || c.PWCode.Session == "" {
		return pwErr(409, "This chat has no pw code session.")
	}
	return m.writeMachine(w, r, c.PWCode)
}

// writeMachine answers the machine of a chat's session.
func (m *Module) writeMachine(w http.ResponseWriter, r *http.Request, l *conversations.PWCodeLink) error {
	if l.Relay {
		mach, err := codesessions.PlatformMachine(r, m.pf, l.Machine)
		if err != nil {
			return err
		}
		return reqbody.OK(w, mach)
	}
	name := l.MachineName
	if name == "" {
		if h, err := m.pwcodeHereCached(r.Context(), r); err == nil && h != nil {
			name = h.hostname
		}
	}
	if name == "" {
		name, _ = os.Hostname()
	}
	owner := ""
	if u := user(r); u != nil {
		owner = u.Username
	}
	return reqbody.OK(w, jsonv.NewObject("id", directMachine, "name", firstNonEmpty(name, "this Studio's machine"), "kind", "local",
		"owner", owner, "status", "connected"))
}

// feedHeartbeat is the frame the platform sends a feed that asked for a
// heartbeat; seq 0 keeps it out of the reader's cursor.
const feedHeartbeat = `{"type":"heartbeat","seq":0}`

// codeFeed is the session's event feed for the view, as the platform's
// /ws/code/machines/{machine}/sessions/{id}/events serves it: the daemon's
// own frames after ?after=, and a heartbeat every ?heartbeat= seconds.
func (m *Module) codeFeed(w http.ResponseWriter, r *http.Request) error {
	// A WebSocket is not bound by CORS: only the Studio's own pages open it.
	if !sessions.SameOrigin(r) {
		return writeProblem(w, http.StatusForbidden, "Only this Studio's pages open a session's events.")
	}
	c, t, err := m.proxiedSession(r)
	if err != nil {
		return writeErr(w, err)
	}
	q := r.URL.Query()
	after, err := strconv.ParseInt(firstNonEmpty(q.Get("after"), "0"), 10, 64)
	if err != nil || after < 0 {
		return writeProblem(w, http.StatusBadRequest, "Invalid after cursor")
	}
	beat := 0
	if s := q.Get("heartbeat"); s != "" {
		if beat, err = strconv.Atoi(s); err != nil || beat < 1 || beat > 60 {
			return writeProblem(w, http.StatusBadRequest, "Invalid heartbeat interval")
		}
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	up, err := pwcode.OpenEvents(ctx, t.client, c.PWCode.Session, after)
	if err != nil {
		return writeErr(w, err)
	}
	defer up.Close()
	timed := pwcode.Heartbeats(t.client)
	srv := websocket.Server{
		// The origin was checked above, against the host the page was sent from.
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(ws *websocket.Conn) {
			pipeFeed(ctx, ws, up, time.Duration(beat)*time.Second, timed)
		},
	}
	srv.ServeHTTP(hijacker{w}, r)
	return nil
}

// hijacker reaches the connection under the server's response wrappers
// (their Unwrap), which x/net/websocket asks for as an http.Hijacker.
type hijacker struct{ http.ResponseWriter }

func (h hijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(h.ResponseWriter).Hijack()
}

// pipeFeed passes up's frames to ws until either ends: up's own
// heartbeats are dropped and ws is sent its own every beat. With timed, an
// up that says nothing for three of its heartbeats is taken as lost, so the
// view reconnects.
func pipeFeed(ctx context.Context, ws, up *websocket.Conn, beat time.Duration, timed bool) {
	ws.PayloadType = websocket.TextFrame
	up.MaxPayloadBytes = 32 << 20
	ws.MaxPayloadBytes = 4 << 10
	done := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			close(done)
			up.Close()
			ws.Close()
		})
	}
	defer stop()
	var wmu sync.Mutex
	send := func(s string) error {
		wmu.Lock()
		defer wmu.Unlock()
		_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return websocket.Message.Send(ws, s)
	}
	// The view sends nothing; a read ends when it goes.
	go func() {
		defer stop()
		var s string
		for websocket.Message.Receive(ws, &s) == nil {
		}
	}()
	go func() {
		defer stop()
		for {
			if timed {
				_ = up.SetReadDeadline(time.Now().Add(3 * 10 * time.Second))
			}
			var s string
			if err := websocket.Message.Receive(up, &s); err != nil {
				return
			}
			if len(s) < 64 && strings.Contains(s, `"heartbeat"`) {
				if v, err := jsonv.DecodeString(s); err == nil && jsonv.String(jsonv.Path(v, "type")) == "heartbeat" {
					continue
				}
			}
			if send(s) != nil {
				return
			}
		}
	}()
	var tick <-chan time.Time
	if beat > 0 {
		t := time.NewTicker(beat)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-tick:
			if send(feedHeartbeat) != nil {
				return
			}
		}
	}
}

// apiStatus is an apierr's status, or 0.
func apiStatus(err error) int {
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

// writeErr answers err as a problem.
func writeErr(w http.ResponseWriter, err error) error {
	status, msg := problemOf(err)
	return writeProblem(w, status, msg)
}

// codeListing is /api/code/sessions as the view polls it, for the viewer's
// own pw code chats only: their machines (the platform's for relayed
// sessions, the Studio's own for the rest) and their sessions.
func (m *Module) codeListing(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	own := m.ownChats(r)
	machines, sessionsOut := []any{}, []any{}
	relayed, direct := map[string]bool{}, false
	for _, c := range own {
		if c.PWCode.Relay {
			relayed[c.PWCode.Machine] = true
		} else {
			direct = true
		}
	}
	if len(relayed) > 0 {
		if ms, ss, err := codesessions.PlatformListing(r, m.pf); err == nil {
			for _, mach := range ms {
				if relayed[jsonv.String(jsonv.Path(mach, "id"))] {
					machines = append(machines, mach)
				}
			}
			for _, s := range ss {
				if c := own[jsonv.String(jsonv.Path(s, "id"))]; c != nil && c.PWCode.Relay && c.PWCode.Machine == jsonv.String(jsonv.Path(s, "machineId")) {
					sessionsOut = append(sessionsOut, s)
				}
			}
		}
	}
	if direct {
		rec := &bodyRecorder{header: http.Header{}}
		if m.writeMachine(rec, r, &conversations.PWCodeLink{}) == nil && rec.status < 400 {
			if v, err := jsonv.Decode(rec.body); err == nil && jsonv.Obj(v) != nil {
				machines = append(machines, v)
			}
		}
		if t, err := m.pwcodeTarget(ctx, r, nil); err == nil {
			if list, err := pwcode.ListSessions(ctx, t.client); err == nil {
				rows := jsonv.Arr(list)
				if rows == nil {
					rows = jsonv.Arr(jsonv.Path(list, "sessions"))
				}
				for _, s := range rows {
					o := jsonv.Obj(s)
					if c := own[jsonv.String(jsonv.Path(o, "id"))]; o != nil && c != nil && !c.PWCode.Relay {
						o.Set("machineId", directMachine)
						sessionsOut = append(sessionsOut, o)
					}
				}
			}
		}
	}
	return reqbody.OK(w, jsonv.NewObject("machines", machines, "sessions", sessionsOut))
}

// bodyRecorder keeps a handler's answer for another to use.
type bodyRecorder struct {
	header http.Header
	status int
	body   []byte
}

func (b *bodyRecorder) Header() http.Header { return b.header }
func (b *bodyRecorder) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = 200
	}
	b.body = append(b.body, p...)
	return len(p), nil
}
func (b *bodyRecorder) WriteHeader(s int) { b.status = s }

// codeListFeed stands for the platform's feed of changes to the sessions
// list (/ws/code/sessions): it sends only its heartbeat, so the view reads
// the list by polling /api/code/sessions, which carries the viewer's own
// pw code chats only.
func (m *Module) codeListFeed(w http.ResponseWriter, r *http.Request) error {
	if !sessions.SameOrigin(r) {
		return writeProblem(w, http.StatusForbidden, "Only this Studio's pages open the sessions feed.")
	}
	beat, err := strconv.Atoi(firstNonEmpty(r.URL.Query().Get("heartbeat"), "10"))
	if err != nil || beat < 1 || beat > 60 {
		return writeProblem(w, http.StatusBadRequest, "Invalid heartbeat interval")
	}
	srv := websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(ws *websocket.Conn) {
			ws.PayloadType = websocket.TextFrame
			ws.MaxPayloadBytes = 4 << 10
			gone := make(chan struct{})
			go func() {
				defer close(gone)
				var s string
				for websocket.Message.Receive(ws, &s) == nil {
				}
			}()
			t := time.NewTicker(time.Duration(beat) * time.Second)
			defer t.Stop()
			defer ws.Close()
			for {
				select {
				case <-gone:
					return
				case <-r.Context().Done():
					return
				case <-t.C:
					_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
					if websocket.Message.Send(ws, feedHeartbeat) != nil {
						return
					}
				}
			}
		},
	}
	srv.ServeHTTP(hijacker{w}, r)
	return nil
}
