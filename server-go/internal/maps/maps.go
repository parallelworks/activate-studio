// Package maps serves the semantic map routes of semanticMap.ts: each
// library's map, its clusters a page at a time, a file's nearest files by
// meaning, the list for the page's picker, and a refresh. A map of a library
// on this computer is made in process by internal/semanticmap from its
// index; one of a library on another system from a sample of its vectors
// fetched with one gufi_query (remoteMap.ts), made here in its owner's
// folder. The routes only read what a build wrote, so a page load never
// makes a map; the last one is served while a new one is made.
package maps

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/bridge"
	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/index"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/search"
	"github.com/parallelworks/activate-studio/server-go/internal/semanticmap"
)

// BridgePrefix is where Node, while it runs beside the Go server, asks for
// a map to be made again and says a library was removed (semanticMap.ts
// with STUDIO_GO_MAPS). Private to the bridge.
const BridgePrefix = "/internal/maps/"

func init() {
	bridge.RegisterNodeEnv("maps", func(string) map[string]string {
		return map[string]string{"STUDIO_GO_MAPS": "1"}
	})
	bridge.RegisterPrivatePrefix(BridgePrefix)
}

// Target is where a library's map is made from and written to (MapTarget):
// Key names it among the maps kept, the library's id, or for a library on
// another system its root, which names its owner too.
type Target struct {
	ID, Key, Index, Out string
	Remote              *RemoteTarget
}

// RemoteTarget is a library on another system: its root, its owner's
// folder for it, and once fetched, the vectors and what they are of.
type RemoteTarget struct {
	Root, Home, Vectors, Model string
	Of                         int
}

type cached struct {
	stamp string
	data  *jsonv.Object
}

// state is a map's MapState.
type state struct {
	running, again bool
	timer          *time.Timer
	dueAt          time.Time
	firstTried     bool
	lastEnd        time.Time
	err            *string
	empty          bool
	overview       *cached
	clusters       []clusterEntry // most recent last, four at most
	target         *Target
	force          bool
	progress       *jsonv.Object
}

type clusterEntry struct {
	c int
	cached
}

// Module is the maps of one server.
type Module struct {
	Reg      *libraries.Registry
	Eng      *search.Engine
	Pool     *remote.Pool
	Local    *gufi.Runner
	Settings *libraries.FileIndexSettings
	// Keys is the platform key a remote library's owner acts with.
	Keys                                         libraries.Keys
	KBRoot, GufiIndex, SemanticMapDir, IndexBase string
	Log                                          *slog.Logger
	// Build makes a map (the builder Node's tests replace); nil: in process.
	Build func(t Target) error
	// DelayScale scales every delay asked for (Node's delayScale), and
	// RemoteCheck is how long a remote map goes before a check, while it is
	// looked at.
	DelayScale  float64
	RemoteCheck time.Duration
	// BuildLog, when set, gets a line per build started
	// (STUDIO_TEST_MAP_BUILDS), for tests that count them.
	BuildLog string
	// Viewer is req.user?.id as the route sees it; nil: libapi's rule.
	Viewer func(r *http.Request) (libraries.Viewer, error)
	// RemoteFiles is remoteMapFiles().
	RemoteFiles func() int

	mu        sync.Mutex
	states    map[string]*state
	neighbors map[string]*neighborCache
}

var (
	ofMu sync.Mutex
	ofs  = map[*app.App]*Module{}
)

// Of is a's one map module.
func Of(a *app.App) *Module {
	ofMu.Lock()
	defer ofMu.Unlock()
	if m := ofs[a]; m != nil {
		return m
	}
	e := search.Of(a)
	cfg := a.Cfg
	m := New(e.Reg, e, e.Pool, a.Gufi, cfg.KBRoot, cfg.GufiIndex, filepath.Join(cfg.IndexDir, "semantic-map"), cfg.IndexBase, a.Log)
	m.Keys = ownerKeys{a: a}
	m.BuildLog = os.Getenv("STUDIO_TEST_MAP_BUILDS")
	m.Viewer = func(r *http.Request) (libraries.Viewer, error) { return viewerOf(a, r) }
	// The main folder's map after an index job here (index.Indexer's hook):
	// one state for it, whichever asks.
	index.Of(a).Hooks.ScheduleMapRebuild = func(d time.Duration) { m.Schedule(libraries.PrimaryID, d) }
	// A library removed here takes its map with it.
	e.Reg.OnRemoved(m.LibraryRemoved)
	a.OnStop("semantic maps: timers", func() int { m.stopTimers(); return 0 })
	ofs[a] = m
	return m
}

