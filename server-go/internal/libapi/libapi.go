// Package libapi serves the library and search routes of the Studio
// (routes.ts's /api/search and /api/libraries, libraryScope.ts) from
// internal/libraries and internal/search, and fills the App's Libraries
// provider.
package libapi

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/gufitrace"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/search"
)

// Service is the track's state, one per server.
type Service struct {
	a      *app.App
	Reg    *libraries.Registry
	Engine *search.Engine
	Pool   *remote.Pool
}

func pythonBin() string {
	if p := os.Getenv("PYTHON_BIN"); p != "" {
		return p
	}
	for _, c := range []string{"python3.13", "python3.12", "python3.11", "python3.10", "python3"} {
		out, err := exec.Command(c, "-c", "import sys; print(sys.version_info >= (3, 10))").Output()
		if err == nil && strings.TrimSpace(string(out)) == "True" {
			return c
		}
	}
	return "python3"
}

// New builds the service from the App.
func New(a *app.App) *Service {
	cfg := a.Cfg
	pool := remote.New(remote.Options{IndexBase: cfg.IndexBase})
	reg := libraries.New(libraries.Options{
		KBRoot: cfg.KBRoot, IndexBase: cfg.IndexBase, GufiIndex: cfg.GufiIndex, GufiBin: cfg.GufiBin,
		AuthEnabled: cfg.AuthEnabled(), Runner: a.Gufi, SSHSite: remote.SSHSite, Log: a.Log,
		ReachedLabel: func(site string) string {
			if r := pool.ReachedHost(site); r != nil {
				return r.Label
			}
			return ""
		},
		Learn: func(site string) { pool.Warm(site) },
	})
	keys := libraries.DeploymentKeys{AuthEnabled: false}
	eng := &search.Engine{
		Reg: reg, Keys: keys, Local: a.Gufi, GufiBin: cfg.GufiBin, GufiIndex: cfg.GufiIndex,
		KBRoot: cfg.KBRoot, IndexBase: cfg.IndexBase, Pool: pool,
		Remote:       &remote.Runners{Pool: pool, Options: gufi.OptionsFrom(cfg.Gufi)},
		Embed:        &search.WorkerEmbedder{Python: pythonBin(), ProjectRoot: cfg.ProjectRoot, IndexBase: cfg.IndexBase},
		Labels:       &search.FileLabels{IndexBase: cfg.IndexBase, KBRoot: cfg.KBRoot, GufiIndex: cfg.GufiIndex, Runner: a.Gufi},
		Getenv:       os.Getenv,
		SearchEngine: func() string { return a.Settings.Effective().SearchEngine },
		Log:          a.Log,
	}
	return &Service{a: a, Reg: reg, Engine: eng, Pool: pool}
}

// Register serves the track's routes on mux and fills its provider.
func Register(mux *http.ServeMux, a *app.App) {
	s := New(a)
	if gufitrace.Default == nil {
		gufitrace.Default = gufitrace.NewLog(os.Getenv("STUDIO_DATA_DIR"), a.Cfg.IndexBase, func() bool { return a.Settings.Effective().GufiLog })
	}
	libraries.CheckDest = remote.SSHDest
	a.OnStop("remote sessions", func() int {
		s.Pool.Shutdown()
		if w, ok := s.Engine.Embed.(*search.WorkerEmbedder); ok {
			w.Stop()
		}
		return 0
	})
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
}

// viewer is req.user?.id ?? null as Node's routes read it: the front's
// verified user where the Go server checks identity; where identity
// headers pass through to Node, Node's own answer for this request.
func (s *Service) viewer(r *http.Request) (libraries.Viewer, error) {
	if s.a.Cfg.FrontAuth {
		if u := auth.UserOf(r); u != nil {
			return libraries.ViewerOf(u.ID), nil
		}
		if s.a.Cfg.AuthEnabled() {
			return libraries.Deployment, nil
		}
		return libraries.ViewerOf(""), nil
	}
	n := s.a.NodeCaller()
	if n == nil {
		return libraries.ViewerOf(""), nil
	}
	var who struct {
		AuthEnabled bool `json:"authEnabled"`
		User        *struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := n.Do(r, "GET", "/api/whoami", nil, &who); err != nil {
		return nil, err
	}
	if who.User != nil {
		return libraries.ViewerOf(who.User.ID), nil
	}
	if who.AuthEnabled {
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
	if s.a.Cfg.FrontAuth {
		return s.a.Cfg.AuthEnabled()
	}
	if n := s.a.NodeCaller(); n != nil {
		var who struct {
			AuthEnabled bool `json:"authEnabled"`
		}
		if n.Do(r, "GET", "/api/whoami", nil, &who) == nil {
			return who.AuthEnabled
		}
	}
	return s.a.Cfg.AuthEnabled()
}
