// Package libapi serves the library and search routes of the Studio
// (routes.ts's /api/search and /api/libraries, libraryScope.ts) from
// internal/libraries and internal/search, and fills the App's Libraries
// provider.
package libapi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/gufitrace"
	"github.com/parallelworks/activate-studio/server-go/internal/index"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/search"
)

// Service is the track's state, one per server.
type Service struct {
	a      *app.App
	Reg    *libraries.Registry
	Engine *search.Engine
	Pool   *remote.Pool
	builds *builds
}

var (
	buildsMu sync.Mutex
	buildsOf = map[*app.App]*builds{}
)

// New is the service over a's shared registry and engine, and its one
// set of library builds.
func New(a *app.App) *Service {
	e := search.Of(a)
	s := &Service{a: a, Reg: e.Reg, Engine: e, Pool: e.Pool}
	buildsMu.Lock()
	b := buildsOf[a]
	if b == nil {
		b = newBuilds(s)
		buildsOf[a] = b
	}
	buildsMu.Unlock()
	s.builds = b
	return s
}

// Register serves the track's routes on mux and fills its provider.
func Register(mux *http.ServeMux, a *app.App) {
	s := New(a)
	if gufitrace.Default == nil {
		gufitrace.Default = gufitrace.NewLog(os.Getenv("STUDIO_DATA_DIR"), a.Cfg.IndexBase, func() bool { return a.Settings.Effective().GufiLog })
	}
	libraries.CheckDest = remote.SSHDest
	a.Providers.Libraries = func(r *http.Request, kbLabel, viewer string) ([]json.RawMessage, error) {
		v := libraries.ViewerOf(viewer)
		var out []json.RawMessage
		for _, l := range s.Reg.List(kbLabel, v) {
			b, err := json.Marshal(libraries.Public(l))
			if err != nil {
				return nil, err
			}
			out = append(out, b)
		}
		return out, nil
	}
	h := func(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
		return apierr.Handler(a.Log, fn)
	}
	mux.HandleFunc("GET /api/search", h(s.search))
	mux.HandleFunc("GET /api/libraries", h(s.list))
	mux.HandleFunc("GET /api/kb/details", h(s.details))
	mux.HandleFunc("GET /api/kb/topology", h(s.topology))
	mux.HandleFunc("GET /api/libraries/{id}/topology", h(s.topology))
	mux.HandleFunc("POST /api/query", h(s.query))
	mux.HandleFunc("POST /api/query/sql", h(s.querySQL))
	mux.HandleFunc("GET /api/query/saved", h(s.savedList))
	mux.HandleFunc("POST /api/query/saved", h(s.savedAdd))
	mux.HandleFunc("DELETE /api/query/saved/{id}", h(s.savedDelete))
	// The library write routes, builds and read-through settings: Go's in
	// Node-free mode. With Node beside, they stay Node's, which keeps the
	// libraries it builds and syncs in its own memory.
	if a.NodeFree() {
		mux.HandleFunc("POST /api/libraries", h(s.addLibrary))
		mux.HandleFunc("DELETE /api/libraries/{id}", h(s.removeLibrary))
		mux.HandleFunc("PUT /api/libraries/{id}/depth", h(s.setDepth))
		mux.HandleFunc("PUT /api/libraries/{id}/meaning", h(s.setMeaning))
		mux.HandleFunc("POST /api/libraries/build/check", h(s.buildCheck))
		mux.HandleFunc("POST /api/libraries/build", h(s.buildAdd))
		mux.HandleFunc("POST /api/libraries/{id}/rebuild", h(s.rebuild))
		index.Of(a).Hooks.RequestAllLibrarySyncs = s.builds.requestAllSyncs
		a.OnIndexWork("libraries: sync", s.builds.startSync)
		a.OnStop("libraries: stop builds", s.builds.stop)
	}
}

// viewer is req.user?.id ?? null as Node's routes read it, through the
// platform track's ViewerOf (the front's verified user, or Node's answer
// over the bridge where identity headers pass through).
func (s *Service) viewer(r *http.Request) (libraries.Viewer, error) {
	v, err := platform.Of(s.a).ViewerOf(r)
	if err != nil {
		var ne *app.NodeError
		if errors.As(err, &ne) && ne.Status == http.StatusUnauthorized {
			return nil, apierr.New(401, "authentication required")
		}
		return nil, err
	}
	if v != nil && v.User != nil {
		return libraries.ViewerOf(v.User.ID), nil
	}
	if v != nil && v.AuthEnabled {
		// ownerOf(null) with sign-in on: nobody, so no user libraries.
		return libraries.Deployment, nil
	}
	return libraries.ViewerOf(""), nil
}

func (s *Service) kbLabel() string { return s.a.Settings.Effective().KbLabel }

func jsNumberOr(v string, d float64) float64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return d
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n == 0 || math.IsNaN(n) {
		return d
	}
	return n
}

