// Package codesessions shows the viewer's pw code sessions on the
// platform: the machines and sessions the platform's Code page lists, and
// one session's transcript, read through the platform API as the viewer,
// with a link to each session's page on the platform. A call a session
// waits to have approved can be approved or denied there (approve.go);
// otherwise the page reads. A session on the machine whose pw code daemon this Studio's
// chat reaches for the viewer can be continued in a Studio chat
// (chat/pwcodecontinue.go), which looks the session up here first.
package codesessions

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/sessions"
)

// Machine is a machine as the platform lists it for the viewer. DaemonID
// is the machine id its pw code daemon reports (GET /v1/status machineId),
// by which the Studio knows the machine its own pw code runs on.
type Machine struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Owner    string `json:"owner,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
	DaemonID string `json:"daemonId,omitempty"`
}

// approval is a decision a session is waiting on, as its daemon reports
// it: kind confirm is a call to approve or deny (Tool and Command say
// which), ask is a question and plan a finished plan, both answered where
// the session runs.
type approval struct {
	ID      string `json:"id"`
	Kind    string `json:"kind,omitempty"`
	Tool    string `json:"tool,omitempty"`
	Command string `json:"command,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Agent   string `json:"agent,omitempty"`
	// CommandCut is set when Command is longer than the page is sent
	// (maxApprovalCommand); the page then offers Deny alone, since
	// Approve would run text the person did not see.
	CommandCut bool `json:"commandCut,omitempty"`
	// CommandLength is the whole command's length in characters.
	CommandLength int `json:"commandLength,omitempty"`
}

type session struct {
	MachineID      string     `json:"machineId"`
	ID             string     `json:"id"`
	Name           string     `json:"name,omitempty"`
	AutoName       string     `json:"autoName,omitempty"`
	Preview        string     `json:"preview,omitempty"`
	Model          string     `json:"model,omitempty"`
	Workspace      string     `json:"workspace,omitempty"`
	CreatedAt      string     `json:"createdAt,omitempty"`
	UpdatedAt      string     `json:"updatedAt,omitempty"`
	Messages       int        `json:"messages"`
	Status         string     `json:"status"`
	PermissionMode string     `json:"permissionMode,omitempty"`
	RemoteControl  bool       `json:"remoteControl,omitempty"`
	LiveHost       string     `json:"liveHost,omitempty"`
	Branch         string     `json:"branch,omitempty"`
	PullRequests   []string   `json:"pullRequests,omitempty"`
	ArchivedAt     *string    `json:"archivedAt,omitempty"`
	PinnedAt       *string    `json:"pinnedAt,omitempty"`
	LastError      string     `json:"lastError,omitempty"`
	Approvals      []approval `json:"approvals,omitempty"`
}

// View is a session as the Studio lists it.
type View struct {
	MachineID      string   `json:"machineId"`
	Machine        string   `json:"machine"`
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Model          string   `json:"model,omitempty"`
	Workspace      string   `json:"workspace,omitempty"`
	CreatedAt      string   `json:"createdAt,omitempty"`
	UpdatedAt      string   `json:"updatedAt,omitempty"`
	Messages       int      `json:"messages"`
	Status         string   `json:"status"`
	PermissionMode string   `json:"permissionMode,omitempty"`
	Branch         string   `json:"branch,omitempty"`
	PullRequests   []string `json:"pullRequests,omitempty"`
	Archived       bool     `json:"archived,omitempty"`
	Pinned         bool     `json:"pinned,omitempty"`
	Waiting        int      `json:"waiting,omitempty"`
	LastError      string   `json:"lastError,omitempty"`
	LiveHost       string   `json:"liveHost,omitempty"`
	URL            string   `json:"url"`
}

type historyItem struct {
	Kind     string `json:"kind"`
	Text     string `json:"text,omitempty"`
	ToolName string `json:"toolName,omitempty"`
	ToolArgs string `json:"toolArgs,omitempty"`
	TurnID   string `json:"turnId,omitempty"`
}

type detail struct {
	session
	History []historyItem `json:"history"`
}

