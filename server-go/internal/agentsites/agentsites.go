// Package agentsites is pw code agents at sites: this host's daemon, or
// the daemon on one of the user's clusters, reached over the bridge
// (internal/pwcode). An agent is a pw code session there, given an
// objective and usually a /goal. It ports server/src/agentSites.ts and
// its nine routes under /api/agent-sites.
//
// The local site is the daemon of the account the Studio runs as, so it
// is offered only when sign-in is off or AGENT_LOCAL_SITE=1. Clusters are
// reached with the viewer's own platform key; the deployment's key stands
// in only when sign-in is off.
package agentsites

import (
	"context"
	"errors"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// Local is the local site's name.
const Local = "local"

// DefaultGoalTurns is the turn bound of a goal that names none.
const DefaultGoalTurns = 20

// PermissionModes are the modes an agent may start in.
var PermissionModes = []string{"read-only", "accept-edits", "plan"}

var (
	siteRe      = regexp.MustCompile(`^(?:pw://)?[A-Za-z0-9_.-]{1,120}(?:/[A-Za-z0-9_.-]{1,120}){0,2}$`)
	sessionRe   = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,120}$`)
	workspaceRe = regexp.MustCompile(`^(?:~|\$HOME|\$WORKDIR)?(?:/[A-Za-z0-9_.@+-]+)*/?$`)
	homePrefix  = regexp.MustCompile(`^(~|\$HOME|\$WORKDIR)/?`)
	nonSlug     = regexp.MustCompile(`[^a-z0-9]+`)
	edgeDash    = regexp.MustCompile(`^-|-$`)
)

// Sites is the module: the platform and the daemon access.
type Sites struct {
	P      *platform.Platform
	Daemon *pwcode.Service
	// AuthEnabled is authEnabled() for the request (the viewer says).
	AuthEnabled func(r *http.Request) bool
	// Now is time.Now (tests).
	Now func() time.Time
}

func (s *Sites) authOn(r *http.Request) bool {
	if s.AuthEnabled != nil {
		return s.AuthEnabled(r)
	}
	return false
}

func kb(status int, msg string) error { return platform.KbError(status, msg) }

// LocalSiteAllowed is localSiteAllowed.
func (s *Sites) LocalSiteAllowed(authEnabled bool) bool {
	v := s.P.Getenv("AGENT_LOCAL_SITE")
	return v == "1" || (v != "0" && !authEnabled)
}

// AgentKey is agentKey: the viewer's own key, or on a one-person Studio
// the deployment's, else "".
func (s *Sites) AgentKey(userKey string, authEnabled bool) string {
	if userKey != "" {
		return userKey
	}
	if authEnabled {
		return ""
	}
	return s.P.GatewayKey()
}

// CheckSite is checkSite.
func (s *Sites) CheckSite(site string, authEnabled bool) (string, error) {
	v := trimJS(site)
	if v == Local {
		if !s.LocalSiteAllowed(authEnabled) {
			return "", kb(403, "The local site is not offered on this Studio; name one of your clusters.")
		}
		return v, nil
	}
	if !siteRe.MatchString(v) {
		return "", kb(400, "not a site name: "+sliceRunes(v, 80))
	}
	return v, nil
}

// CheckSession is checkSession.
func CheckSession(id string) (string, error) {
	if !sessionRe.MatchString(id) {
		return "", kb(400, "not a session id")
	}
	return id, nil
}

// ClientFor is clientFor: the daemon client for a site.
func (s *Sites) ClientFor(site, key string, authEnabled bool) (pwcode.Client, error) {
	v, err := s.CheckSite(site, authEnabled)
	if err != nil {
		return nil, err
	}
	if v == Local {
		return s.Daemon.Local(), nil
	}
	if key == "" {
		return nil, kb(409, "Agents on your clusters run with your own platform key: add your ACTIVATE API key under Settings, Model access.")
	}
	return s.Daemon.Remote(v, key), nil
}

func (s *Sites) platformHost() string {
	g := s.P.GatewayBase()
	if i := strings.Index(g, "://"); i >= 0 {
		g = g[i+3:]
	}
	if i := strings.IndexAny(g, "/?#"); i >= 0 {
		g = g[:i]
	}
	if i := strings.LastIndex(g, "@"); i >= 0 {
		g = g[i+1:]
	}
	if h, _, ok := strings.Cut(g, ":"); ok {
		g = h
	}
	return strings.ToLower(g)
}

// pwWithKey runs the CLI as the viewer: 90 s, 8 MiB, PW_API_KEY=key; a
// failure is a 502 with the last line of what the CLI said.
func (s *Sites) pwWithKey(ctx context.Context, args []string, key string) (string, error) {
	out, err := s.P.Exec(context.WithoutCancel(ctx), args, platform.ExecOptions{Timeout: 90 * time.Second, MaxBuffer: 8 << 20, Env: map[string]string{"PW_API_KEY": key}})
	if err != nil {
		lines := strings.Split(trimJS(platform.ErrText(err)), "\n")
		return "", kb(502, sliceRunes(lines[len(lines)-1], 300))
	}
	return out, nil
}

// Site is one entry of the site list.
type Site struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Type any    `json:"type,omitempty"`
}

// ListSites is listSites: the local site when offered, then the viewer's
// active clusters.
func (s *Sites) ListSites(ctx context.Context, key string, authEnabled bool) ([]*jsonv.Object, error) {
	out := []*jsonv.Object{}
	if s.LocalSiteAllowed(authEnabled) {
		out = append(out, jsonv.NewObject("name", Local, "kind", "local"))
	}
	if key == "" {
		return out, nil
	}
	args := []string{"clusters", "ls", "--status", "active", "-o", "json"}
	if h := s.platformHost(); h != "" {
		args = append(args, "--platform-host", h)
	}
	text, err := s.pwWithKey(ctx, args, key)
	if err != nil {
		return nil, err
	}
	var parsed any = []any{}
	if start := strings.IndexAny(text, "[{"); start >= 0 {
		closer := "}"
		if text[start] == '[' {
			closer = "]"
		}
		// text.slice(start, lastIndexOf(closer) + 1): empty when the
		// closer comes before the start or not at all.
		end := strings.LastIndex(text, closer) + 1
		chunk := ""
		if end > start {
			chunk = text[start:end]
		}
		v, err := jsonv.DecodeString(chunk)
		if err != nil {
			return nil, err
		}
		parsed = v
	}
	rows, ok := parsed.([]any)
	if !ok {
		rows = jsonv.Arr(jsonv.Path(parsed, "clusters"))
	}
	for _, r := range rows {
		o := jsonv.Obj(r)
		if o == nil {
			continue
		}
		n, _ := o.Get("name")
		name, isStr := n.(string)
		if !jsonv.Truthy(n) || !isStr || !siteRe.MatchString(name) {
			continue
		}
		out = append(out, jsonv.NewObject("name", name, "kind", "cluster", "type", undef(o, "type")))
	}
	return out, nil
}

// ProbeSite is probeSite: the site's daemon status with usable and why,
// starting the local daemon when none runs.
func (s *Sites) ProbeSite(ctx context.Context, site, key string, authEnabled bool) (*jsonv.Object, error) {
	c, err := s.ClientFor(site, key, authEnabled)
	if err != nil {
		return nil, err
	}
	if s.Daemon.IsLocal(c) {
		st, err := s.Daemon.EnsureDaemon(ctx)
		if err != nil {
			st = nil
		}
		out := jsonv.NewObject()
		if st != nil {
			out = st.Clone()
		}
		why := pwcode.UnusableReason(st)
		out.Set("usable", st != nil)
		out.Set("why", nullable(why))
		return out, nil
	}
	v, err := c.Call(ctx, http.MethodGet, "/v1/status", nil, 0)
	if err != nil {
		return nil, err
	}
	st := jsonv.Obj(v)
	why := pwcode.UnusableReason(st)
	out := jsonv.NewObject()
	if st != nil {
		out = st.Clone()
	}
	out.Set("usable", why == "")
	out.Set("why", nullable(why))
	return out, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// defaultWorkspace is a new agent workspace's default path.
func (s *Sites) defaultWorkspace(objective string) string {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	stamp := now().UTC().Format("20060102-150405")
	slug := edgeDash.ReplaceAllString(nonSlug.ReplaceAllString(strings.ToLower(objective), "-"), "")
	slug = sliceRunes(slug, 32)
	if slug == "" {
		slug = "agent"
	}
	return "~/studio-agents/" + stamp + "-" + slug
}

// makeWorkspace makes the workspace and gives it as an absolute path.
func (s *Sites) makeWorkspace(ctx context.Context, site, key, ws string) (string, error) {
	if !workspaceRe.MatchString(ws) {
		return "", kb(400, "workspace: an absolute path, or one under ~, $HOME, or $WORKDIR, in plain path characters")
	}
	if site == Local {
		abs := ws
		if !strings.HasPrefix(ws, "/") {
			abs = filepath.Join(s.P.IndexBase(), "agents", homePrefix.ReplaceAllString(ws, ""))
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return "", err
		}
		return abs, nil
	}
	args := []string{"ssh"}
	if h := s.platformHost(); h != "" {
		args = append(args, "--platform-host", h)
	}
	args = append(args, site, "mkdir -p -- "+ws+" && cd -- "+ws+" && pwd")
	out, err := s.pwWithKey(ctx, args, key)
	if err != nil {
		return "", err
	}
	lines := strings.Split(trimJS(out), "\n")
	abs := lines[len(lines)-1]
	if !strings.HasPrefix(abs, "/") {
		return "", kb(502, "could not make the workspace on "+site)
	}
	return abs, nil
}

// BoundedGoal is boundedGoal: the condition with the turn bound written
// into it.
func BoundedGoal(goal string, turns float64) string {
	r := math.Floor(turns + 0.5) // Math.round
	if r == 0 || math.IsNaN(r) {
		r = DefaultGoalTurns
	}
	n := math.Max(1, math.Min(r, 200))
	return sliceRunes(trimJS(goal), 3600) + "\n\nIf this is not met within " + jsonv.FormatNumber(n) + " turns, stop and say what remains."
}

// StartAgent is startAgent's options.
type StartAgent struct {
	Site, Key, Objective, Goal, Workspace, Model, PermissionMode string
	MaxTurns                                                     float64 // 0: the default
}

// Start is startAgent: a pw code session at the site on the objective,
// with the goal when one is given.
func (s *Sites) Start(ctx context.Context, o StartAgent, authEnabled bool) (*jsonv.Object, error) {
	objective := trimJS(o.Objective)
	if objective == "" {
		return nil, kb(400, "an objective is required")
	}
	mode := o.PermissionMode
	if mode == "" {
		mode = "accept-edits"
	}
	ok := false
	for _, m := range PermissionModes {
		ok = ok || m == mode
	}
	if !ok {
		return nil, kb(400, "permission mode: one of "+strings.Join(PermissionModes, ", "))
	}
	c, err := s.ClientFor(o.Site, o.Key, authEnabled)
	if err != nil {
		return nil, err
	}
	status, err := s.ProbeSite(ctx, o.Site, o.Key, authEnabled)
	if err != nil {
		return nil, err
	}
	if u, _ := status.Get("usable"); u != true {
		why, _ := status.Get("why")
		return nil, kb(502, o.Site+": "+jsString(why))
	}
	ws := o.Workspace
	if ws == "" {
		ws = s.defaultWorkspace(objective)
	}
	workspace, err := s.makeWorkspace(ctx, c.Site(), o.Key, trimJS(ws))
	if err != nil {
		return nil, err
	}
	req := jsonv.NewObject("workspace", workspace, "permissionMode", mode, "remoteControl", true)
	if o.Model != "" {
		req.Set("model", o.Model)
	}
	session, err := pwcode.CreateSession(ctx, c, req)
	if err != nil {
		return nil, err
	}
	id := jsonv.Path(session, "id")
	sidStr := jsonv.String(id)
	if _, err := pwcode.SendMessage(ctx, c, sidStr, objective); err != nil {
		return nil, err
	}
	var goal any
	if trimJS(o.Goal) != "" {
		turns := o.MaxTurns
		if turns == 0 {
			turns = DefaultGoalTurns
		}
		g := BoundedGoal(o.Goal, turns)
		goal = g
		if _, err := pwcode.SetGoal(ctx, c, sidStr, g); err != nil {
			return nil, err
		}
	}
	host, has := status.Get("hostname")
	if !has {
		host = jsonv.Undefined
	}
	return jsonv.NewObject("site", c.Site(), "session", undef(session, "id"), "workspace", workspace, "host", host, "mode", mode, "goal", goal), nil
}

// undef is o[k], undefined when absent.
func undef(o *jsonv.Object, k string) any {
	if o == nil {
		return jsonv.Undefined
	}
	v, ok := o.Get(k)
	if !ok {
		return jsonv.Undefined
	}
	return v
}

// jsString is `${v}` for a value read from JSON.
func jsString(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	}
	if v == jsonv.Undefined {
		return "undefined"
	}
	return jsonv.String(v)
}

// HistoryLine is agentSites.ts's historyLine.
func HistoryLine(h *jsonv.Object) (string, bool) {
	text := trimJS(str(h, "text"))
	switch str(h, "kind") {
	case "user":
		return "> " + sliceUnits(text, 600), true
	case "assistant":
		if text == "" {
			return "", false
		}
		return sliceUnits(text, 1500), true
	case "tool_call":
		name := "tool"
		if v, _ := h.Get("toolName"); v != nil {
			name = jsonv.String(v)
		}
		return "[" + name + "] " + sliceUnits(str(h, "toolArgs"), 300), true
	case "tool_result":
		if text == "" {
			return "", false
		}
		return "  result: " + sliceUnits(text, 400), true
	case "notice":
		if text == "" {
			return "", false
		}
		return "! " + sliceUnits(text, 300), true
	case "goal_progress":
		g := GoalProgressOf(h)
		if g == nil {
			return "", false
		}
		turns := "?"
		if v, _ := g.Get("turns"); v != nil {
			turns = jsonv.String(v)
		}
		outcome := "not met yet"
		if v, _ := g.Get("outcome"); jsonv.Truthy(v) {
			outcome = jsonv.String(v)
		}
		line := "[goal] turn " + turns + ": " + outcome
		if v, _ := g.Get("reason"); jsonv.Truthy(v) {
			line += ", " + jsonv.String(v)
		}
		if v, _ := g.Get("err"); jsonv.Truthy(v) {
			line += " (" + jsonv.String(v) + ")"
		}
		return line, true
	}
	return "", false
}

// GoalProgressOf is goalProgressOf: a goal_progress item's parsed text.
func GoalProgressOf(h *jsonv.Object) *jsonv.Object {
	if str(h, "kind") != "goal_progress" {
		return nil
	}
	t := str(h, "text")
	if t == "" {
		return nil
	}
	v, err := jsonv.DecodeString(t)
	if err != nil || v == nil {
		return nil
	}
	if o := jsonv.Obj(v); o != nil {
		return o
	}
	return jsonv.NewObject() // a truthy non-object reads as one with no fields
}

func str(o *jsonv.Object, k string) string {
	if o == nil {
		return ""
	}
	v, _ := o.Get(k)
	if v == nil {
		return ""
	}
	return jsonv.String(v)
}

// Status is agentStatus: where a session stands.
func (s *Sites) Status(ctx context.Context, site, key, id string, lines float64, authEnabled bool) (*jsonv.Object, error) {
	c, err := s.ClientFor(site, key, authEnabled)
	if err != nil {
		return nil, err
	}
	if _, err := CheckSession(id); err != nil {
		return nil, err
	}
	d, err := pwcode.GetSession(ctx, c, id)
	if err != nil {
		return nil, err
	}
	var goal any
	if st, err := pwcode.SessionState(ctx, c, id); err == nil && st != nil {
		if g, _ := st.Get("goal"); g != nil {
			goal = g
		}
	}
	hist := jsonv.Arr(jsonv.Path(d, "history"))
	var lastCheck any
	for i := len(hist) - 1; i >= 0; i-- {
		if g := GoalProgressOf(jsonv.Obj(hist[i])); g != nil {
			lastCheck = g
			break
		}
	}
	n := int(math.Max(1, math.Min(lines, 60))) // slice(-n) truncates
	from := len(hist) - n
	if from < 0 {
		from = 0
	}
	recent := []string{}
	for _, h := range hist[from:] {
		o := jsonv.Obj(h)
		if o == nil {
			o = jsonv.NewObject()
		}
		if l, ok := HistoryLine(o); ok && l != "" {
			recent = append(recent, l)
		}
	}
	approvals := []any{}
	for _, x := range jsonv.Arr(jsonv.Path(d, "approvals")) {
		a := jsonv.Obj(x)
		if a == nil {
			a = jsonv.NewObject()
		}
		head := str(a, "header")
		if head == "" {
			head = str(a, "kind")
		}
		what := str(a, "command")
		if what == "" {
			what = str(a, "question")
		}
		if what == "" {
			what = str(a, "reason")
		}
		parts := []string{}
		for _, p := range []string{head, what} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		approvals = append(approvals, jsonv.NewObject("id", undef(a, "id"), "kind", undef(a, "kind"), "text", strings.Join(parts, ": ")))
	}
	return jsonv.NewObject("site", c.Site(), "session", undef(d, "id"), "status", undef(d, "status"),
		"workspace", undef(d, "workspace"), "model", undef(d, "model"), "goal", goal, "lastCheck", lastCheck,
		"approvals", approvals, "recent", recent), nil
}

// Sessions is agentSessions.
func (s *Sites) Sessions(ctx context.Context, site, key string, authEnabled bool) ([]any, error) {
	c, err := s.ClientFor(site, key, authEnabled)
	if err != nil {
		return nil, err
	}
	rows, err := pwcode.ListSessions(ctx, c)
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, r := range jsonv.Arr(rows) {
		o := jsonv.Obj(r)
		if o == nil {
			o = jsonv.NewObject()
		}
		orNull := func(k string) any {
			v, _ := o.Get(k)
			return v
		}
		out = append(out, jsonv.NewObject("id", undef(o, "id"), "name", orNull("name"), "status", orNull("status"),
			"workspace", orNull("workspace"), "model", orNull("model"), "updatedAt", orNull("updatedAt")))
	}
	return out, nil
}

// Goal is agentGoal: set, show, pause, resume or clear a session's goal.
func (s *Sites) Goal(ctx context.Context, site, key, id, action, condition string, turns float64, authEnabled bool) (any, error) {
	c, err := s.ClientFor(site, key, authEnabled)
	if err != nil {
		return nil, err
	}
	var args string
	switch action {
	case "set":
		if trimJS(condition) == "" {
			return nil, kb(400, "a condition is required to set a goal")
		}
		if turns == 0 {
			turns = DefaultGoalTurns
		}
		args = BoundedGoal(condition, turns)
	case "pause", "resume", "clear":
		args = action
	case "show":
		args = ""
	default:
		return nil, kb(400, "action: set, show, pause, resume, or clear")
	}
	if _, err := CheckSession(id); err != nil {
		return nil, err
	}
	return pwcode.SetGoal(ctx, c, id, args)
}

// Steer is agentSteer: direction reaches a working turn without ending
// it; a message queues a new turn.
func (s *Sites) Steer(ctx context.Context, site, key, id, text, mode string, authEnabled bool) (*jsonv.Object, error) {
	c, err := s.ClientFor(site, key, authEnabled)
	if err != nil {
		return nil, err
	}
	clean := sliceUnits(trimJS(text), 4000)
	if clean == "" {
		return nil, kb(400, "text is required")
	}
	if _, err := CheckSession(id); err != nil {
		return nil, err
	}
	if mode == "message" {
		r, err := pwcode.SendMessage(ctx, c, id, clean)
		if err != nil {
			return nil, err
		}
		q, err := queued(r)
		if err != nil {
			return nil, err
		}
		return jsonv.NewObject("queued", q), nil
	}
	r, err := pwcode.SteerSession(ctx, c, id, clean)
	if err != nil {
		return nil, err
	}
	if r == nil || !jsonv.Truthy(undef(r, "delivered")) {
		m, err := pwcode.SendMessage(ctx, c, id, clean)
		if err != nil {
			return nil, err
		}
		q, err := queued(m)
		if err != nil {
			return nil, err
		}
		return jsonv.NewObject("queued", q, "asMessage", true), nil
	}
	return jsonv.NewObject("delivered", true), nil
}

// queued is (await sendMessage(...)).queued, which throws on a daemon
// that answered no object.
func queued(r *jsonv.Object) (any, error) {
	if r == nil {
		return nil, errors.New("Cannot read properties of null (reading 'queued')")
	}
	return undef(r, "queued"), nil
}

// Approve is agentApprove.
func (s *Sites) Approve(ctx context.Context, site, key, id, approval string, allowed bool, denyMessage string, authEnabled bool) (*jsonv.Object, error) {
	c, err := s.ClientFor(site, key, authEnabled)
	if err != nil {
		return nil, err
	}
	if !sessionRe.MatchString(approval) {
		return nil, kb(400, "not an approval id")
	}
	if _, err := CheckSession(id); err != nil {
		return nil, err
	}
	answer := jsonv.NewObject("allowed", allowed)
	if !allowed {
		if denyMessage == "" {
			denyMessage = "Denied from the Studio."
		}
		answer.Set("denyMessage", denyMessage)
	}
	if _, err := pwcode.AnswerApproval(ctx, c, id, approval, answer); err != nil {
		return nil, err
	}
	return jsonv.NewObject("ok", true), nil
}

// Stop is agentStop.
func (s *Sites) Stop(ctx context.Context, site, key, id string, authEnabled bool) (*jsonv.Object, error) {
	if _, err := CheckSession(id); err != nil {
		return nil, err
	}
	c, err := s.ClientFor(site, key, authEnabled)
	if err != nil {
		return nil, err
	}
	if _, err := pwcode.InterruptSession(ctx, c, id); err != nil {
		return nil, err
	}
	return jsonv.NewObject("ok", true), nil
}

/* ---- text helpers ---- */

var wsTrim = regexp.MustCompile(`^[\s\x{feff}\x{a0}]+|[\s\x{feff}\x{a0}]+$`)

func trimJS(s string) string { return wsTrim.ReplaceAllString(s, "") }

func sliceRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// sliceUnits is s.slice(0, n) in UTF-16 code units, kept whole at a
// surrogate pair the cut would split (where Node leaves half a pair, which
// JSON.stringify then writes as an escape).
func sliceUnits(s string, n int) string {
	units := 0
	for i, r := range s {
		w := 1
		if r > 0xFFFF {
			w = 2
		}
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}
