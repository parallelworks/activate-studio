// Package userlibs is userLibraries.ts: libraries a user adds from another
// system, over plain ssh or pw ssh. An existing GUFI index is connected
// after a connection test; an index the Studio builds there runs as a
// process of its own on that system, followed here as an index job; on a
// platform-hosted Studio the index workflow builds one on a cluster. The
// /api/user-libraries routes, and GET /api/whoami where the Go front does
// not verify identity itself (whoami.go).
//
// Browsing and searching a library on another system (remoteTarget,
// remoteQuery, remoteTree, remoteFile) belong to the libraries track; this
// package keeps the store they read (libraries.Users) and shares the
// session pool with them (remote.Shared).
package userlibs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/search"
)

// Module is the user libraries of one server.
type Module struct {
	P           *platform.Platform
	Users       *libraries.Users
	Settings    *libraries.FileIndexSettings
	Pool        *remote.Pool
	Runners     *remote.Runners
	IndexBase   string
	ProjectRoot string
	Log         *slog.Logger
	// Jobs is where a build is followed: the files track's jobs, which
	// GET /api/jobs/:id serves.
	Jobs func() *kb.Jobs
	// PollEvery is how often a build there is read (setBuildPollMs).
	PollEvery time.Duration
	// Python runs fetch_embed.py here (PYTHON_BIN).
	Python func() string
	// Notify tells Node's modules that keep something per library on
	// another system (semanticMap.ts) that one was removed or built.
	Notify func(event, root string)
	// Background is the context builds are followed and files sent in.
	Background func() context.Context

	mu        sync.Mutex
	passed    map[string]passedProbe
	checked   map[string]string
	sending   map[string]*sync.Mutex
	followers map[string]*follower
	embedSent int
	pinned    pinsFile
	eng       *search.Engine
	py        string
	pyOnce    sync.Once
}

type follower struct {
	id      int
	running bool
}

var (
	instMu sync.Mutex
	inst   = map[*app.App]*Module{}
)

// Of is a's module, made on first use; the libraries track reads the same
// store through it (Of(a).Users).
func Of(a *app.App) *Module {
	instMu.Lock()
	defer instMu.Unlock()
	if m := inst[a]; m != nil {
		return m
	}
	p := platform.Of(a)
	pool, runners := remote.Shared(a, func() remote.Options {
		return remote.Options{IndexBase: a.Cfg.IndexBase, PwCLI: p.CLI, GatewayBase: p.GatewayBase}
	})
	m := New(p, pool, runners, a.Cfg.IndexBase, a.Cfg.ProjectRoot, a.Log)
	m.Jobs = func() *kb.Jobs {
		if kb.Default != nil {
			return kb.Default.Jobs
		}
		return m.ownJobs()
	}
	m.Background = func() context.Context { return context.Background() }
	m.Notify = func(event, root string) {
		if n := p.Node(); n != nil {
			if err := n.Background(http.MethodPost, platform.BridgePrefix+"user-library", map[string]string{"event": event, "root": root}, nil); err != nil {
				m.Log.Warn("telling Node of a user library", "event", event, "root", root, "err", err.Error())
			}
		}
	}
	a.OnStop("user libraries: ssh sessions", func() int { pool.Close(); return 0 })
	inst[a] = m
	return m
}

var localJobs = &kb.Jobs{}

func (m *Module) ownJobs() *kb.Jobs { return localJobs }

// New is a module over a pool, for Of and tests.
func New(p *platform.Platform, pool *remote.Pool, runners *remote.Runners, indexBase, projectRoot string, log *slog.Logger) *Module {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if runners == nil {
		runners = &remote.Runners{Pool: pool}
	}
	m := &Module{P: p, Pool: pool, Runners: runners, IndexBase: indexBase, ProjectRoot: projectRoot, Log: log,
		Users:     &libraries.Users{IndexBase: indexBase},
		Settings:  libraries.NewFileIndexSettings(indexBase),
		PollEvery: 3 * time.Second,
		passed:    map[string]passedProbe{}, checked: map[string]string{}, sending: map[string]*sync.Mutex{},
		followers: map[string]*follower{},
	}
	m.Jobs = m.ownJobs
	m.Background = context.Background
	m.Notify = func(string, string) {}
	return m
}

func (m *Module) engine() *search.Engine {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.eng == nil {
		m.eng = &search.Engine{Pool: m.Pool, Remote: m.Runners}
	}
	return m.eng
}

func (m *Module) getenv(k string) string { return m.P.Getenv(k) }

