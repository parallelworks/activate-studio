package chat

import (
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"io"
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
	h("POST /api/chat/pwcode/{id}/follow", m.codeFollow)
	for _, method := range []string{"GET", "POST", "DELETE"} {
		h(method+" /api/code/machines/{machine}/claude-code/{rest...}", m.codeAccounts)
	}
}

// machineTarget is the viewer's own pw code on a machine: directMachine is
// the one the Studio drives for them (this Studio's when no one signs in,
// else theirs on the cluster the Studio runs on, reached with their own
// key), and any other is one of their machines through ACTIVATE, which
// answers only for the person's own.
func (m *Module) machineTarget(r *http.Request, machine string) (pwcode.Client, error) {
	if machine != directMachine {
		if !pwcode.IsRouteID(machine) {
			return nil, pwErr(404, "Not a machine.")
		}
		return m.relayClient(r, machine)
	}
	c, _, err := m.pwcodeDaemon(r)
	return c, err
}

// codeAccounts sends the account panel's requests on to the viewer's own
// pw code on the machine: its Claude Code accounts, their models, signing
// one in, removing one and moving its sessions. A change is logged.
func (m *Module) codeAccounts(w http.ResponseWriter, r *http.Request) error {
	suffix := "/" + r.PathValue("rest")
	key := pwcode.MachineRoute(r.Method, suffix)
	if key == "" {
		return writeProblem(w, http.StatusNotFound, "The Studio does not send this to pw code; do it in PW Code on the web or where pw code runs.")
	}
	if why := pwcode.ClaudeCodeOff(r.Context()); why != "" {
		return writeProblem(w, http.StatusForbidden, why)
	}
	machine := r.PathValue("machine")
	client, err := m.machineTarget(r, machine)
	if err != nil {
		return writeErr(w, err)
	}
	var body any
	if r.Method == http.MethodPost {
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
	route := "/v1/claude-code" + suffix
	if q := r.URL.RawQuery; q != "" {
		route += "?" + q
	}
	v, err := client.Call(r.Context(), r.Method, route, body, 0)
	if err != nil {
		return writeErr(w, err)
	}
	switch key {
	case "DELETE /accounts/{name}", "POST /accounts/{name}/move", "POST /logins":
		m.A.Log.Info("pw code: Claude Code account change", "user", userID(r), "machine", machine, "action", key, "route", suffix)
	}
	if v == nil {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	return reqbody.OK(w, v)
}

// imageTypes are what a session's image is served as: pictures only, never
// a page or an SVG, which could run script from the Studio's origin.
var imageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// maxImage is the largest image the Studio passes on.
const maxImage = 25 << 20

// codeImage passes one of the session's images on as the session's
// machine serves it.
func (m *Module) codeImage(w http.ResponseWriter, r *http.Request, t *pwTarget, route string) error {
	g, ok := t.client.(pwcode.ByteGetter)
	if !ok {
		return writeProblem(w, http.StatusNotImplemented, "The Studio cannot read this session's images.")
	}
	b, err := g.GetBytes(r.Context(), route, maxImage)
	if err != nil {
		return writeErr(w, err)
	}
	typ, _, _ := strings.Cut(b.Type, ";")
	typ = strings.ToLower(strings.TrimSpace(typ))
	if !imageTypes[typ] {
		return writeProblem(w, http.StatusBadGateway, "The session's machine did not answer with a picture.")
	}
	w.Header().Set("Content-Type", typ)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(b.Body)
	return nil
}

// codeStream hands a session's transcript on as it arrives, so the end the
// daemon sends first is drawn while the rest is on its way: read whole, a
// long session's took seconds before anything showed. Only a JSON answer
// is passed on, with none of the machine's or platform's other headers;
// a refusal is the route's usual error. A daemon without the route (an
// older pw code) is read once whole here and answered in the same form,
// so the page does not ask twice, one after the other.
func (m *Module) codeStream(w http.ResponseWriter, r *http.Request, t *pwTarget, g pwcode.StreamGetter, id, route string) error {
	s, err := g.GetStream(r.Context(), route)
	if err != nil {
		var de *pwcode.DaemonError
		if errors.As(err, &de) && (de.Status == http.StatusNotFound || de.Status == http.StatusMethodNotAllowed) {
			return m.transcriptFromDetail(w, r, t, id)
		}
		return writeErr(w, err)
	}
	defer s.Body.Close()
	typ, _, _ := strings.Cut(s.Type, ";")
	if strings.ToLower(strings.TrimSpace(typ)) != "application/json" {
		return writeProblem(w, http.StatusBadGateway, "The session's machine did not answer with a transcript.")
	}
	writeStreamed(w, r, func(out io.Writer, flush func()) {
		buf := make([]byte, 32<<10)
		for {
			n, rerr := s.Body.Read(buf)
			if n > 0 {
				if _, werr := out.Write(buf[:n]); werr != nil {
					return
				}
				flush()
			}
			if rerr != nil {
				// The status is sent: a transcript cut short is not JSON,
				// and the page reads it again.
				return
			}
		}
	})
	return nil
}

// transcriptTail is how many history items the end of a transcript built
// here holds when the page does not say: the daemon's own default.
const transcriptTail = 200

// transcriptFromDetail answers a transcript read from the session's whole
// detail, as a daemon with the route does: its end first, the earlier
// history after it.
func (m *Module) transcriptFromDetail(w http.ResponseWriter, r *http.Request, t *pwTarget, id string) error {
	route := "/v1/sessions/" + url.PathEscape(id)
	if p := r.URL.Query().Get("previews"); p != "" {
		route += "?" + url.Values{"previews": {p}}.Encode()
	}
	v, err := t.client.Call(r.Context(), http.MethodGet, route, nil, 0)
	if err != nil {
		return writeErr(w, err)
	}
	o := jsonv.Obj(v)
	if o == nil {
		return writeProblem(w, http.StatusBadGateway, "The session's machine did not answer with a transcript.")
	}
	if !t.relay {
		o.Set("machineId", directMachine)
	}
	n := transcriptTail
	if q, err := strconv.Atoi(r.URL.Query().Get("tail")); err == nil && q > 0 && q < 10000 {
		n = q
	}
	history := jsonv.Arr(jsonv.Path(o, "history"))
	at := max(0, len(history)-n)
	tail := o.Clone()
	tail.Set("history", append([]any{}, history[at:]...))
	head, err := jsonv.Marshal(tail)
	if err != nil {
		return err
	}
	older, err := jsonv.Marshal(append([]any{}, history[:at]...))
	if err != nil {
		return err
	}
	writeStreamed(w, r, func(out io.Writer, flush func()) {
		_, _ = out.Write([]byte(`{"tail":`))
		_, _ = out.Write(head)
		flush()
		_, _ = out.Write([]byte(`,"older":`))
		_, _ = out.Write(older)
		_, _ = out.Write([]byte(`}`))
	})
	return nil
}

// writeStreamed answers 200 with JSON that write produces a part at a
// time, each part sent as it is flushed: gzipped for a browser that takes
// that, a part at a time, so the start still arrives first.
func writeStreamed(w http.ResponseWriter, r *http.Request, write func(out io.Writer, flush func())) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	h.Add("Vary", "Accept-Encoding")
	var out io.Writer = w
	var zw *gzip.Writer
	if acceptsGzip(r.Header.Get("Accept-Encoding")) {
		h.Set("Content-Encoding", "gzip")
		zw, _ = gzip.NewWriterLevel(w, gzip.BestSpeed)
		out = zw
	}
	w.WriteHeader(http.StatusOK)
	write(out, func() {
		if zw != nil {
			_ = zw.Flush()
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})
	if zw != nil {
		_ = zw.Close()
	}
}

// acceptsGzip reports whether an Accept-Encoding takes gzip.
func acceptsGzip(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		q := strings.ReplaceAll(strings.TrimSpace(params), " ", "")
		return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
	}
	return false
}

// codeFollow moves the viewer's chat to the session its session was
// cleared into (/clear makes a new one, here or wherever the session is
// directed from), so the chat goes on with it as PW Code on the web does.
// Only a session the chat's own target reaches, as the viewer, in the same
// workspace, is followed.
func (m *Module) codeFollow(w http.ResponseWriter, r *http.Request) error {
	c, t, err := m.ownPWCode(r)
	if err != nil {
		return err
	}
	rb, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	v, _ := jsonv.Decode(rb.Raw)
	next := jsonv.String(jsonv.Path(v, "session"))
	if !pwcode.IsRouteID(next) {
		return pwErr(400, "Not a pw code session id.")
	}
	if next == c.PWCode.Session {
		return reqbody.OK(w, reqbody.O("session", next))
	}
	ctx := r.Context()
	was, err := pwcode.GetSession(ctx, t.client, c.PWCode.Session)
	if err != nil {
		return err
	}
	now, err := pwcode.GetSession(ctx, t.client, next)
	if err != nil {
		return err
	}
	if ws := jsonv.String(jsonv.Path(was, "workspace")); ws == "" || ws != jsonv.String(jsonv.Path(now, "workspace")) {
		return pwErr(409, "That session is not this chat's session cleared: it works in another folder.")
	}
	l := *c.PWCode
	l.Session = next
	m.Conv.SetPWCode(c.ID, &l)
	m.A.Log.Info("pw code chat: followed a cleared session", "user", userID(r), "conversation", c.ID, "from", c.PWCode.Session, "to", next, "site", l.Site)
	return reqbody.OK(w, reqbody.O("session", next))
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
	if key == "GET /claude-code/accounts" {
		if why := pwcode.ClaudeCodeOff(ctx); why != "" {
			return writeProblem(w, http.StatusForbidden, why)
		}
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
	if key == "GET /images/{ref}" || key == "GET /github-images/{asset}" {
		route := "/v1/sessions/" + url.PathEscape(id) + suffix
		if q := r.URL.RawQuery; q != "" {
			route += "?" + q
		}
		return m.codeImage(w, r, t, route)
	}
	if key == "GET /transcript" {
		if g, ok := t.client.(pwcode.StreamGetter); ok {
			route := "/v1/sessions/" + url.PathEscape(id) + suffix
			if q := r.URL.RawQuery; q != "" {
				route += "?" + q
			}
			return m.codeStream(w, r, t, g, id, route)
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
	case "POST /commands":
		if err := m.checkCommand(r, c, t, body); err != nil {
			return writeErr(w, err)
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
	if err != nil && takesSessionUp(r.Method, key) && isNotLive(err) {
		// A saved session the daemon has not taken up yet (the chat was
		// just opened, or the daemon let it go while idle): the palette
		// a command needs, a settings change or anything sent to it takes
		// it up first, as PW Code on the web's server does. Reading it does
		// not. The platform does not relay attach.
		if !t.relay {
			if _, aerr := pwcode.AttachSession(ctx, t.client, id); aerr == nil {
				v, err = t.client.Call(ctx, r.Method, route, body, 0)
			}
		}
		if err != nil && isNotLive(err) {
			return writeProblem(w, http.StatusConflict, notLiveYet)
		}
	}
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
	case "POST /plan":
		// Plan mode is a mode too: the session plans until a plan's answer
		// moves it on.
		m.logModeChange(r, c, t, "plan", "command")
	case "POST /commands":
		if jsonv.String(jsonv.Path(body, "name")) == "permissions" {
			if mode := strings.TrimSpace(jsonv.String(jsonv.Path(body, "args"))); mode != "" {
				m.logModeChange(r, c, t, mode, "command")
			}
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

// notLiveYet is what the chat says when the session could not be taken up
// for a request that needs it live: the daemon's own "attach it first"
// names a step the person cannot take here.
const notLiveYet = "The Studio is still connecting to this session. Try again in a moment."

// takesSessionUp is whether a request needs the session live: the palette
// (read only when a command is typed) and everything that changes or is
// sent to the session.
func takesSessionUp(method, key string) bool {
	return key == "GET /palette" || method != http.MethodGet
}

// isNotLive is whether the daemon answered that the session is not live.
func isNotLive(err error) bool {
	var de *pwcode.DaemonError
	return errors.As(err, &de) && notLiveMessage(de.Message)
}

// checkSettings refuses a settings change the Studio does not make: a
// field other than mode, model (with the allocation that bills it) and
// effort, a mode it does not know, or,
// for a relayed session, a mode above what the machine takes from
// ACTIVATE.
func (m *Module) checkSettings(r *http.Request, c *conversations.Conversation, t *pwTarget, body any) error {
	o := jsonv.Obj(body)
	if o == nil || o.Len() == 0 {
		return pwErr(400, "Name the mode, model or effort to change.")
	}
	for _, k := range o.Keys() {
		// The allocation that bills a model comes with the model: the
		// session view's picker sends it with every pick ("" for a model
		// that bills none).
		if k == "allocation" && o.Has("model") {
			continue
		}
		if !settingsFields[k] {
			return pwErr(400, "The Studio changes a session's mode, model and effort only; change "+k+" where the session runs.")
		}
	}
	if err := pwcode.ClaudeCodeRefused(r.Context(), o); err != nil {
		return err
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

// sessionCommands are the slash commands that manage the session or how its
// machine takes remote control: the Code page's, or the machine's own.
var sessionCommands = map[string]bool{"rename": true, "fork": true, "archive": true, "resume": true, "remote-control": true}

// checkCommand holds a slash command to the chat's rules: /permissions with
// a mode is a mode change, held to the same check as the picker's (and
// logged once it succeeds); one that manages the session is refused.
func (m *Module) checkCommand(r *http.Request, c *conversations.Conversation, t *pwTarget, body any) error {
	name := jsonv.String(jsonv.Path(body, "name"))
	if sessionCommands[name] {
		return pwErr(403, "/"+name+" is not sent from a chat: manage the session on the Code page, or where it runs.")
	}
	args := strings.TrimSpace(jsonv.String(jsonv.Path(body, "args")))
	// Claude Code accounts, where they are off: neither the command that
	// manages them nor a switch to one of their models.
	if name == "claude-code" || (name == "model" && pwcode.IsClaudeModel(args)) {
		if why := pwcode.ClaudeCodeOff(r.Context()); why != "" {
			return pwErr(403, why)
		}
	}
	if name != "permissions" {
		return nil
	}
	if args == "" {
		return nil
	}
	if _, ok := modeRank[args]; !ok {
		return pwErr(400, "Change the session's mode with the permission picker, or /permissions and one mode: read-only, accept-edits, auto or bypass-permissions.")
	}
	return m.checkMode(r, c, t, args)
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
		return reqbody.OK(w, codesessions.ClaudeCodeShown(r, mach))
	}
	name := l.MachineName
	var st any
	if h, err := m.pwcodeHereCached(r.Context(), r); err == nil && h != nil {
		st = h.status
		if name == "" {
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
	rec := jsonv.NewObject("id", directMachine, "name", firstNonEmpty(name, "this Studio's machine"), "kind", "local",
		"owner", owner, "status", "connected")
	// What the session view offers follows what the daemon allows, as the
	// platform's machine record carries it: signing an account in, removing
	// one, and the Claude Code integration itself.
	for _, k := range directFacts {
		if v := jsonv.Path(st, k); v != nil {
			rec.Set(k, v)
		}
	}
	return reqbody.OK(w, codesessions.ClaudeCodeShown(r, rec))
}

// directFacts are the daemon's status fields the machine record passes on.
// permissionModes is the modes the daemon accepts: without it the package
// offers its older list, which has no auto.
var directFacts = []string{"claudeCodeAllowed", "claudeInstalled", "claudeLogin", "claudeAccountRemove", "remoteStart", "remoteMaxPermissionMode", "permissionModes", "sessionImages", "version"}

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
					machines = append(machines, codesessions.ClaudeCodeShown(r, mach))
				}
			}
			for _, s := range ss {
				if c := own[jsonv.String(jsonv.Path(s, "id"))]; c != nil && c.PWCode.Relay && c.PWCode.Machine == jsonv.String(jsonv.Path(s, "machineId")) {
					sessionsOut = append(sessionsOut, s)
				}
			}
		}
	}
	// The Studio's own machine is listed whenever pw code is offered to
	// the person, chats or not: a new pw code chat's composer offers its
	// models before the first message makes a session there.
	if direct || m.pwcodeUnavailable(ctx, r) == "" {
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