// New is a module over the stores given, for Of and tests.
func New(reg *libraries.Registry, eng *search.Engine, pool *remote.Pool, local *gufi.Runner, kbRoot, gufiIndex, semanticMapDir, indexBase string, log *slog.Logger) *Module {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Module{
		Reg: reg, Eng: eng, Pool: pool, Local: local,
		// Read from the file each time it changes, as Node's indexSettings:
		// Node writes it while it runs beside.
		Settings: libraries.NewFileIndexSettings(indexBase),
		Keys:     libraries.DeploymentKeys{},
		KBRoot:   kbRoot, GufiIndex: gufiIndex, SemanticMapDir: semanticMapDir,
		IndexBase: indexBase, Log: log, DelayScale: 1, RemoteCheck: time.Hour,
		RemoteFiles: remoteMapFiles,
		states:      map[string]*state{}, neighbors: map[string]*neighborCache{},
	}
}

// ownerKeys is keyOf(owner): the owner's personal key from the key store;
// the deployment's on a Studio without sign-in.
type ownerKeys struct{ a *app.App }

func (k ownerKeys) KeyOf(owner string) string {
	if owner == "single" {
		if k.a.Cfg.AuthEnabled() {
			return ""
		}
		return platform.Of(k.a).GatewayKey()
	}
	if f := k.a.Providers.PersonalCred; f != nil {
		if key, base, ok := f(owner, ""); ok && base == "" {
			return key
		}
	}
	return ""
}

// viewerOf is req.user?.id ?? null as Node's routes read it, through the
// platform track's ViewerOf as libapi's routes take it (the front's user,
// or Node's answer over the bridge).
func viewerOf(a *app.App, r *http.Request) (libraries.Viewer, error) {
	v, err := platform.Of(a).ViewerOf(r)
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
		return libraries.Deployment, nil
	}
	return libraries.ViewerOf(""), nil
}

func Register(mux *http.ServeMux, a *app.App) {
	m := Of(a)
	m.Routes(mux, a.Log)
}