// Title is the platform's session label: its name, the name it was given
// from its first message, or that message.
func Title(name, autoName, preview string) string {
	for _, s := range []string{name, autoName, preview} {
		if s = strings.TrimSpace(s); s != "" {
			return clip(oneLine(s), 120)
		}
	}
	return "Untitled session"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// URL is the session's page on the platform.
func URL(origin, machineID, id string) string {
	return strings.TrimRight(origin, "/") + "/code/" + platform.EncodeURIComponent(machineID) + "/" + platform.EncodeURIComponent(id)
}

func view(s session, names map[string]string, origin string) View {
	v := View{
		MachineID: s.MachineID, Machine: names[s.MachineID], ID: s.ID,
		Title: Title(s.Name, s.AutoName, s.Preview), Model: s.Model, Workspace: s.Workspace,
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Messages: s.Messages, Status: s.Status,
		PermissionMode: s.PermissionMode, Branch: s.Branch, PullRequests: s.PullRequests,
		Archived: s.ArchivedAt != nil, Pinned: s.PinnedAt != nil, Waiting: len(s.Approvals),
		LastError: s.LastError, LiveHost: s.LiveHost, URL: URL(origin, s.MachineID, s.ID),
	}
	if v.Machine == "" {
		v.Machine = s.MachineID
	}
	return v
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

type routes struct {
	a *app.App
	p *platform.Platform
}

// Register serves GET /api/code-sessions and /api/code-sessions/item, and
// POST /api/code-sessions/approve.
func Register(mux *http.ServeMux, a *app.App, p *platform.Platform) {
	s := &routes{a: a, p: p}
	registered.Store(s)
	a.Providers.LocalCode = func(r *http.Request) bool { return localCode(r, a, p) }
	h := func(f func(w http.ResponseWriter, r *http.Request) (any, error)) http.HandlerFunc {
		return platform.Handler(a.Log, f)
	}
	mux.HandleFunc("GET /api/code-sessions", h(s.list))
	mux.HandleFunc("GET /api/code-sessions/item", h(s.item))
	mux.HandleFunc("POST /api/code-sessions/approve", h(s.approve))
}

// signedIn is whether the Studio has sign-in on for this viewer: then the
// Code page works as the person alone.
func signedIn(v *platform.Viewer) bool { return v != nil && (v.AuthEnabled || v.User != nil) }

// viewerKey is the key the Code page reads and answers with. With sign-in
// on it is the person's own ACTIVATE credential and never the
// deployment's: that account runs other people's delegated and standing
// agents, so its list would show their transcripts and let anyone answer
// their waiting calls (as sessions.DesktopKey does for desktops).
func viewerKey(r *http.Request, p *platform.Platform) (string, error) { return ViewerKey(r, p) }

// ViewerKey is viewerKey, for the chat that drives a session on another
// machine through the platform with the same credential.
func ViewerKey(r *http.Request, p *platform.Platform) (string, error) {
	v, err := p.ViewerOf(r)
	if err != nil {
		return "", err
	}
	if signedIn(v) {
		if v.UserID() == "" {
			return "", platform.KbError(401, "Sign in to the Studio to see your pw code sessions.")
		}
		if k := v.PlatformKey(); k != "" {
			return k, nil
		}
		return "", platform.KbError(409, "The Code page lists your pw code sessions with your own ACTIVATE sign-in. Sign in with ACTIVATE under Settings, Accounts, then try again.")
	}
	if k := sessions.KeyFor(p, v); k != "" {
		return k, nil
	}
	return "", platform.KbError(409, "No platform credential: add your ACTIVATE API key under Settings, Accounts.")
}

func (s *routes) fetch(r *http.Request, path string, out any) error { return fetch(r, s.p, path, out) }

// fetch reads a platform API path as the viewer, explaining the answers
// that mean the feature is not there for them.
func fetch(r *http.Request, p *platform.Platform, path string, out any) error {
	if p.Origin() == "" {
		return platform.KbError(409, "This Studio is not connected to an ACTIVATE platform, where pw code sessions are listed.")
	}
	key, err := viewerKey(r, p)
	if err != nil {
		return err
	}
	res := p.APIFetch(r.Context(), path, key, 30*time.Second)
	switch {
	case res == nil:
		return platform.KbError(502, "The platform did not answer.")
	case res.Status == http.StatusUnauthorized:
		return platform.KbError(401, "The platform refused your credential; sign in again, or renew your API key under Settings, Accounts.")
	case res.Status == http.StatusForbidden || res.Status == http.StatusNotFound && strings.HasSuffix(path, "/sessions"):
		return platform.KbError(404, "PW Code on the web is not turned on for your account on this platform. It is a preview; ask your administrator to enable it.")
	case res.Status == http.StatusNotFound:
		return platform.KbError(404, "That session is no longer on its machine, or the machine is not reachable.")
	case !res.OK:
		return platform.KbError(502, "The platform answered "+http.StatusText(res.Status)+" for the pw code sessions.")
	}
	if err := json.Unmarshal(res.Raw, out); err != nil {
		return platform.KbError(502, "The platform's answer was not the sessions it should be.")
	}
	return nil
}

type listing struct {
	Machines []Machine `json:"machines"`
	Sessions []session `json:"sessions"`
}

// Find is one of the viewer's sessions as the platform lists it, read as
// the viewer, with its machine; a 404 when the platform does not list it
// for them.
func Find(r *http.Request, p *platform.Platform, machineID, id string) (*Machine, *View, error) {
	if !idRe.MatchString(machineID) || !idRe.MatchString(id) {
		return nil, nil, platform.KbError(400, "machine and session are required")
	}
	if s := registered.Load(); s != nil {
		if c := s.localDaemon(r); c != nil {
			return s.localFind(r, c, machineID, id)
		}
	}
	var body listing
	if err := fetch(r, p, "/api/code/sessions", &body); err != nil {
		return nil, nil, err
	}
	var mach *Machine
	names := map[string]string{}
	for i, m := range body.Machines {
		names[m.ID] = m.Name
		if m.ID == machineID {
			mach = &body.Machines[i]
		}
	}
	for _, x := range body.Sessions {
		if x.MachineID == machineID && x.ID == id && mach != nil {
			v := view(x, names, p.Origin())
			return mach, &v, nil
		}
	}
	return nil, nil, platform.KbError(404, "The platform does not list that pw code session as yours.")
}

func (s *routes) list(w http.ResponseWriter, r *http.Request) (any, error) {
	if c := s.localDaemon(r); c != nil {
		return s.localList(r, c)
	}
	var body listing
	if err := s.fetch(r, "/api/code/sessions", &body); err != nil {
		return nil, err
	}
	origin := s.p.Origin()
	names := map[string]string{}
	for _, m := range body.Machines {
		names[m.ID] = m.Name
	}
	out := []View{}
	for _, x := range body.Sessions {
		out = append(out, view(x, names, origin))
	}
	machines := body.Machines
	if machines == nil {
		machines = []Machine{}
	}
	return map[string]any{"machines": machines, "sessions": out, "codeUrl": strings.TrimRight(origin, "/") + "/code"}, nil
}

// Transcript limits: the latest items, each cut to a readable length.
const (
	maxItems      = 400
	maxText       = 20000
	maxToolResult = 1500
	maxToolArgs   = 600
	// An approval's command is sent whole up to this many characters:
	// Approve runs exactly what the person was shown.
	maxApprovalCommand = 64 << 10
)

func (s *routes) item(w http.ResponseWriter, r *http.Request) (any, error) {
	mid, id := platform.Q(r, "machine"), platform.Q(r, "id")
	if !idRe.MatchString(mid) || !idRe.MatchString(id) {
		return nil, platform.KbError(400, "machine and id are required")
	}
	var d detail
	origin := s.p.Origin()
	c := s.localDaemon(r)
	if c != nil {
		ld, err := s.localItem(r, c, mid, id)
		if err != nil {
			return nil, err
		}
		d = *ld
	} else if err := s.fetch(r, "/api/code/machines/"+platform.EncodeURIComponent(mid)+"/sessions/"+platform.EncodeURIComponent(id), &d); err != nil {
		return nil, err
	}
	if d.MachineID == "" {
		d.MachineID = mid
	}
	name := platform.Q(r, "machineName")
	if c != nil {
		name, origin = LocalName, ""
	}
	v := view(d.session, map[string]string{mid: name}, origin)
	if c != nil {
		v.URL = ""
	}
	items := d.History
	dropped := 0
	if len(items) > maxItems {
		dropped = len(items) - maxItems
		items = items[dropped:]
	}
	hist := make([]historyItem, 0, len(items))
	for _, it := range items {
		switch it.Kind {
		case "thinking", "turn_end", "goal_progress", "compaction":
			continue
		case "tool_result":
			it.Text = clip(it.Text, maxToolResult)
		default:
			it.Text = clip(it.Text, maxText)
		}
		it.ToolArgs = clip(it.ToolArgs, maxToolArgs)
		hist = append(hist, it)
	}
	waiting := make([]approval, 0, len(d.Approvals))
	for _, a := range d.Approvals {
		if n := len([]rune(a.Command)); n > maxApprovalCommand {
			a.Command, a.CommandCut, a.CommandLength = string([]rune(a.Command)[:maxApprovalCommand]), true, n
		} else if a.Command != "" {
			a.CommandLength = n
		}
		a.Reason = clip(oneLine(a.Reason), 300)
		waiting = append(waiting, a)
	}
	return map[string]any{"session": v, "history": hist, "earlier": dropped, "approvals": waiting}, nil
}

// PlatformMachine is one of the viewer's machines as the platform lists it
// (an AgentMachine, with its remote permission ceiling), read as the viewer.
func PlatformMachine(r *http.Request, p *platform.Platform, machineID string) (*jsonv.Object, error) {
	var body struct {
		Machines []json.RawMessage `json:"machines"`
	}
	if err := fetch(r, p, "/api/code/sessions", &body); err != nil {
		return nil, err
	}
	for _, raw := range body.Machines {
		v, err := jsonv.Decode(raw)
		if o := jsonv.Obj(v); err == nil && o != nil && jsonv.String(jsonv.Path(o, "id")) == machineID {
			return o, nil
		}
	}
	return nil, platform.KbError(404, "The platform does not list that machine as yours.")
}

// PlatformListing is the viewer's machines and sessions as the platform
// lists them (/api/code/sessions), each as the platform gives it.
func PlatformListing(r *http.Request, p *platform.Platform) (machines, sessions []*jsonv.Object, err error) {
	var body struct {
		Machines []json.RawMessage `json:"machines"`
		Sessions []json.RawMessage `json:"sessions"`
	}
	if err := fetch(r, p, "/api/code/sessions", &body); err != nil {
		return nil, nil, err
	}
	decode := func(raws []json.RawMessage) []*jsonv.Object {
		var out []*jsonv.Object
		for _, raw := range raws {
			if v, err := jsonv.Decode(raw); err == nil && jsonv.Obj(v) != nil {
				out = append(out, jsonv.Obj(v))
			}
		}
		return out
	}
	return decode(body.Machines), decode(body.Sessions), nil
}
