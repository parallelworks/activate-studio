// Package sessions ports server/src/sessions.ts and runPane.ts: the
// platform sessions a viewer can see (the apps workflows open, read from
// the platform API with the viewer's key), the run pane's run and run
// files, and the session relay that serves a session's own host under the
// Studio's origin.
package sessions

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// SessionRun is the run that opened a session.
type SessionRun struct {
	Slug     string
	Workflow string
	Number   *float64
	Status   string // "" is null
}

// SessionRow is one platform session as the Studio shows it.
type SessionRow struct {
	Name, User, Type, Status string
	Healthy                  *bool
	URL                      string // absolute; "" is null
	OwnHost                  bool
	PlatformURL              string // "" is null
	Run                      *SessionRun
	Resource                 string // "" is null
	CreatedAt                string // "" is null
}

func orNull(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// MarshalJSON writes the row with sessions.ts's keys in their order.
func (s *SessionRow) MarshalJSON() ([]byte, error) { return jsonv.Marshal(s.Object()) }

// Object is the row as a JavaScript object.
func (s *SessionRow) Object() *jsonv.Object {
	var healthy any
	if s.Healthy != nil {
		healthy = *s.Healthy
	}
	var run any
	if s.Run != nil {
		var num any
		if s.Run.Number != nil {
			num = *s.Run.Number
		}
		run = jsonv.NewObject("slug", s.Run.Slug, "workflow", s.Run.Workflow, "number", num, "status", orNull(s.Run.Status))
	}
	return jsonv.NewObject("name", s.Name, "user", s.User, "type", s.Type, "status", s.Status, "healthy", healthy,
		"url", orNull(s.URL), "ownHost", s.OwnHost, "platformUrl", orNull(s.PlatformURL), "run", run,
		"resource", orNull(s.Resource), "createdAt", orNull(s.CreatedAt))
}

var internalHref = regexp.MustCompile(`^/sessions/[^?#]+$`)

// jsString is String(v ?? ”).
func jsString(v any) string { return jsonv.String(v) }

// hostnameOf is new URL(u).hostname, or "" when u is not a URL.
func hostnameOf(u string) string {
	p, err := url.Parse(u)
	if err != nil || p.Scheme == "" || p.Host == "" {
		return ""
	}
	return strings.ToLower(p.Hostname())
}

// hostOf is new URL(u).host lower-cased, or "".
func hostOf(u string) string {
	p, err := url.Parse(u)
	if err != nil || p.Scheme == "" || p.Host == "" {
		return ""
	}
	return strings.ToLower(p.Host)
}

// Row is sessionRow: a platform session record as the Studio shows it, or
// nil when it has no name. platform is the platform's origin.
func Row(raw any, platformOrigin string) *SessionRow {
	o := jsonv.Obj(raw)
	name, _ := jsonv.Path(o, "name").(string)
	if name == "" {
		return nil
	}
	href, _ := jsonv.Path(o, "externalHref").(string)
	u := ""
	if strings.HasPrefix(href, "https://") {
		u = href
	} else if strings.HasPrefix(href, "/") {
		u = platformOrigin + href
	}
	own := false
	if dn := jsonv.Path(o, "domainName"); u != "" && jsonv.Truthy(dn) {
		own = hostnameOf(u) != "" && hostnameOf(u) == jsString(dn)
	}
	row := &SessionRow{
		Name: name, User: jsString(jsonv.Path(o, "user")), Type: jsString(jsonv.Path(o, "type")),
		Status: jsString(jsonv.Path(o, "status")), URL: u, OwnHost: own,
	}
	if h, ok := jsonv.Path(o, "healthy").(bool); ok {
		row.Healthy = &h
	}
	if ih, ok := jsonv.Path(o, "internalHref").(string); ok && internalHref.MatchString(ih) {
		row.PlatformURL = platformOrigin + ih
	}
	if wr := jsonv.Obj(jsonv.Path(o, "workflowRun")); wr != nil {
		if slug := jsonv.Path(wr, "slug"); jsonv.Truthy(slug) {
			run := &SessionRun{Slug: jsString(slug)}
			if dn := jsonv.Path(wr, "displayName"); jsonv.Truthy(dn) {
				run.Workflow = jsString(dn)
			} else if n := jsonv.Path(wr, "name"); jsonv.Truthy(n) {
				run.Workflow = jsString(n)
			}
			if n, ok := jsonv.Path(wr, "number").(float64); ok {
				run.Number = &n
			}
			if st := jsonv.Path(wr, "status"); jsonv.Truthy(st) {
				run.Status = jsString(st)
			}
			row.Run = run
		}
	}
	if t := jsonv.Path(o, "targetName"); jsonv.Truthy(t) {
		row.Resource = jsString(t)
	}
	if c := jsonv.Path(o, "createdAt"); jsonv.Truthy(c) {
		row.CreatedAt = jsString(c)
	}
	return row
}

// OfRun is sessionOfRun: the run the platform recorded for the session,
// or for a session with none, a name that is the run's id or ends with it.
func OfRun(s *SessionRow, slug string) bool {
	if slug == "" {
		return false
	}
	if s.Run != nil {
		return s.Run.Slug == slug
	}
	return s.Name == slug || strings.HasSuffix(s.Name, "-"+slug)
}

// Embed is sessionEmbed: the markdown card for a session in a chat reply.
func Embed(s *SessionRow) string {
	return "![" + s.Name + "](/?embed=session&user=" + formEncode(s.User) + "&name=" + formEncode(s.Name) + ")"
}

// formEncode is URLSearchParams' encoding of a value.
func formEncode(v string) string {
	return strings.NewReplacer("%2A", "*", "~", "%7E").Replace(url.QueryEscape(v))
}

// KeyFor is sessionKeyFor: the viewer's own platform key, else the
// deployment's; "" when there is neither.
func KeyFor(p *platform.Platform, v *platform.Viewer) string {
	k, _ := p.KeyFor(v)
	return k
}

type cached struct {
	at   time.Time
	rows []*SessionRow
}

var (
	cacheMu sync.Mutex
	cache   = map[string]cached{}
)

const ttl = 10 * time.Second

// ResetCache forgets the session lists (resetSessionsForTests).
func ResetCache() { cacheMu.Lock(); cache = map[string]cached{}; cacheMu.Unlock() }

// List is listSessions: every session the key's account can see, kept ten
// seconds per key.
func List(ctx context.Context, p *platform.Platform, key string) ([]*SessionRow, error) {
	k := key
	if len(k) > 12 {
		k = k[len(k)-12:]
	}
	cacheMu.Lock()
	hit, ok := cache[k]
	cacheMu.Unlock()
	if ok && time.Since(hit.at) < ttl {
		return hit.rows, nil
	}
	origin := p.Origin()
	if origin == "" {
		return nil, platform.KbError(502, "No platform address is configured.")
	}
	res, err := platform.Get(ctx, origin+"/api/sessions", map[string]string{"Authorization": "Bearer " + key}, 20*time.Second)
	if err != nil {
		return nil, err
	}
	if !res.OK {
		status := 502
		if res.Status == 401 || res.Status == 403 {
			status = 409
		}
		return nil, platform.KbError(status, "The platform refused the session list ("+itoa(res.Status)+").")
	}
	if res.Body == nil {
		if _, err := jsonv.Decode(res.Raw); err != nil {
			return nil, errNotJSON
		}
	}
	var raw []any
	if a, ok := res.Body.([]any); ok {
		raw = a
	} else if a := jsonv.Arr(jsonv.Path(res.Body, "sessions")); a != nil {
		raw = a
	}
	rows := []*SessionRow{}
	for _, r := range raw {
		if row := Row(r, origin); row != nil {
			rows = append(rows, row)
		}
	}
	cacheMu.Lock()
	cache[k] = cached{at: time.Now(), rows: rows}
	cacheMu.Unlock()
	return rows, nil
}

type notJSON struct{}

func (notJSON) Error() string { return "the session list is not JSON" }

var errNotJSON error = notJSON{}

func itoa(n int) string { return jsonv.FormatNumber(float64(n)) }

// Routes serves sessions.ts's and runPane.ts's routes.
type Routes struct {
	p   *platform.Platform
	a   *app.App
	pw  workspaceStarter
	mu  sync.Mutex
	run map[string]time.Time // the last workspace start per key
}

type workspaceStarter func(key string)

// Register registers the sessions, run pane and relay routes.
func Register(mux *http.ServeMux, a *app.App, p *platform.Platform) *Routes {
	s := &Routes{p: p, a: a, run: map[string]time.Time{}}
	s.pw = s.startWorkspace
	h := func(f func(w http.ResponseWriter, r *http.Request) (any, error)) http.HandlerFunc {
		return platform.Handler(a.Log, f)
	}
	mux.HandleFunc("GET /api/sessions", h(s.list))
	mux.HandleFunc("GET /api/sessions/item", h(s.item))
	mux.HandleFunc("GET /api/runs/item", h(s.runItem))
	mux.HandleFunc("GET /api/runs/file", h(s.runFile))
	mux.HandleFunc("GET /api/session-proxy/{host}", s.redirect)
	mux.HandleFunc("GET /api/session-proxy/{host}/{rest...}", h(s.relay))
	return s
}

func (s *Routes) keyOf(r *http.Request) (string, error) {
	v, err := s.p.ViewerOf(r)
	if err != nil {
		return "", err
	}
	key := KeyFor(s.p, v)
	if key == "" {
		return "", platform.KbError(409, "No platform credential: add your ACTIVATE API key under Settings, Model access.")
	}
	return key, nil
}

func (s *Routes) list(w http.ResponseWriter, r *http.Request) (any, error) {
	run := platform.Q(r, "run")
	key, err := s.keyOf(r)
	if err != nil {
		return nil, err
	}
	rows, err := List(r.Context(), s.p, key)
	if err != nil {
		return nil, err
	}
	out := []*SessionRow{}
	for _, x := range rows {
		if run == "" || OfRun(x, run) {
			out = append(out, x)
		}
	}
	created := func(x *SessionRow) string {
		if x.CreatedAt == "" {
			return "null"
		}
		return x.CreatedAt
	}
	sort.SliceStable(out, func(i, j int) bool { return created(out[i]) > created(out[j]) })
	return jsonv.NewObject("sessions", out), nil
}

func (s *Routes) item(w http.ResponseWriter, r *http.Request) (any, error) {
	user, name, host, u, run := platform.Q(r, "user"), platform.Q(r, "name"), platform.Q(r, "host"), platform.Q(r, "url"), platform.Q(r, "run")
	want := strings.ToLower(host)
	if want == "" && u != "" {
		want = hostOf(u)
	}
	key, err := s.keyOf(r)
	if err != nil {
		return nil, err
	}
	rows, err := List(r.Context(), s.p, key)
	if err != nil {
		return nil, err
	}
	var row *SessionRow
	if run != "" {
		var ofRun []*SessionRow
		for _, x := range rows {
			if OfRun(x, run) {
				ofRun = append(ofRun, x)
			}
		}
		sort.SliceStable(ofRun, func(i, j int) bool { return ofRun[i].Status == "running" && ofRun[j].Status != "running" })
		if len(ofRun) > 0 {
			row = ofRun[0]
		}
	}
	if row == nil {
		for _, x := range rows {
			if (name != "" && x.Name == name && (user == "" || x.User == user)) || (want != "" && x.URL != "" && hostOf(x.URL) == want) {
				row = x
				break
			}
		}
	}
	if row == nil {
		if run != "" && name == "" && want == "" {
			return nil, platform.KbError(404, "Run "+run+" has no session yet.")
		}
		what := name
		if what == "" {
			what = want
		}
		return nil, platform.KbError(404, "No session "+what+" is visible to you.")
	}
	return row.Object(), nil
}

// The names sessions.ts exports, for the workflows track (runs.ts posts a
// run's session in its conversation with them).
var (
	ListSessions  = List
	SessionOfRun  = OfRun
	SessionEmbed  = Embed
	SessionKeyFor = KeyFor
)