// Routes registers the nine routes of semanticMapRoutes and the bridge's.
func (m *Module) Routes(mux *http.ServeMux, log *slog.Logger) {
	h := func(fn func(r *http.Request, viewer libraries.Viewer) (any, error)) http.HandlerFunc {
		return platform.Handler(log, func(w http.ResponseWriter, r *http.Request) (any, error) {
			v := libraries.ViewerOf("")
			if m.Viewer != nil {
				var err error
				if v, err = m.Viewer(r); err != nil {
					return nil, err
				}
			}
			return fn(r, v)
		})
	}
	kb := libraries.PrimaryID
	q := func(r *http.Request, k string) (string, bool) {
		vs, ok := r.URL.Query()[k]
		if !ok || len(vs) == 0 {
			return "", false
		}
		return vs[0], true
	}
	cluster := func(id string) func(r *http.Request, v libraries.Viewer) (any, error) {
		return func(r *http.Request, v libraries.Viewer) (any, error) {
			lib := id
			if lib == "" {
				lib = r.PathValue("id")
			}
			off, limit := 0.0, 2000.0
			if s, ok := q(r, "offset"); ok {
				off = jsNumber(s)
			}
			if s, ok := q(r, "limit"); ok {
				limit = jsNumber(s)
			}
			return m.Cluster(jsNumber(r.PathValue("c")), off, limit, lib, v)
		}
	}
	neighbors := func(id string) func(r *http.Request, v libraries.Viewer) (any, error) {
		return func(r *http.Request, v libraries.Viewer) (any, error) {
			lib := id
			if lib == "" {
				lib = r.PathValue("id")
			}
			file, _ := q(r, "file")
			k := 5.0
			if s, ok := q(r, "k"); ok {
				k = jsNumber(s)
			}
			return m.Neighbors(r.Context(), file, k, lib, v)
		}
	}
	mux.HandleFunc("GET /api/kb/semantic-map", h(func(r *http.Request, v libraries.Viewer) (any, error) {
		return m.Overview(kb, v, true)
	}))
	mux.HandleFunc("GET /api/kb/semantic-map/cluster/{c}", h(cluster(kb)))
	mux.HandleFunc("GET /api/kb/semantic-map/neighbors", h(neighbors(kb)))
	mux.HandleFunc("GET /api/semantic-maps", h(func(r *http.Request, v libraries.Viewer) (any, error) {
		return jsonv.NewObject("maps", m.List(m.Reg.List("", v), v)), nil
	}))
	mux.HandleFunc("GET /api/libraries/{id}/semantic-map", h(func(r *http.Request, v libraries.Viewer) (any, error) {
		return m.Overview(r.PathValue("id"), v, true)
	}))
	mux.HandleFunc("GET /api/libraries/{id}/semantic-map/cluster/{c}", h(cluster("")))
	mux.HandleFunc("GET /api/libraries/{id}/semantic-map/neighbors", h(neighbors("")))
	mux.HandleFunc("POST /api/libraries/{id}/semantic-map/refresh", h(func(r *http.Request, v libraries.Viewer) (any, error) {
		return m.Refresh(r.PathValue("id"), v)
	}))
	mux.HandleFunc("POST /api/kb/semantic-map/refresh", h(func(r *http.Request, v libraries.Viewer) (any, error) {
		return m.Refresh(kb, v)
	}))

	// Node, beside: a map to make again (indexing.ts, libraryBuild.ts), a
	// library it removed, and its tests' reset.
	b := func(fn func(body *jsonv.Object) any) http.HandlerFunc {
		return platform.Handler(log, func(w http.ResponseWriter, r *http.Request) (any, error) {
			if err := platform.RequireSecret(r); err != nil {
				return nil, err
			}
			body, err := platform.BodyObject(r)
			if err != nil {
				return nil, err
			}
			return fn(body), nil
		})
	}
	mux.HandleFunc("POST "+BridgePrefix+"schedule", b(func(body *jsonv.Object) any {
		id := jsonv.String(get(body, "id"))
		if id == "" {
			id = kb
		}
		ms, _ := jsonv.Num(get(body, "delayMs"))
		m.Schedule(id, time.Duration(ms*float64(time.Millisecond)))
		return jsonv.NewObject("ok", true)
	}))
	mux.HandleFunc("POST "+BridgePrefix+"removed", b(func(body *jsonv.Object) any {
		m.LibraryRemoved(jsonv.String(get(body, "id")))
		return jsonv.NewObject("ok", true)
	}))
	mux.HandleFunc("POST "+BridgePrefix+"reset", b(func(body *jsonv.Object) any {
		m.ResetForTests()
		return jsonv.NewObject("ok", true)
	}))
}

func get(o *jsonv.Object, k string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(k)
	return v
}

// jsNumber is JavaScript's Number(s) for a query or path value.
func jsNumber(s string) float64 {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0
	}
	switch t {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if len(t) > 2 && t[0] == '0' && (t[1] == 'x' || t[1] == 'X' || t[1] == 'o' || t[1] == 'O' || t[1] == 'b' || t[1] == 'B') {
		base := map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[t[1]]
		if n, err := strconv.ParseUint(t[2:], base, 64); err == nil {
			return float64(n)
		}
		return math.NaN()
	}
	low := strings.ToLower(t)
	if strings.Contains(low, "inf") || strings.Contains(low, "nan") || strings.ContainsAny(t, "_") || strings.HasPrefix(low, "0x") {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		if e, ok := err.(*strconv.NumError); ok && e.Err == strconv.ErrRange {
			return f
		}
		return math.NaN()
	}
	return f
}

// jsFloorOr is Math.floor(v) || d.
func jsFloorOr(v, d float64) float64 {
	f := math.Floor(v)
	if math.IsNaN(f) || f == 0 {
		return d
	}
	return f
}

func (m *Module) stateOf(key string) *state {
	st := m.states[key]
	if st == nil {
		st = &state{}
		m.states[key] = st
	}
	return st
}

func (m *Module) stopTimers() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, st := range m.states {
		if st.timer != nil {
			st.timer.Stop()
			st.timer = nil
		}
	}
}

// ResetForTests is resetSemanticMapForTests: every map's state forgotten.
func (m *Module) ResetForTests() {
	m.stopTimers()
	m.mu.Lock()
	m.states = map[string]*state{}
	m.neighbors = map[string]*neighborCache{}
	m.mu.Unlock()
}