// first is Fastify's querystring value for a key given once (a repeated
// key is an array; the routes here read the first).
func first(q map[string][]string, k string) string {
	if v := q[k]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// validateText is refine.ts validateText.
func (s *Service) validateText(ctx context.Context, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	why := checkShape(text)
	if why == "" {
		why = s.Engine.CheckFTS(ctx, text)
	}
	if why == "" {
		return nil
	}
	why = strings.TrimPrefix(why, "fts5: ")
	if strings.HasPrefix(why, "fts5:") {
		why = strings.TrimLeft(why[5:], " ")
	}
	if c, ok := strings.CutPrefix(why, "no such column: "); ok {
		why = `"` + c + `" was read as a column name, so a word with a hyphen or a colon needs quotes`
	}
	return apierr.New(400, "The text search cannot be read (%s). Put a word with punctuation in double quotes, such as \"covid-19\", and write AND, OR and NOT in capitals between words.", why)
}

func checkShape(expr string) string {
	if strings.Count(expr, `"`)%2 == 1 {
		return "a quotation mark is not closed"
	}
	bare := quoted.ReplaceAllString(expr, "")
	depth := 0
	for _, c := range bare {
		if c == '(' {
			depth++
		}
		if c == ')' {
			depth--
			if depth < 0 {
				return "a closing bracket has no opening one"
			}
		}
	}
	if depth != 0 {
		return "a bracket is not closed"
	}
	if opEnds.MatchString(expr) {
		return "AND, OR and NOT need words on both sides"
	}
	return ""
}

type searchAnswer struct {
	Hits    []search.ScopedHit   `json:"hits"`
	Gufi    []gufitrace.Command  `json:"gufi"`
	Slow    []string             `json:"slow,omitempty"`
	Omitted []libraries.Omission `json:"omitted,omitempty"`
}

func (s *Service) search(w http.ResponseWriter, r *http.Request) error {
	qs := r.URL.Query()
	q := first(qs, "q")
	refine := search.RefineOf(first(qs, "name"), first(qs, "text"), first(qs, "match"))
	if strings.TrimSpace(q) == "" && refine == nil {
		apierr.JSON(w, 200, map[string]any{"hits": []any{}})
		return nil
	}
	if refine != nil {
		if err := s.validateText(r.Context(), refine.Text); err != nil {
			return err
		}
	}
	n := int(math.Min(jsNumberOr(first(qs, "limit"), 50), 1000))
	viewer, err := s.viewer(r)
	if err != nil {
		return err
	}
	libs, omitted, err := s.Reg.Scope(qs["library"], viewer, s.kbLabel())
	if err != nil {
		return err
	}
	if !s.Engine.GufiInstalled() {
		remoteAny := false
		for _, l := range libs {
			remoteAny = remoteAny || l.Site != ""
		}
		if !remoteAny {
			apierr.JSON(w, 200, map[string]any{"hits": []any{}, "error": "GUFI is not installed here"})
			return nil
		}
	}
	om := &libraries.Omissions{}
	om.Set(omitted)
	var semantic bool
	if refine != nil {
		semantic = refine.Text != "" && gufi.WantsSemantic(search.MeaningPhrase(refine.Text))
	} else {
		semantic = gufi.WantsSemantic(q)
	}
	text := q
	if refine != nil {
		text = ""
	}
	slow := []string{}
	ctx, commands := gufitrace.Start(r.Context(), "search")
	hits := s.Engine.SearchScope(ctx, libs, text, n, search.ScopeOpts{Semantic: semantic, Slow: &slow, Omitted: om, Refine: refine})
	cmds := commands()
	var filter []string
	if t := first(qs, "tags"); t != "" {
		for _, x := range strings.Split(t, ",") {
			if x != "" {
				filter = append(filter, x)
			}
		}
	}
	hits = s.Engine.Annotate(r.Context(), hits, filter)
	if cmds == nil {
		cmds = []gufitrace.Command{}
	}
	apierr.JSON(w, 200, searchAnswer{Hits: hits, Gufi: cmds, Slow: slow, Omitted: om.Items()})
	return nil
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	libs := s.Reg.ProbeAll(r.Context(), s.kbLabel())
	out := make([]libraries.SettingsLibrary, 0, len(libs))
	for _, l := range libs {
		out = append(out, s.Reg.SettingsView(l))
	}
	// canBuild: localBuildOffered(), libraries built of folders here
	// where nobody signs in.
	apierr.JSON(w, 200, struct {
		Libraries []libraries.SettingsLibrary `json:"libraries"`
		CanBuild  bool                        `json:"canBuild"`
	}{out, !s.authEnabled(r)})
	return nil
}

func (s *Service) authEnabled(r *http.Request) bool {
	if v, err := platform.Of(s.a).ViewerOf(r); err == nil && v != nil {
		return v.AuthEnabled
	}
	return s.a.Cfg.AuthEnabled()
}