// python is PYTHON_BIN: the one set, else the newest python3 on the PATH
// that is 3.10 or later, else python3.
func (m *Module) python() string {
	if m.Python != nil {
		return m.Python()
	}
	m.pyOnce.Do(func() {
		if v := m.getenv("PYTHON_BIN"); v != "" {
			m.py = v
			return
		}
		for _, c := range []string{"python3.13", "python3.12", "python3.11", "python3.10", "python3"} {
			out, err := exec.Command(c, "-c", "import sys; print(sys.version_info >= (3, 10))").Output()
			if err == nil && strings.TrimSpace(string(out)) == "True" {
				m.py = c
				return
			}
		}
		m.py = "python3"
	})
	return m.py
}

// ---- who acts ----

// who is a request's viewer, as the rules read it.
type who struct {
	ID   string
	Auth bool
	// Own is the viewer's own platform key, "" for none.
	Own string
}

func (m *Module) whoOf(r *http.Request) (who, error) {
	v, err := m.P.ViewerOf(r)
	if err != nil {
		// Node turned the request away (AUTH_REQUIRED): its answer.
		var ne *app.NodeError
		if errors.As(err, &ne) && ne.Status == http.StatusUnauthorized {
			msg := "authentication required"
			if b, _ := jsonv.Decode(ne.Body); jsonv.Str(get(jsonv.Obj(b), "error")) != "" {
				msg = jsonv.Str(get(jsonv.Obj(b), "error"))
			}
			return who{}, apierr.New(401, "%s", msg)
		}
		return who{}, err
	}
	return who{ID: v.UserID(), Auth: v.AuthEnabled, Own: v.PlatformKey()}, nil
}

const single = "single"

// owner is ownerOf: the viewer's id, or "single" when sign-in is off.
func (w who) owner() string {
	if w.ID != "" {
		return w.ID
	}
	if w.Auth {
		return ""
	}
	return single
}

// keyOf is the key an owner acts with: their own; the deployment's only on
// a one-person Studio.
func (m *Module) keyOf(w who, owner string) string {
	if owner != single && w.Own != "" {
		return w.Own
	}
	if w.Auth {
		return ""
	}
	return m.P.GatewayKey()
}

// sshAllowed: libraries over ssh only for the one user of a Studio with
// sign-in off.
func (w who) sshAllowed() bool { return !w.Auth && w.owner() == single }

func errSSHSingle() error { return apierr.New(403, "%s", libraries.SSHSingleUser) }

// pwKey is the key a viewer's pw ssh acts with, or why there is none.
func (m *Module) pwKey(w who) (owner, key string, err error) {
	owner = w.owner()
	if owner == "" {
		return "", "", apierr.New(401, "sign in to add your own libraries")
	}
	key = m.keyOf(w, owner)
	if key == "" {
		if owner == single {
			return "", "", apierr.New(409, "The pw CLI on this computer is not signed in. Run pw auth, then test again.")
		}
		return "", "", apierr.New(409, "Indexes on your clusters are read with your own platform key: add your ACTIVATE API key under Settings, Model access.")
	}
	return owner, key, nil
}

// pwUnavailable is why a viewer cannot use pw ssh, or "" when they can.
func (m *Module) pwUnavailable(w who) string {
	owner := w.owner()
	if owner == "" {
		return "Sign in to add your own libraries."
	}
	if found, _ := m.P.FindCLI(); found == "" {
		set := ""
		if env := m.getenv("PW_CLI"); strings.Contains(env, "/") {
			set = " PW_CLI names " + env + ", which is not there."
		}
		return "The Studio does not find the pw CLI on this computer." + set + " Install it, or choose where it is under Settings, Model access, pw CLI."
	}
	if m.keyOf(w, owner) == "" {
		if owner == single {
			return "The pw CLI on this computer is not signed in to a platform. Sign it in under Settings, Model access."
		}
		return "Systems on the platform are reached with your own platform key: add your ACTIVATE API key under Settings, Model access."
	}
	return ""
}

// workflowOffered: the index workflow builds on a cluster on a Studio the
// platform hosts.
func (m *Module) workflowOffered(w who) bool { return w.Auth || m.getenv("PW_PLATFORM_HOST") != "" }

// ---- the store ----

func (m *Module) load(owner string) []libraries.UserDef { return m.Users.Load(owner) }

func find(defs []libraries.UserDef, id string) *libraries.UserDef {
	for i := range defs {
		if defs[i].ID == id {
			return &defs[i]
		}
	}
	return nil
}

func (m *Module) update(owner, id string, patch func(*libraries.UserDef)) *libraries.UserDef {
	return m.Users.Update(owner, id, patch)
}

