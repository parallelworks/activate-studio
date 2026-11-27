package codesessions

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/sessions"
)

// The Code page draws the person's pw code sessions with the packages PW
// Code on the web is built from (@parallelworks/code and
// @parallelworks/ui/ai), which call the platform's own routes. The Studio
// serves the ones the page uses under /api/code-page, with the platform's
// paths after /api/code, and sends each on as the person: to the platform
// with their own ACTIVATE credential (viewerKey), which relays it to the
// session's machine, or, on a Studio with no platform for them, to this
// computer's pw code. The Studio's own account never reaches the platform
// here.
//
// What may be sent is a short list: the listing, a machine's folders, a
// new session, two reads of the models and accounts a machine offers, and
// what PW Code on the web's session list and menu do with a session
// (pwcode.PageRoute). Anything else is refused before it is sent. A new
// session's permission mode, and the mode a plan's answer moves a session
// to, are held to the machine's remote ceiling and logged.

const pagePrefix = "/api/code-page"

// pageTimeout bounds one request the page sends on.
const pageTimeout = 30 * time.Second

// pageMaxAnswer is the most of a platform answer the page is passed.
const pageMaxAnswer = 16 << 20

// createFields are the fields of a new session the Studio passes on, the
// platform's own CreateSessionRequest.
var createFields = []string{"workspace", "model", "effort", "allocation", "claudeAccount", "permissionMode", "prompt"}

// StartTools is a new session's choice to add the Studio's tools to pw code
// on its machine first (chat, bridgeAtStart). It is not sent on.
const StartTools = "studioTools"

// modeRank orders the permission modes by authority; plan is not a mode a
// session starts in from here.
var modeRank = map[string]int{"read-only": 0, "accept-edits": 1, "auto": 2, "bypass-permissions": 3}

// defaultCeiling is the ceiling for a machine that names none (pw code's
// permissions.DefaultRemoteCeiling).
const defaultCeiling = "accept-edits"

func registerPage(mux *http.ServeMux, s *routes) {
	mux.HandleFunc("GET "+pagePrefix+"/sessions", s.pageListing)
	mux.HandleFunc("GET "+pagePrefix+"/machines/{machine}/directories", s.pageDirectories)
	mux.HandleFunc("POST "+pagePrefix+"/machines/{machine}/sessions", s.pageCreate)
	mux.HandleFunc("GET "+pagePrefix+"/machines/{machine}/claude-code/models", s.pageClaude)
	mux.HandleFunc("GET "+pagePrefix+"/machines/{machine}/claude-code/accounts", s.pageClaude)
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		mux.HandleFunc(m+" "+pagePrefix+"/machines/{machine}/sessions/{id}", s.pageSession)
		mux.HandleFunc(m+" "+pagePrefix+"/machines/{machine}/sessions/{id}/{rest...}", s.pageSession)
	}
	mux.HandleFunc("GET "+pagePrefix+"/openai/models", s.pageModels)
	mux.HandleFunc("GET "+pagePrefix+"/where", s.pageWhere)
	mux.HandleFunc("GET "+pagePrefix+"/icon", s.pageIcon)
}

// pageWhere says where the page's sessions are: on this computer, or on
// the platform, whose Code page each session also opens on.
func (s *routes) pageWhere(w http.ResponseWriter, r *http.Request) {
	if s.localDaemon(r) != nil {
		writeJSON(w, http.StatusOK, jsonv.NewObject("local", true, "codeUrl", ""))
		return
	}
	code := ""
	if o := s.p.Origin(); o != "" {
		code = strings.TrimRight(o, "/") + "/code"
	}
	// No ACTIVATE sign-in of their own: the page sends them to Settings,
	// Accounts, rather than calling routes that would refuse.
	// With no platform at all, signing in under Accounts is what connects one.
	_, err := viewerKey(r, s.p)
	writeJSON(w, http.StatusOK, jsonv.NewObject("local", false, "codeUrl", code, "signIn", code == "" || err != nil))
}

