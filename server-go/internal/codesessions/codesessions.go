// Package codesessions shows the viewer's pw code sessions on the
// platform: the machines and sessions the platform's Code page lists, and
// one session's transcript, read through the platform API as the viewer,
// with a link to each session's page on the platform. Read only: starting,
// answering and steering a session stay on the platform's own page until
// its client is open for others to build on.
package codesessions

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/sessions"
)

type machine struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Owner    string `json:"owner,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

type approval struct {
	ID string `json:"id"`
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
		LastError: s.LastError, URL: URL(origin, s.MachineID, s.ID),
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

// Register serves GET /api/code-sessions and /api/code-sessions/item.
func Register(mux *http.ServeMux, a *app.App, p *platform.Platform) {
	s := &routes{a: a, p: p}
	h := func(f func(w http.ResponseWriter, r *http.Request) (any, error)) http.HandlerFunc {
		return platform.Handler(a.Log, f)
	}
	mux.HandleFunc("GET /api/code-sessions", h(s.list))
	mux.HandleFunc("GET /api/code-sessions/item", h(s.item))
}

func (s *routes) key(r *http.Request) (string, error) {
	v, err := s.p.ViewerOf(r)
	if err != nil {
		return "", err
	}
	if k := sessions.KeyFor(s.p, v); k != "" {
		return k, nil
	}
	return "", platform.KbError(409, "No platform credential: add your ACTIVATE API key under Settings, Model access.")
}

// fetch reads a platform API path as the viewer, explaining the answers
// that mean the feature is not there for them.
func (s *routes) fetch(r *http.Request, path string, out any) error {
	if s.p.Origin() == "" {
		return platform.KbError(409, "This Studio is not connected to an ACTIVATE platform, where pw code sessions are listed.")
	}
	key, err := s.key(r)
	if err != nil {
		return err
	}
	res := s.p.APIFetch(r.Context(), path, key, 30*time.Second)
	switch {
	case res == nil:
		return platform.KbError(502, "The platform did not answer.")
	case res.Status == http.StatusUnauthorized:
		return platform.KbError(401, "The platform refused your credential; sign in again, or renew your API key under Settings, Model access.")
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

func (s *routes) list(w http.ResponseWriter, r *http.Request) (any, error) {
	var body struct {
		Machines []machine `json:"machines"`
		Sessions []session `json:"sessions"`
	}
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
		machines = []machine{}
	}
	return map[string]any{"machines": machines, "sessions": out, "codeUrl": strings.TrimRight(origin, "/") + "/code"}, nil
}

// Transcript limits: the latest items, each cut to a readable length.
const (
	maxItems      = 400
	maxText       = 20000
	maxToolResult = 1500
	maxToolArgs   = 600
)

func (s *routes) item(w http.ResponseWriter, r *http.Request) (any, error) {
	mid, id := platform.Q(r, "machine"), platform.Q(r, "id")
	if !idRe.MatchString(mid) || !idRe.MatchString(id) {
		return nil, platform.KbError(400, "machine and id are required")
	}
	var d detail
	if err := s.fetch(r, "/api/code/machines/"+platform.EncodeURIComponent(mid)+"/sessions/"+platform.EncodeURIComponent(id), &d); err != nil {
		return nil, err
	}
	if d.MachineID == "" {
		d.MachineID = mid
	}
	name := platform.Q(r, "machineName")
	v := view(d.session, map[string]string{mid: name}, s.p.Origin())
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
	return map[string]any{"session": v, "history": hist, "earlier": dropped}, nil
}