// LibraryRemoved is onLibraryRemoved: the map goes with the library, from
// disk and from memory.
func (m *Module) LibraryRemoved(id string) {
	if id == "" || id == libraries.PrimaryID {
		return
	}
	m.forget(id)
	os.RemoveAll(m.mapDir(id))
}

// UserLibrary is userLibraries.ts's hooks for a library on another system:
// removed (its state goes; its folder goes with the library) or built
// there (a check whether its vectors changed, in five seconds).
func (m *Module) UserLibrary(event, root string) {
	switch event {
	case "removed":
		m.forget(root)
	case "built":
		id := root[strings.LastIndex(root, ":")+1:]
		t, err := m.remoteMapTarget(id, root)
		if err != nil {
			return
		}
		m.scheduleRemote(t, false, 5*time.Second)
	}
}

func (m *Module) forget(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st := m.states[key]; st != nil && st.timer != nil {
		st.timer.Stop()
	}
	delete(m.states, key)
	delete(m.neighbors, key)
}

// mapDir is semanticMapDir: the folder a library's map is kept in.
func (m *Module) mapDir(id string) string {
	if id == libraries.PrimaryID {
		return m.SemanticMapDir
	}
	return filepath.Join(m.Reg.BuiltLibraryDir(id), "semantic-map")
}

// libraryOf is the library a map would be of, and what it can be made
// from: its MapLibrary as the page reads it.
func (m *Module) libraryOf(id string, viewer libraries.Viewer) (*jsonv.Object, Target, bool, error) {
	if id == libraries.PrimaryID {
		meaningOff := m.Settings.Meaning(libraries.PrimaryID) == "off"
		label := filepath.Base(m.KBRoot)
		if m.KBRoot == "" || label == "/" || label == "." {
			label = "main folder"
		}
		lib := jsonv.NewObject("id", id, "label", label, "kind", "main", "depth", m.Settings.Depth(libraries.PrimaryID), "vectors", !meaningOff)
		return lib, Target{ID: id, Key: id, Index: m.GufiIndex, Out: m.SemanticMapDir}, meaningOff, nil
	}
	l, err := m.Reg.Get(id, "", viewer)
	if err != nil {
		return nil, Target{}, false, err
	}
	kind := "connected"
	switch {
	case l.Site != "":
		kind = "remote"
	case l.Build != nil:
		kind = "built"
	}
	meaningOff := false
	if kind == "built" || (kind == "remote" && l.Depth != "") {
		meaningOff = l.Meaning == "" || l.Meaning == "off"
	}
	var depth any
	if l.Depth != "" {
		depth = l.Depth
	}
	lib := jsonv.NewObject("id", id, "label", l.Label, "kind", kind, "depth", depth, "vectors", !m.Reg.Lacks(l, "vectors"))
	if l.Site != "" {
		lib.Set("host", remote.SiteName(l.Site))
	}
	if kind == "remote" {
		t, err := m.remoteMapTarget(id, l.IndexRoot)
		if err != nil {
			return nil, Target{}, false, err
		}
		return lib, t, meaningOff, nil
	}
	return lib, Target{ID: id, Key: id, Index: l.IndexRoot, Out: m.mapDir(id)}, meaningOff, nil
}

func (m *Module) remoteMapTarget(id, root string) (Target, error) {
	home, err := m.Reg.Users().RemoteMapHome(root)
	if err != nil {
		return Target{}, err
	}
	return Target{ID: id, Key: root, Index: root, Out: filepath.Join(home, "semantic-map"), Remote: &RemoteTarget{Root: root, Home: home}}, nil
}