// writeProblem answers as the platform does (application/problem+json), so
// the page's error words read it.
func writeProblem(w http.ResponseWriter, status int, msg string) {
	if status < 400 {
		status = http.StatusBadGateway
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	b, _ := jsonv.Marshal(jsonv.NewObject("title", http.StatusText(status), "status", status, "detail", msg))
	_, _ = w.Write(b)
}

// apiErr is err as the Studio's own refusal, or nil.
func apiErr(err error) *apierr.Error {
	var e *apierr.Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

func writeErr(w http.ResponseWriter, err error) {
	status, msg := http.StatusBadGateway, err.Error()
	var de *pwcode.DaemonError
	if errors.As(err, &de) {
		if de.Status != 0 {
			status = de.Status
		}
		msg = strings.TrimPrefix(de.Message, "pw code daemon: ")
	}
	if ae := apiErr(err); ae != nil {
		status, msg = ae.Status, ae.Message
	}
	writeProblem(w, status, msg)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := jsonv.Marshal(v)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "The answer could not be read.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// crossSite is a request that changes something sent by a page of another
// site: a browser names the page's origin on every such request.
func crossSite(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return false
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return true
	}
	return r.Header.Get("Origin") != "" && !sessions.SameOrigin(r)
}

// CrossSite is crossSite, for the chat's routes the Code page's pane uses.
func CrossSite(r *http.Request) bool { return crossSite(r) }

// pageTarget is where the page's request for machine goes: this computer's
// pw code (local, with the machine it lists and whether the viewer is held
// to their own sessions), or the platform as the viewer (key).
type pageTarget struct {
	local   pwcode.Client
	machine Machine
	own     map[string]bool
	only    bool
	key     string
}

func (s *routes) pageTarget(r *http.Request, machine string) (*pageTarget, error) {
	if c := s.localDaemon(r); c != nil {
		m, err := s.localMachine(r.Context(), c)
		if err != nil {
			return nil, err
		}
		if machine != "" && machine != m.ID {
			return nil, platform.KbError(404, "That machine is not this computer, the only one this Studio lists for you.")
		}
		own, only := s.mine(r)
		return &pageTarget{local: c, machine: m, own: own, only: only}, nil
	}
	if s.p.Origin() == "" {
		return nil, platform.KbError(409, "pw code sessions are listed on an ACTIVATE platform. Sign in with ACTIVATE under Settings, Accounts.")
	}
	key, err := viewerKey(r, s.p)
	if err != nil {
		return nil, err
	}
	return &pageTarget{key: key}, nil
}

// forward sends one request to the platform as the viewer and passes its
// answer back as it came. A refusal of the credential or a platform
// without the feature is said in the person's terms.
func (s *routes) forward(w http.ResponseWriter, r *http.Request, t *pageTarget, method, path string, body []byte) {
	ctx, cancel := context.WithTimeout(r.Context(), pageTimeout)
	defer cancel()
	header := map[string]string{"Authorization": "Bearer " + t.key, "Accept": "application/json"}
	var rd io.Reader
	if body != nil {
		header["Content-Type"] = "application/json"
		rd = bytes.NewReader(body)
	}
	res, done, err := platform.Request(ctx, method, strings.TrimRight(s.p.Origin(), "/")+path, header, rd, pageTimeout)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "The platform did not answer.")
		return
	}
	defer done()
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, pageMaxAnswer))
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "The platform's answer was cut off.")
		return
	}
	switch res.StatusCode {
	case http.StatusUnauthorized:
		writeProblem(w, http.StatusUnauthorized, "The platform refused your credential; sign in again, or renew your API key under Settings, Accounts.")
		return
	}
	if method == http.MethodGet && path == "/api/code/sessions" && res.StatusCode == http.StatusOK {
		raw = claudeCodeListing(r, raw)
		raw = studioHomeListing(raw, s.a.Cfg.AuthEnabled())
		if Listed != nil {
			if v, err := jsonv.Decode(raw); err == nil {
				Listed(r, jsonv.Arr(jsonv.Path(v, "sessions")))
			}
		}
	}
	ct := res.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(res.StatusCode)
	_, _ = w.Write(raw)
}