func (m *Module) appendDef(owner string, d libraries.UserDef) error {
	return m.Users.Modify(owner, func(defs []libraries.UserDef) ([]libraries.UserDef, error) {
		return append(defs, d), nil
	})
}

// uniqueID is the base id, or base-2, base-3 and on.
func uniqueID(defs []libraries.UserDef, base string) string {
	id := base
	for i := 2; find(defs, id) != nil; i++ {
		id = base + "-" + strconv.Itoa(i)
	}
	return id
}

func (m *Module) tellBuilt(owner, id string) {
	root := libraries.RemoteRootOf(owner, id)
	m.Users.TellBuilt(root)
	m.Notify("built", root)
}

func isoNow() string { return platform.NowISO() }

// sshSiteOf is the ssh site of a library over plain ssh; "" for a cluster.
func sshSiteOf(d *libraries.UserDef) string {
	if !d.IsSSH() || deref(d.Destination) == "" {
		return ""
	}
	site, err := remote.SSHSite(*d.Destination, intOf(d.Port), d.SSHOptions)
	if err != nil {
		return ""
	}
	return site
}

func intOf(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// ---- the routes ----

// Register registers the routes on mux.
func Register(mux *http.ServeMux, a *app.App) {
	m := Of(a)
	m.Routes(mux, a.Log)
	if !a.Cfg.FrontAuth {
		registerWhoami(mux, a)
	}
}

// Routes registers the fifteen /api/user-libraries routes.
func (m *Module) Routes(mux *http.ServeMux, log *slog.Logger) {
	h := func(read bool, fn func(r *http.Request, w who, body *jsonv.Object) (any, error)) http.HandlerFunc {
		return platform.Handler(log, func(rw http.ResponseWriter, r *http.Request) (any, error) {
			// Fastify parses a body on every route, so a malformed one fails
			// the same way whether the route reads it or not.
			var body *jsonv.Object
			if read {
				b, err := platform.BodyObject(r)
				if err != nil {
					return nil, err
				}
				body = b
			}
			w, err := m.whoOf(r)
			if err != nil {
				return nil, err
			}
			return fn(r, w, body)
		})
	}
	const base = "/api/user-libraries"
	mux.HandleFunc("GET "+base, h(false, m.list))
	mux.HandleFunc("GET "+base+"/pw/systems", h(false, m.pwSystems))
	mux.HandleFunc("POST "+base, h(true, func(r *http.Request, w who, b *jsonv.Object) (any, error) {
		d, err := m.addFromCluster(r.Context(), w, b)
		if err != nil {
			return nil, err
		}
		return m.public(d, 0), nil
	}))
	mux.HandleFunc("POST "+base+"/ssh/test", h(true, func(r *http.Request, w who, b *jsonv.Object) (any, error) {
		if !w.sshAllowed() {
			return nil, errSSHSingle()
		}
		spec, err := sshSpecOf(b)
		if err != nil {
			return nil, err
		}
		p, err := m.probeSSH(r.Context(), spec, false)
		if err != nil {
			return nil, err
		}
		return p.testJSON(), nil
	}))
	mux.HandleFunc("POST "+base+"/ssh", h(true, func(r *http.Request, w who, b *jsonv.Object) (any, error) {
		d, err := m.addFromSSH(r.Context(), w, b)
		if err != nil {
			return nil, err
		}
		return m.public(d, 0), nil
	}))
	mux.HandleFunc("POST "+base+"/pw/test", h(true, func(r *http.Request, w who, b *jsonv.Object) (any, error) {
		_, key, err := m.pwKey(w)
		if err != nil {
			return nil, err
		}
		spec, err := pwSpecOf(b)
		if err != nil {
			return nil, err
		}
		return m.probePw(r.Context(), spec, key, false).testJSON(), nil
	}))
	mux.HandleFunc("POST "+base+"/pw", h(true, func(r *http.Request, w who, b *jsonv.Object) (any, error) {
		d, err := m.addFromPw(r.Context(), w, b)
		if err != nil {
			return nil, err
		}
		return m.public(d, 0), nil
	}))
	for _, t := range []string{"ssh", "pw"} {
		mux.HandleFunc("POST "+base+"/"+t+"/build/test", h(true, func(r *http.Request, w who, b *jsonv.Object) (any, error) {
			req, err := m.buildRequest(w, t, b)
			if err != nil {
				return nil, err
			}
			return m.probeBuild(r.Context(), req.host, req.spec).buildJSON(), nil
		}))
		mux.HandleFunc("POST "+base+"/"+t+"/build", h(true, func(r *http.Request, w who, b *jsonv.Object) (any, error) {
			d, job, err := m.addBuild(r.Context(), w, t, b)
			if err != nil {
				return nil, err
			}
			return m.public(d, job), nil
		}))
	}
	mux.HandleFunc("POST "+base+"/{id}/refresh", h(true, func(r *http.Request, w who, _ *jsonv.Object) (any, error) {
		d, err := m.refresh(r.Context(), w, r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		return m.public(d, 0), nil
	}))
	mux.HandleFunc("PUT "+base+"/{id}/ssh-options", h(true, func(r *http.Request, w who, b *jsonv.Object) (any, error) {
		d, err := m.setSSHOptions(r.Context(), w, r.PathValue("id"), field(b, "sshOptions"))
		if err != nil {
			return nil, err
		}
		return m.public(d, 0), nil
	}))
	// Search and Query call this as they open, so the first query after a
	// long gap does not wait for the ssh login.
	mux.HandleFunc("POST "+base+"/warm", h(true, func(_ *http.Request, w who, _ *jsonv.Object) (any, error) {
		return jsonv.NewObject("hosts", float64(m.warm(w))), nil
	}))
	mux.HandleFunc("DELETE "+base+"/{id}", h(true, func(r *http.Request, w who, _ *jsonv.Object) (any, error) {
		if err := m.remove(w, r.PathValue("id")); err != nil {
			return nil, err
		}
		return jsonv.NewObject("ok", true), nil
	}))
}

func (m *Module) list(r *http.Request, w who, _ *jsonv.Object) (any, error) {
	libs := []any{}
	for _, d := range m.listDefs(r.Context(), w) {
		libs = append(libs, m.public(&d, 0))
	}
	why := m.pwUnavailable(w)
	return jsonv.NewObject("libraries", libs, "available", w.owner() != "", "ssh", w.sshAllowed(), "pw", why == "",
		"pwOffered", why == "" || w.sshAllowed(), "pwWhy", nullable(why), "workflow", m.workflowOffered(w)), nil
}

// listDefs is a viewer's libraries, with any running build followed one
// step.
func (m *Module) listDefs(ctx context.Context, w who) []libraries.UserDef {
	owner := w.owner()
	if owner == "" {
		return []libraries.UserDef{}
	}
	if key := m.keyOf(w, owner); key != "" {
		for _, d := range m.load(owner) {
			m.checkRun(ctx, owner, d, key)
		}
	}
	// A build on a remote system goes on without the Studio; one not being
	// followed (after a restart, say) is followed again.
	for _, d := range m.load(owner) {
		if d.Build != nil && (d.State == "building" || d.State == "updating") {
			m.follow(w, owner, d.ID)
		}
	}
	defs := m.load(owner)
	// Where each ssh library lands, so the next listing names the account
	// and host reached. Never waited on.
	for i := range defs {
		if site := sshSiteOf(&defs[i]); site != "" {
			go m.Pool.ReachedHost(site)
		}
	}
	return defs
}

func (m *Module) pwSystems(r *http.Request, w who, _ *jsonv.Object) (any, error) {
	if why := m.pwUnavailable(w); why != "" {
		return jsonv.NewObject("usable", false, "why", why, "systems", []any{}, "error", nil), nil
	}
	systems, err := m.listPwSystems(r.Context(), w)
	if err != nil {
		return jsonv.NewObject("usable", true, "why", nil, "systems", []any{}, "error", err.Error()), nil
	}
	return jsonv.NewObject("usable", true, "why", nil, "systems", systems, "error", nil), nil
}

// public is publicUserLibrary: the shape Settings shows, no keys.
func (m *Module) public(d *libraries.UserDef, job int) *jsonv.Object {
	transport := d.Transport
	if transport == "" {
		transport = "pw"
	}
	built := d.Build != nil
	var meaning, depth any
	if built {
		meaning, depth = m.Settings.Meaning(d.ID), m.Settings.Depth(d.ID)
	}
	var reached any
	if site := sshSiteOf(d); site != "" {
		if r := m.Pool.ReachedKnown(site); r != nil {
			reached = r.Label
		}
	}
	var sshOptions, jump any
	if len(d.SSHOptions) > 0 {
		sshOptions = remote.OptionsText(d.SSHOptions)
		jump = nullable(remote.JumpOf(d.SSHOptions))
	}
	var run any
	if d.Run != nil {
		run = jsonv.NewObject("slug", d.Run.Slug, "mode", d.Run.Mode, "startedAt", d.Run.StartedAt)
	}
	var scheduler any
	if d.Scheduler != nil {
		s := jsonv.NewObject()
		if d.Scheduler.Partition != "" {
			s.Set("partition", d.Scheduler.Partition)
		}
		if d.Scheduler.Account != "" {
			s.Set("account", d.Scheduler.Account)
		}
		if d.Scheduler.QOS != "" {
			s.Set("qos", d.Scheduler.QOS)
		}
		if d.Scheduler.Time != "" {
			s.Set("time", d.Scheduler.Time)
		}
		scheduler = s
	}
	var formatNote any
	if n := libraries.FormatNote(d.IndexFormat); n != nil {
		formatNote = *n
	}
	var readThrough any
	if rt := libraries.PublicReadThrough(d.ReadThrough); rt != nil {
		readThrough = jsonv.NewObject("destination", rt.Destination, "port", intPtr(rt.Port), "sshOptions", sptr(rt.SSHOptions), "root", sptr(rt.Root))
	}
	var indexDir, progress, buildingDepth any
	if built {
		indexDir = d.Build.IndexDir
		if pr := d.Build.Progress; pr != nil {
			progress = jsonv.NewObject("phase", pr.Phase, "done", float64(pr.Done), "total", float64(pr.Total), "current", sptr(pr.Current))
		}
		if (d.State == "building" || d.State == "updating") && d.Build.Depth != "" {
			buildingDepth = d.Build.Depth
		}
	}
	gaps := []any{}
	for _, g := range d.Gaps {
		gaps = append(gaps, g)
	}
	o := jsonv.NewObject("id", d.ID, "label", d.Label, "transport", transport, "existing", !built && (d.IsSSH() || (d.Existing != nil && *d.Existing)),
		"meaning", meaning, "depth", depth, "site", d.Site, "destination", optional(d.Destination), "reached", reached,
		"port", intPtr(d.Port), "sshOptions", sshOptions, "jump", jump, "gufiVersion", optional(d.GufiVersion),
		"sourceDir", d.SourceDir, "sourceRoot", optional(d.SourceRoot), "indexRoot", optional(d.IndexRoot), "state", d.State,
		"run", run, "error", optional(d.Error), "builtAt", optional(d.BuiltAt),
		"directories", int64Ptr(d.Directories), "files", int64Ptr(d.Files), "scheduler", scheduler,
		"fullText", d.FullText != nil && *d.FullText, "vectors", d.Vectors != nil && *d.Vectors, "formatNote", formatNote, "readThrough", readThrough,
		"built", built, "indexDir", indexDir, "progress", progress, "builtDepth", optional(d.BuiltDepth), "gaps", gaps,
		"buildingDepth", buildingDepth)
	if job != 0 {
		o.Set("job", float64(job))
	}
	return o
}

// The read-through view's port and options are undefined when absent
// (Node spreads them in only when set): left out of the object.
func intPtr(p *int) any {
	if p == nil {
		return nil
	}
	return float64(*p)
}

func int64Ptr(p *int64) any {
	if p == nil {
		return nil
	}
	return float64(*p)
}

// ---- adding, refreshing and removing ----

var (
	siteRe     = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,120}$`)
	safeDirRe  = regexp.MustCompile(`^(?:~|\$HOME|\$WORKDIR)?(?:/[A-Za-z0-9_.@+-]+)*/?$`)
	resourceRe = regexp.MustCompile(`^pw://[A-Za-z0-9_.@-]+/[A-Za-z0-9_.-]+$`)
	notWordRe  = regexp.MustCompile(`[^\w.-]`)
	slurmTime  = regexp.MustCompile(`^\d{1,3}:\d{2}:\d{2}$`)
)

func label(given any, fallback string) string {
	l := str(given)
	if l == "" {
		l = fallback
	}
	return slice(l, 60)
}

func (m *Module) addFromCluster(ctx context.Context, w who, b *jsonv.Object) (*libraries.UserDef, error) {
	owner := w.owner()
	if owner == "" {
		return nil, apierr.New(401, "sign in to add your own libraries")
	}
	key := m.keyOf(w, owner)
	if key == "" {
		return nil, apierr.New(409, "Libraries on your clusters are built with your own platform key: add your ACTIVATE API key under Settings, Model access.")
	}
	site := str(field(b, "site"))
	if !siteRe.MatchString(site) {
		return nil, apierr.New(400, "choose one of your clusters")
	}
	sourceDir := "$WORKDIR"
	if v := field(b, "sourceDir"); v != nil {
		sourceDir = str(v)
	}
	if sourceDir == "" {
		sourceDir = "$WORKDIR"
	}
	if !safeDirRe.MatchString(sourceDir) {
		return nil, apierr.New(400, "directory: an absolute path, or one under ~, $HOME, or $WORKDIR, in plain path characters")
	}
	var resource string
	if rv := field(b, "resource"); jsonv.Truthy(rv) && resourceRe.MatchString(jsonv.String(rv)) {
		resource = jsonv.String(rv)
	} else {
		var err error
		if resource, err = m.resourceOf(ctx, site, key); err != nil {
			return nil, err
		}
	}
	defs := m.load(owner)
	id := uniqueID(defs, idBase(site+"-"+sourceDir))
	var scheduler *libraries.Scheduler
	if s := jsonv.Obj(field(b, "scheduler")); s != nil && jsonv.Truthy(get(s, "partition")) {
		clean := func(v any) string { return slice(notWordRe.ReplaceAllString(jsonv.String(v), ""), 64) }
		scheduler = &libraries.Scheduler{Partition: clean(get(s, "partition"))}
		if v := get(s, "account"); jsonv.Truthy(v) {
			scheduler.Account = clean(v)
		}
		if v := get(s, "qos"); jsonv.Truthy(v) {
			scheduler.QOS = clean(v)
		}
		if v := get(s, "time"); jsonv.Truthy(v) && slurmTime.MatchString(jsonv.String(v)) {
			scheduler.Time = jsonv.String(v)
		}
	}
	d := libraries.UserDef{ID: id, Label: label(field(b, "label"), site+" "+sourceDir), Site: site, Resource: resource, SourceDir: sourceDir,
		State: "building", Scheduler: scheduler}
	slug, err := m.launch(ctx, &d, key, "build")
	if err != nil {
		return nil, err
	}
	d.Run = &libraries.WorkflowRun{Slug: slug, Mode: "build", StartedAt: isoNow()}
	if err := m.appendDef(owner, d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (m *Module) refresh(ctx context.Context, w who, id string) (*libraries.UserDef, error) {
	owner := w.owner()
	var d *libraries.UserDef
	if owner != "" {
		d = find(m.load(owner), id)
	}
	if d == nil {
		return nil, apierr.New(404, "no such library")
	}
	if d.Build != nil {
		return m.rebuildRemote(ctx, w, owner, d)
	}
	if d.IsSSH() || (d.Existing != nil && *d.Existing) {
		return m.recheckExisting(ctx, w, owner, d)
	}
	if d.State == "building" || d.State == "updating" {
		return d, nil
	}
	key := m.keyOf(w, owner)
	if key == "" {
		return nil, apierr.New(409, "add your ACTIVATE API key under Settings, Model access")
	}
	mode, state := "build", "building"
	if deref(d.IndexRoot) != "" {
		mode, state = "update", "updating"
	}
	slug, err := m.launch(ctx, d, key, mode)
	if err != nil {
		return nil, err
	}
	return m.update(owner, id, func(x *libraries.UserDef) {
		x.State, x.Error = state, nil
		x.Run = &libraries.WorkflowRun{Slug: slug, Mode: mode, StartedAt: isoNow()}
	}), nil
}

func notPassed(prefix string, p *probe) error {
	detail := ""
	if f := p.firstFailed(); f != nil {
		detail = f.Detail
	}
	return apierr.New(400, "%s", jsTrim(prefix+" "+detail))
}

// fromProbe is what a passing test gives a new library connected to an
// existing index.
func fromProbe(d *libraries.UserDef, p *probe) {
	d.OS = nonEmpty(p.OS)
	d.GufiVersion = p.GufiVersion
	d.SourceDir = deref(p.SourceRoot)
	d.SourceRoot = nonEmpty(p.SourceRoot)
	d.IndexRoot = p.IndexRoot
	bin := deref(p.GufiBin)
	d.GufiBin = &bin
	if p.Shell != nil && !*p.Shell {
		d.Shell = ptr(false)
	}
	if p.FullText != nil && *p.FullText {
		d.FullText = ptr(true)
	}
	d.Vectors = p.Vectors
	d.IndexFormat = p.IndexFormat
	d.State = "ready"
	if p.BuiltAt != nil {
		d.BuiltAt = p.BuiltAt
	} else {
		d.BuiltAt = ptr(isoNow())
	}
	d.Directories, d.Files = toInt64(p.Directories), toInt64(p.Files)
}

func nonEmpty(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}

func toInt64(f *float64) *int64 {
	if f == nil {
		return nil
	}
	n := int64(*f)
	return &n
}

func basename(p string) string {
	p = strings.TrimRight(p, "/")
	return p[strings.LastIndex(p, "/")+1:]
}

func (m *Module) addFromPw(ctx context.Context, w who, b *jsonv.Object) (*libraries.UserDef, error) {
	owner, key, err := m.pwKey(w)
	if err != nil {
		return nil, err
	}
	spec, err := pwSpecOf(b)
	if err != nil {
		return nil, err
	}
	p := m.probePw(ctx, spec, key, true)
	if !p.OK {
		return nil, notPassed("The connection test did not pass.", p)
	}
	indexRoot := deref(p.IndexRoot)
	defs := m.load(owner)
	for _, d := range defs {
		if !d.IsSSH() && d.Site == spec.Cluster && deref(d.IndexRoot) == indexRoot && d.IndexRoot != nil {
			return nil, apierr.New(409, "This index is already a library: %s.", d.Label)
		}
	}
	name := basename(indexRoot)
	if name == "" {
		name = "index"
	}
	d := libraries.UserDef{ID: uniqueID(defs, idBase(spec.Cluster+"-"+name)), Label: label(field(b, "label"), name), Transport: "pw",
		Existing: ptr(true), Site: spec.Cluster, Resource: spec.Cluster}
	fromProbe(&d, p)
	if err := m.appendDef(owner, d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (m *Module) addFromSSH(ctx context.Context, w who, b *jsonv.Object) (*libraries.UserDef, error) {
	if !w.sshAllowed() {
		return nil, errSSHSingle()
	}
	owner := w.owner()
	spec, err := sshSpecOf(b)
	if err != nil {
		return nil, err
	}
	p, err := m.probeSSH(ctx, spec, true)
	if err != nil {
		return nil, err
	}
	if !p.OK {
		return nil, notPassed("The connection test did not pass.", p)
	}
	indexRoot := deref(p.IndexRoot)
	defs := m.load(owner)
	for _, d := range defs {
		if d.IsSSH() && deref(d.Destination) == spec.Destination && d.Destination != nil && intOf(d.Port) == spec.Port && deref(d.IndexRoot) == indexRoot && d.IndexRoot != nil {
			return nil, apierr.New(409, "This index is already a library: %s.", d.Label)
		}
	}
	host := spec.Destination[strings.LastIndex(spec.Destination, "@")+1:]
	name := basename(indexRoot)
	if name == "" {
		name = "index"
	}
	site, _ := remote.SSHSite(spec.Destination, spec.Port, spec.SSHOptions)
	d := libraries.UserDef{ID: uniqueID(defs, idBase(host+"-"+name)), Label: label(field(b, "label"), name), Transport: "ssh",
		Site: spec.Destination, Destination: ptr(spec.Destination), Resource: site}
	if spec.Port != 0 {
		d.Port = ptr(spec.Port)
	}
	if len(spec.SSHOptions) > 0 {
		d.SSHOptions = spec.SSHOptions
	}
	fromProbe(&d, p)
	if err := m.appendDef(owner, d); err != nil {
		return nil, err
	}
	return &d, nil
}

// recheckExisting is Refresh for an index that was there already: the
// connection test runs again and the counts and index time are brought up
// to date.
func (m *Module) recheckExisting(ctx context.Context, w who, owner string, d *libraries.UserDef) (*libraries.UserDef, error) {
	var p *probe
	if d.IsSSH() {
		if !w.sshAllowed() {
			return nil, errSSHSingle()
		}
		opts := []any{}
		for _, o := range d.SSHOptions {
			opts = append(opts, o)
		}
		spec, err := newSSHSpec(optional(d.Destination), intPtr(d.Port), opts, optional(d.IndexRoot), optional(d.GufiBin), optional(d.SourceRoot))
		if err != nil {
			return nil, err
		}
		if p, err = m.probeSSH(ctx, spec, false); err != nil {
			return nil, err
		}
	} else {
		spec, err := newPwSpec(d.Site, optional(d.IndexRoot), optional(d.GufiBin), optional(d.SourceRoot))
		if err != nil {
			return nil, err
		}
		_, key, err := m.pwKey(w)
		if err != nil {
			return nil, err
		}
		p = m.probePw(ctx, spec, key, false)
	}
	failed := p.firstFailed()
	next := m.update(owner, d.ID, func(x *libraries.UserDef) {
		if failed != nil {
			x.Error = ptr(failed.Detail)
			return
		}
		x.Error = nil
		if p.OS != nil && *p.OS != "" {
			x.OS = p.OS
		} else if x.OS != nil && *x.OS == "" {
			x.OS = nil
		}
		if p.GufiVersion != nil {
			x.GufiVersion = p.GufiVersion
		}
		if p.Directories != nil {
			x.Directories = toInt64(p.Directories)
		}
		if p.Files != nil {
			x.Files = toInt64(p.Files)
		}
		if p.BuiltAt != nil {
			x.BuiltAt = p.BuiltAt
		}
		// Whether the login runs a shell can change either way between tests.
		x.Shell = nil
		if p.Shell != nil && !*p.Shell {
			x.Shell = ptr(false)
		}
		if p.FullText != nil {
			x.FullText = p.FullText
		}
		if p.Vectors != nil {
			x.Vectors = p.Vectors
		}
		if p.IndexFormat != nil {
			x.IndexFormat = p.IndexFormat
		}
	})
	if next == nil {
		return nil, apierr.New(404, "no such library")
	}
	// The index there was made again since the last test.
	if failed == nil && p.BuiltAt != nil && *p.BuiltAt != deref(d.BuiltAt) {
		m.tellBuilt(owner, d.ID)
	}
	return next, nil
}

func (m *Module) remove(w who, id string) error {
	owner := w.owner()
	if owner == "" {
		return apierr.New(404, "no such library")
	}
	err := m.Users.Modify(owner, func(defs []libraries.UserDef) ([]libraries.UserDef, error) {
		if find(defs, id) == nil {
			return nil, apierr.New(404, "no such library")
		}
		out := []libraries.UserDef{}
		for _, d := range defs {
			if d.ID != id {
				out = append(out, d)
			}
		}
		return out, nil
	})
	if err != nil {
		return err
	}
	m.Settings.Forget(id)
	root := libraries.RemoteRootOf(owner, id)
	if home, err := m.Users.RemoteMapHome(root); err == nil {
		os.RemoveAll(home)
	}
	m.Users.TellRemoved(root)
	m.Notify("removed", root)
	return nil
}

// setSSHOptions changes the ssh options of a library over ssh, after a
// connection test with the new ones passes.
func (m *Module) setSSHOptions(ctx context.Context, w who, id string, text any) (*libraries.UserDef, error) {
	if !w.sshAllowed() {
		return nil, errSSHSingle()
	}
	owner := w.owner()
	d := find(m.load(owner), id)
	if d == nil {
		return nil, apierr.New(404, "no such library")
	}
	if !d.IsSSH() {
		return nil, apierr.New(400, "ssh options apply to a library over ssh")
	}
	if d.Build != nil {
		return nil, apierr.New(400, "a library built over ssh keeps the options it was built with; remove it and build it again to change them")
	}
	spec, err := newSSHSpec(optional(d.Destination), intPtr(d.Port), text, optional(d.IndexRoot), optional(d.GufiBin), optional(d.SourceRoot))
	if err != nil {
		return nil, err
	}
	p, err := m.probeSSH(ctx, spec, false)
	if err != nil {
		return nil, err
	}
	if !p.OK {
		return nil, notPassed("The connection test with these options did not pass.", p)
	}
	site, _ := remote.SSHSite(spec.Destination, spec.Port, spec.SSHOptions)
	next := m.update(owner, id, func(x *libraries.UserDef) {
		x.SSHOptions = nil
		if len(spec.SSHOptions) > 0 {
			x.SSHOptions = spec.SSHOptions
		}
		x.Resource = site
		x.Shell = nil
		if p.Shell != nil && !*p.Shell {
			x.Shell = ptr(false)
		}
		x.Error = nil
	})
	if next == nil {
		return nil, apierr.New(404, "no such library")
	}
	return next, nil
}

// warm opens the shared ssh connection to each host the viewer's
// libraries are on, without waiting for it.
func (m *Module) warm(w who) int {
	if !w.sshAllowed() {
		return 0
	}
	sites := map[string]bool{}
	for _, d := range m.load(w.owner()) {
		if site := sshSiteOf(&d); site != "" {
			sites[site] = true
		}
	}
	for site := range sites {
		wait := m.Pool.Warm(site)
		go wait()
	}
	return len(sites)
}

// ---- helpers shared by the build ----

func sha8(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])[:8]
}

// posixNormalize is path.posix.normalize for an absolute path: a trailing
// slash kept.
func posixNormalize(p string) string {
	c := path.Clean(p)
	if strings.HasSuffix(p, "/") && c != "/" {
		c += "/"
	}
	return c
}

func (m *Module) workflowFile() string {
	return filepath.Join(m.ProjectRoot, "deploy", "gufi-index.yaml")
}