// readJSON is readJson: a file's object, kept while its stamp holds.
func readJSON(file string, c *cached) *cached {
	fi, err := os.Stat(file)
	if err != nil {
		return nil
	}
	stamp := strconv.FormatInt(fi.ModTime().UnixNano(), 10) + ":" + strconv.FormatInt(fi.Size(), 10)
	if c != nil && c.stamp == stamp {
		return c
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	v, err := jsonv.Decode(b)
	if err != nil || jsonv.Obj(v) == nil {
		return nil
	}
	return &cached{stamp: stamp, data: jsonv.Obj(v)}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func errOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// Overview is semanticMapOverview: kick false (the picker's list) starts
// no fetch from another system.
func (m *Module) Overview(id string, viewer libraries.Viewer, kick bool) (*jsonv.Object, error) {
	lib, t, meaningOff, err := m.libraryOf(id, viewer)
	if err != nil {
		return nil, err
	}
	if meaningOff {
		return jsonv.NewObject("available", false, "reason", "off", "building", false, "library", lib), nil
	}
	if t.Remote != nil {
		return m.remoteOverview(lib, t, kick), nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.stateOf(t.Key)
	if hit := readJSON(filepath.Join(t.Out, "overview.json"), st.overview); hit != nil && jsonv.Truthy(get(hit.data, "available")) {
		st.overview = hit
		return hit.data.Clone().Set("building", st.running).Set("library", lib), nil
	}
	// An index used as it is, with no vectors the Studio can read: nothing to make.
	if jsonv.Str(get(lib, "kind")) == "connected" && (!jsonv.Truthy(get(lib, "vectors")) || st.empty) {
		return jsonv.NewObject("available", false, "reason", "connected", "building", false, "library", lib), nil
	}
	// None yet: made once, in the background, when there is an index to
	// make it from.
	if !st.firstTried && !st.running && exists(filepath.Join(t.Index, "db.db")) {
		st.firstTried = true
		m.scheduleLocked(id, 0)
	}
	reason := "none"
	if st.err != nil {
		reason = "failed"
	}
	return jsonv.NewObject("available", false, "reason", reason, "building", st.running || st.timer != nil, "error", errOrNil(st.err), "library", lib), nil
}

// List is semanticMapList: every library's map at a glance.
func (m *Module) List(libs []libraries.Library, viewer libraries.Viewer) []any {
	out := []any{}
	for _, l := range libs {
		o, err := m.Overview(l.ID, viewer, false)
		if err != nil {
			out = append(out, jsonv.NewObject("id", l.ID, "label", l.Label, "available", false, "files", float64(0), "reason", "none", "building", false))
			continue
		}
		files, ok := jsonv.Num(get(o, "files"))
		if !ok {
			files = 0
		}
		e := jsonv.NewObject("id", l.ID, "label", l.Label, "available", get(o, "available"), "files", files)
		if r := jsonv.Str(get(o, "reason")); r != "" {
			e.Set("reason", r)
		}
		e.Set("building", get(o, "building"))
		out = append(out, e)
	}
	return out
}

// ClusterPageMax is CLUSTER_PAGE_MAX: a cluster's points per request, at most.
const ClusterPageMax = 5000

// Cluster is semanticMapCluster: one cluster's files, a page at a time.
func (m *Module) Cluster(c, offset, limit float64, id string, viewer libraries.Viewer) (any, error) {
	if math.IsNaN(c) || math.IsInf(c, 0) || c != math.Trunc(c) || c < 0 {
		return nil, apierr.New(400, "cluster: a number from 0")
	}
	_, t, _, err := m.libraryOf(id, viewer)
	if err != nil {
		return nil, err
	}
	n := int(c)
	m.mu.Lock()
	st := m.stateOf(t.Key)
	var prev *cached
	for _, e := range st.clusters {
		if e.c == n {
			p := e.cached
			prev = &p
		}
	}
	hit := readJSON(filepath.Join(t.Out, "cluster-"+strconv.Itoa(n)+".json"), prev)
	if hit != nil {
		kept := st.clusters[:0]
		for _, e := range st.clusters {
			if e.c != n {
				kept = append(kept, e)
			}
		}
		kept = append(kept, clusterEntry{n, *hit})
		// A few clusters are kept; the oldest goes.
		if len(kept) > 4 {
			kept = kept[len(kept)-4:]
		}
		st.clusters = kept
	}
	m.mu.Unlock()
	if hit == nil {
		return nil, apierr.New(404, "There is no such cluster in the map.")
	}
	start := math.Max(0, jsFloorOr(offset, 0))
	size := math.Min(ClusterPageMax, math.Max(1, jsFloorOr(limit, 2000)))
	points := jsonv.Arr(get(hit.data, "points"))
	lo := len(points)
	if start < float64(lo) {
		lo = int(start)
	}
	hi := len(points)
	if float64(lo)+size < float64(hi) {
		hi = lo + int(size)
	}
	page := append([]any{}, points[lo:hi]...)
	return jsonv.NewObject("c", get(hit.data, "c"), "label", get(hit.data, "label"), "count", get(hit.data, "count"), "offset", start, "points", page), nil
}

// Schedule is scheduleSemanticMapRebuild: a new map of a library in
// delay. Calls close together make one build, the earliest time wins, and
// a call while a build runs makes one more after it.
func (m *Module) Schedule(id string, delay time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scheduleLocked(id, delay)
}

func (m *Module) scheduleLocked(id string, delay time.Duration) {
	st := m.stateOf(id)
	if st.running {
		st.again = true
		return
	}
	if delay < 0 {
		delay = 0
	}
	delay = time.Duration(float64(delay) * m.DelayScale)
	due := time.Now().Add(delay)
	if st.timer != nil && !st.dueAt.After(due) {
		return
	}
	if st.timer != nil {
		st.timer.Stop()
	}
	st.dueAt = due
	var tm *time.Timer
	tm = time.AfterFunc(delay, func() {
		m.mu.Lock()
		if st.timer == tm {
			st.timer = nil
		}
		m.mu.Unlock()
		m.rebuild(id)
	})
	st.timer = tm
}

func (m *Module) rebuild(id string) {
	m.mu.Lock()
	st := m.stateOf(id)
	if st.running {
		st.again = true
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	_, t, meaningOff, err := m.libraryOf(id, libraries.Deployment)
	if err != nil || meaningOff || t.Remote != nil {
		return // removed meanwhile, or not one made from an index here
	}
	m.mu.Lock()
	st = m.stateOf(id)
	if st.running {
		st.again = true
		m.mu.Unlock()
		return
	}
	st.running = true
	m.mu.Unlock()
	berr := m.build(t)
	m.mu.Lock()
	defer m.mu.Unlock()
	if berr != nil {
		msg := cut(berr.Error(), 400)
		st.err = &msg
		m.Log.Warn("semantic map ("+id+"): "+msg, "library", id)
	} else {
		st.err = nil
		st.empty = !exists(filepath.Join(t.Out, "overview.json"))
	}
	st.running = false
	st.lastEnd = time.Now()
	if st.again {
		st.again = false
		m.scheduleLocked(id, time.Minute)
	}
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// build makes t's map: from its index, or from the vectors fetched.
func (m *Module) build(t Target) error {
	if m.BuildLog != "" {
		if f, err := os.OpenFile(m.BuildLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			b, _ := json.Marshal(map[string]string{"id": t.ID, "key": t.Key, "out": t.Out})
			f.Write(append(b, '\n'))
			f.Close()
		}
	}
	if m.Build != nil {
		return m.Build(t)
	}
	var src semanticmap.Source
	opt := semanticmap.Options{Sample: 3000, Fit: 20000}
	if t.Remote != nil && t.Remote.Vectors != "" {
		src = semanticmap.VectorsSource(t.Remote.Vectors, t.Remote.Model)
		if t.Remote.Of > 0 {
			of := t.Remote.Of
			opt.Of = &of
		}
	} else {
		src = semanticmap.IndexSource(t.Index)
	}
	sum, err := semanticmap.Build(src, t.Out, opt)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(sum)
	m.Log.Info("semantic map ("+t.ID+"): "+string(b), "library", t.ID)
	return nil
}

// Refresh is refreshSemanticMap: a library's map made again now; for one
// on another system, its vectors fetched again.
func (m *Module) Refresh(id string, viewer libraries.Viewer) (any, error) {
	_, t, meaningOff, err := m.libraryOf(id, viewer)
	if err != nil {
		return nil, err
	}
	if meaningOff {
		return nil, apierr.New(409, "This library has no search by meaning, so there is no map to make.")
	}
	m.mu.Lock()
	st := m.stateOf(t.Key)
	st.err = nil
	m.mu.Unlock()
	if t.Remote != nil {
		if _, err := m.remoteTarget(t.Remote.Root); err != nil {
			return nil, err
		}
		m.mu.Lock()
		st.firstTried = true
		m.mu.Unlock()
		m.scheduleRemote(t, true, 0)
	} else {
		m.Schedule(id, 0)
	}
	return m.Overview(id, viewer, true)
}

// background is the context the work a route starts runs in.
func background() context.Context { return context.Background() }