// callLocal sends one request to this computer's pw code and writes its
// answer.
func callLocal(w http.ResponseWriter, r *http.Request, c pwcode.Client, method, route string, body any) {
	v, err := c.Call(r.Context(), method, route, body, pageTimeout)
	if err != nil {
		writeErr(w, err)
		return
	}
	if v == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// localMachineView is this computer as the platform would list it: a
// computer whose sessions start from here only for a Studio with no
// sign-in, where every session is the one person's.
func (t *pageTarget) localMachineView() *jsonv.Object {
	m := t.machine
	o := jsonv.NewObject("id", m.ID, "name", m.Name, "kind", "local", "owner", "", "hostname", m.Hostname,
		"status", m.Status, "os", runtime.GOOS, "daemonId", m.DaemonID, "claudeCodeAllowed", false, "remoteStart", !t.only,
		"directories", !t.only, "sessionGroups", true, StudioHome, true)
	// The new session form offers the modes the daemon accepts; without
	// them the package falls back to a list that has no auto.
	if len(m.PermissionModes) > 0 {
		o.Set("permissionModes", m.PermissionModes)
	}
	return o
}

// pageListing is GET /api/code/sessions: the viewer's machines and
// sessions as the platform lists them for them, or this computer's.
func (s *routes) pageListing(w http.ResponseWriter, r *http.Request) {
	t, err := s.pageTarget(r, "")
	if err != nil {
		writeErr(w, err)
		return
	}
	if t.local == nil {
		s.forward(w, r, t, http.MethodGet, "/api/code/sessions", nil)
		return
	}
	machines, sessions, err := t.localListing(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if Listed != nil {
		Listed(r, sessions)
	}
	writeJSON(w, http.StatusOK, jsonv.NewObject("machines", machines, "sessions", sessions))
}

// localListing is this computer and its sessions the viewer may see.
func (t *pageTarget) localListing(r *http.Request) (machines, sessions []any, err error) {
	list, err := pwcode.ListSessions(r.Context(), t.local)
	if err != nil {
		return nil, nil, platform.KbError(502, "pw code on this computer did not answer: "+err.Error())
	}
	rows := jsonv.Arr(list)
	if rows == nil {
		rows = jsonv.Arr(jsonv.Path(list, "sessions"))
	}
	sessions = []any{}
	for _, x := range rows {
		o := jsonv.Obj(x)
		if o == nil || (t.only && !t.own[jsonv.String(jsonv.Path(o, "id"))]) {
			continue
		}
		o.Set("machineId", t.machine.ID)
		sessions = append(sessions, o)
	}
	return []any{t.localMachineView()}, sessions, nil
}

// Listing is the machines and sessions the Code page lists for the person
// r is from (GET /api/code-page/sessions), for the Studio's own use: what
// waits on them is read from the same listing the page draws.
func Listing(r *http.Request) (machines, sessions []any, err error) {
	s := registered.Load()
	if s == nil {
		return nil, nil, platform.KbError(409, "pw code sessions are not listed here.")
	}
	t, err := s.pageTarget(r, "")
	if err != nil {
		return nil, nil, err
	}
	if t.local != nil {
		return t.localListing(r)
	}
	ms, ss, err := PlatformListing(r, s.p)
	if err != nil {
		return nil, nil, err
	}
	machines, sessions = []any{}, []any{}
	for _, m := range ms {
		machines = append(machines, ClaudeCodeShown(r, m))
	}
	for _, x := range ss {
		sessions = append(sessions, x)
	}
	return machines, sessions, nil
}

// pageDirectories lists the folders of one directory on the machine, for
// the folder a new session starts in.
func (s *routes) pageDirectories(w http.ResponseWriter, r *http.Request) {
	machine := r.PathValue("machine")
	if !idRe.MatchString(machine) {
		writeProblem(w, http.StatusBadRequest, "machine is required")
		return
	}
	t, err := s.pageTarget(r, machine)
	if err != nil {
		writeErr(w, err)
		return
	}
	q := url.Values{"path": {r.URL.Query().Get("path")}, "filter": {r.URL.Query().Get("filter")}}.Encode()
	if t.local != nil {
		if t.only {
			writeProblem(w, http.StatusForbidden, "Sessions on this computer start from a chat in Assistant, which works in your own folder.")
			return
		}
		callLocal(w, r, t.local, http.MethodGet, "/v1/directories?"+q, nil)
		return
	}
	s.forward(w, r, t, http.MethodGet, "/api/code/machines/"+platform.EncodeURIComponent(machine)+"/directories?"+q, nil)
}

// pageClaude reads the Claude models or accounts the machine offers a new
// session. Only the platform relays them.
func (s *routes) pageClaude(w http.ResponseWriter, r *http.Request) {
	machine := r.PathValue("machine")
	if !idRe.MatchString(machine) {
		writeProblem(w, http.StatusBadRequest, "machine is required")
		return
	}
	t, err := s.pageTarget(r, machine)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Where Claude Code accounts are off for the person, there are none.
	if t.local != nil || pwcode.ClaudeCodeOff(r.Context()) != "" {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	what := "models"
	if strings.HasSuffix(r.URL.Path, "/accounts") {
		what = "accounts"
	}
	path := "/api/code/machines/" + platform.EncodeURIComponent(machine) + "/claude-code/" + what
	if a := r.URL.Query().Get("account"); a != "" {
		path += "?" + url.Values{"account": {a}}.Encode()
	}
	s.forward(w, r, t, http.MethodGet, path, nil)
}

// pageModels is the platform's model list as the viewer, the models a new
// session may run on besides a machine's own.
func (s *routes) pageModels(w http.ResponseWriter, r *http.Request) {
	t, err := s.pageTarget(r, "")
	if err != nil {
		if apiErr(err) != nil && apiErr(err).Status == http.StatusConflict {
			writeJSON(w, http.StatusOK, jsonv.NewObject("object", "list", "data", []any{}))
			return
		}
		writeErr(w, err)
		return
	}
	if t.local != nil {
		writeJSON(w, http.StatusOK, jsonv.NewObject("object", "list", "data", []any{}))
		return
	}
	s.forward(w, r, t, http.MethodGet, "/api/openai/v1/models", nil)
}

// readJSONObject is the request's body when it is a JSON object, else an
// empty object for no body.
func readJSONObject(r *http.Request) (*jsonv.Object, error) {
	v, err := platform.Body(r)
	if err != nil {
		return nil, platform.KbError(400, "The body is not JSON.")
	}
	if v == nil {
		return jsonv.NewObject(), nil
	}
	o := jsonv.Obj(v)
	if o == nil {
		return nil, platform.KbError(400, "The body is not a JSON object.")
	}
	return o, nil
}

// machineCeiling is the most authority a session on machine may be given
// from here: the machine's remote ceiling as the platform lists it for the
// viewer. An unreadable listing, or a ceiling the Studio does not rank,
// allows read-only.
func (s *routes) machineCeiling(r *http.Request, machine string) (string, string) {
	m, err := PlatformMachine(r, s.p, machine)
	return ceilingOf(m, err)
}

// ceilingOf is machineCeiling for the machine as the platform listed it,
// or the error reading it.
func ceilingOf(m *jsonv.Object, err error) (string, string) {
	if err != nil {
		return "read-only", "the Studio could not read the machine's ceiling"
	}
	c := jsonv.String(jsonv.Path(m, "remoteMaxPermissionMode"))
	if c == "" {
		return defaultCeiling, ""
	}
	if _, ok := modeRank[c]; !ok {
		return "read-only", "the Studio does not know the machine's ceiling " + c
	}
	return c, ""
}

// checkMode refuses a mode above the machine's ceiling, or one the Studio
// does not know.
func (s *routes) checkMode(r *http.Request, machine, mode string) error {
	return checkModeOn(mode, func() (string, string) { return s.machineCeiling(r, machine) })
}

func checkModeOn(mode string, machineCeiling func() (string, string)) error {
	want, ok := modeRank[mode]
	if !ok {
		return platform.KbError(400, "The Studio does not start a session in "+mode+" mode.")
	}
	ceiling, why := machineCeiling()
	if want > modeRank[ceiling] {
		msg := "This machine lets sessions be given at most " + ceiling + " from elsewhere"
		if why != "" {
			msg = "The Studio allows at most read-only here: " + why
		}
		return platform.KbError(403, msg+". Choose a narrower mode, or start the session where it runs.")
	}
	return nil
}

func (s *routes) viewerName(r *http.Request) string {
	if v, err := s.p.ViewerOf(r); err == nil && v != nil {
		return v.UserID()
	}
	return ""
}

// pageCreate starts a session on the machine with the person's choices
// and, when there is one, its first message.
func (s *routes) pageCreate(w http.ResponseWriter, r *http.Request) {
	if crossSite(r) {
		writeProblem(w, http.StatusForbidden, "Sessions start from the Studio's own pages.")
		return
	}
	machine := r.PathValue("machine")
	if !idRe.MatchString(machine) {
		writeProblem(w, http.StatusBadRequest, "machine is required")
		return
	}
	in, err := readJSONObject(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	body := jsonv.NewObject()
	for _, k := range createFields {
		if v, ok := in.Get(k); ok && v != nil {
			if _, isStr := v.(string); !isStr {
				writeProblem(w, http.StatusBadRequest, k+" must be text.")
				return
			}
			body.Set(k, v)
		}
	}
	ws := jsonv.String(jsonv.Path(body, "workspace"))
	if !strings.HasPrefix(ws, "/") || strings.ContainsAny(ws, "\x00\r\n") {
		writeProblem(w, http.StatusBadRequest, "The folder must be a full path on the machine.")
		return
	}
	if err := pwcode.ClaudeCodeRefused(r.Context(), body); err != nil {
		writeErr(w, err)
		return
	}
	// For the chat only: the platform and pw code have no such field.
	tools, choseTools := in.Get(StartTools)
	if _, ok := tools.(bool); choseTools && !ok {
		writeProblem(w, http.StatusBadRequest, StartTools+" must be true or false.")
		return
	}
	mode := jsonv.String(jsonv.Path(body, "permissionMode"))
	if mode == "" {
		mode = "read-only"
		body.Set("permissionMode", mode)
	}
	t, err := s.pageTarget(r, machine)
	if err != nil {
		writeErr(w, err)
		return
	}
	if t.local != nil {
		if t.only {
			writeProblem(w, http.StatusForbidden, "Sessions on this computer start from a chat in Assistant, which works in your own folder.")
			return
		}
		if _, ok := modeRank[mode]; !ok {
			writeProblem(w, http.StatusBadRequest, "The Studio does not start a session in "+mode+" mode.")
			return
		}
		if s.startInChat(w, r, machine, nil, body, mode) {
			return
		}
		s.a.Log.Info("code page: started a session", "machine", machine, "mode", mode, "via", "this computer")
		callLocal(w, r, t.local, http.MethodPost, "/v1/sessions", body)
		return
	}
	mach, machErr := PlatformMachine(r, s.p, machine)
	if err := checkModeOn(mode, func() (string, string) { return ceilingOf(mach, machErr) }); err != nil {
		writeErr(w, err)
		return
	}
	if machErr == nil {
		chat := body
		if choseTools {
			chat = body.Clone()
			chat.Set(StartTools, tools)
		}
		if s.startInChat(w, r, machine, mach, chat, mode) {
			return
		}
	}
	raw, _ := jsonv.Marshal(body)
	s.a.Log.Info("code page: started a session", "user", s.viewerName(r), "machine", machine, "mode", mode, "via", "platform")
	s.forward(w, r, t, http.MethodPost, "/api/code/machines/"+platform.EncodeURIComponent(machine)+"/sessions", raw)
}

// Listed and Renamed, set by the chat, are told of the viewer's sessions as
// the page's listing has them, and of a name the page gave one, so a chat
// and its session keep one name.
var (
	Listed  func(r *http.Request, rows []any)
	Renamed func(r *http.Request, id, name string)
)

// answered keeps the status of an answer.
type answered struct {
	http.ResponseWriter
	status int
}

func (a *answered) WriteHeader(s int) { a.status = s; a.ResponseWriter.WriteHeader(s) }

// StartInChat, set by the chat, starts a Code page session as a chat's is
// started, with the Studio's tools and instructions, and makes its chat:
// mach is the machine as the platform lists it to the viewer (nil for this
// computer on a Studio with no platform), and body the request, its mode
// already held to the machine's ceiling. It answers the session, or false
// where a chat is not offered here, and the page's own start goes on.
var StartInChat func(r *http.Request, machine string, mach, body *jsonv.Object) (*jsonv.Object, bool, error)

// startInChat answers the request with StartInChat's session; false where
// it made none.
func (s *routes) startInChat(w http.ResponseWriter, r *http.Request, machine string, mach, body *jsonv.Object, mode string) bool {
	if StartInChat == nil {
		return false
	}
	v, ok, err := StartInChat(r, machine, mach, body)
	if err != nil {
		writeErr(w, err)
		return true
	}
	if !ok {
		return false
	}
	s.a.Log.Info("code page: started a session", "user", s.viewerName(r), "machine", machine, "mode", mode, "via", "chat")
	writeJSON(w, http.StatusCreated, v)
	return true
}

// pageSession is one of pwcode.PageRoute's requests about a session.
func (s *routes) pageSession(w http.ResponseWriter, r *http.Request) {
	if crossSite(r) {
		writeProblem(w, http.StatusForbidden, "Sessions change from the Studio's own pages.")
		return
	}
	machine, id := r.PathValue("machine"), r.PathValue("id")
	if !idRe.MatchString(machine) || !idRe.MatchString(id) {
		writeProblem(w, http.StatusNotFound, "No such session.")
		return
	}
	suffix := ""
	if rest := r.PathValue("rest"); rest != "" {
		suffix = "/" + rest
	}
	key := pwcode.PageRoute(r.Method, suffix)
	if key == "" {
		writeProblem(w, http.StatusNotFound, "The Studio's Code page does not send this to a session; do it in PW Code on the web or where the session runs.")
		return
	}
	var body *jsonv.Object
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		var err error
		if body, err = readJSONObject(r); err != nil {
			writeErr(w, err)
			return
		}
	}
	t, err := s.pageTarget(r, machine)
	if err != nil {
		writeErr(w, err)
		return
	}
	if t.local != nil && t.only && !t.own[id] {
		writeErr(w, notYours())
		return
	}
	if t.local != nil && t.only && key == "POST /fork" {
		writeProblem(w, http.StatusForbidden, "A copy of a session on this computer would not be yours here; continue the session in a chat instead.")
		return
	}
	mode := ""
	if key == "POST /approvals/{approval}" && body != nil && body.Has("mode") {
		mode = jsonv.String(jsonv.Path(body, "mode"))
		if t.local == nil {
			if err := s.checkMode(r, machine, mode); err != nil {
				writeErr(w, err)
				return
			}
		}
	}
	query := ""
	if q := r.URL.RawQuery; q != "" {
		query = "?" + q
	}
	switch key {
	case "DELETE ", "POST /archive", "POST /stop", "POST /fork", "POST /worktree/leave":
		s.a.Log.Info("code page: "+strings.ToLower(key), "user", s.viewerName(r), "machine", machine, "session", id)
	}
	if mode != "" {
		s.a.Log.Info("pw code: permission mode changed", "user", s.viewerName(r), "machine", machine, "session", id, "mode", mode, "via", "plan answer on the Code page")
	}
	if key == "POST /rename" && Renamed != nil && body != nil {
		a := &answered{ResponseWriter: w, status: http.StatusOK}
		w = a
		name := jsonv.String(jsonv.Path(body, "name"))
		defer func() {
			if a.status < 300 {
				Renamed(r, id, name)
			}
		}()
	}
	if t.local != nil {
		var b any
		if body != nil {
			b = body
		}
		callLocal(w, r, t.local, r.Method, "/v1/sessions/"+url.PathEscape(id)+suffix+query, b)
		return
	}
	var raw []byte
	if body != nil {
		raw, _ = jsonv.Marshal(body)
	}
	s.forward(w, r, t, r.Method, "/api/code/machines/"+platform.EncodeURIComponent(machine)+"/sessions/"+platform.EncodeURIComponent(id)+suffix+query, raw)
}
