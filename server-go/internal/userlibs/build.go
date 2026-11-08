package userlibs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// Building an index on the remote system, over ssh or pw ssh: the check,
// the start, and following the build there as an index job.

type buildSpec struct {
	SourceRoot string
	IndexRoot  *string
	GufiBin    *string
}

type buildConn struct {
	Destination *string
	Port        int
	SSHOptions  []string
	Site        string
}

type buildReq struct {
	owner string
	host  probeHost
	conn  buildConn
	spec  buildSpec
}

// buildRequest is the host a build request names, as the probe reaches
// it, and its folders.
func (m *Module) buildRequest(w who, transport string, b *jsonv.Object) (*buildReq, error) {
	r := &buildReq{}
	if transport == "ssh" {
		if !w.sshAllowed() {
			return nil, errSSHSingle()
		}
		r.owner = w.owner()
		d, err := sshDest(field(b, "destination"), field(b, "port"), field(b, "sshOptions"))
		if err != nil {
			return nil, err
		}
		site, err := remote.SSHSite(d.Destination, d.Port, d.SSHOptions)
		if err != nil {
			return nil, err
		}
		via := ""
		if j := remote.JumpOf(d.SSHOptions); j != "" {
			via = " through " + j
		}
		r.host = probeHost{Site: site, Name: d.Destination, Reached: d.Destination + portText(d.Port) + via,
			Advice: func(msg string) string { return sshAdvice(msg, d.Destination, d.Port) }}
		r.conn = buildConn{Destination: ptr(d.Destination), Port: d.Port, SSHOptions: d.SSHOptions, Site: d.Destination}
	} else {
		owner, key, err := m.pwKey(w)
		if err != nil {
			return nil, err
		}
		r.owner = owner
		cluster := str(field(b, "cluster"))
		if !clusterRe.MatchString(cluster) {
			return nil, apierr.New(400, "system: the name of one of your running clusters")
		}
		r.host = probeHost{Site: cluster, Key: key, Name: cluster, Reached: cluster,
			Advice: func(msg string) string { return pwAdvice(msg, cluster) }}
		r.conn = buildConn{SSHOptions: []string{}, Site: cluster}
	}
	src, err := remotePath(field(b, "sourceRoot"), "source folder")
	if err != nil {
		return nil, err
	}
	if src == nil || *src == "" {
		return nil, apierr.New(400, "source folder: the folder to index on that system, absolute or under ~")
	}
	idx, err := remotePath(field(b, "indexRoot"), "index folder")
	if err != nil {
		return nil, err
	}
	bin, err := remotePath(field(b, "gufiBin"), "GUFI bin directory")
	if err != nil {
		return nil, err
	}
	r.spec = buildSpec{SourceRoot: *src, IndexRoot: idx, GufiBin: bin}
	return r, nil
}

func inside(child, parent string) bool {
	return child == parent || strings.HasPrefix(child, strings.TrimRight(parent, "/")+"/")
}

var notNameChar = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// defaultIndexDir is where the index goes when no folder is given: under
// the login's own ~/.activate-studio, named after the folder.
func defaultIndexDir(home, source string) string {
	name := basename(source)
	if name == "" {
		name = "root"
	}
	// One _ per UTF-16 code unit, as the regular expression without the u
	// flag replaces them.
	name = notNameChar.ReplaceAllStringFunc(name, func(c string) string { return strings.Repeat("_", units(c)) })
	name = slice(name, 60)
	s := sha256.Sum256([]byte(source))
	return strings.TrimRight(home, "/") + "/.activate-studio/indexes/" + name + "-" + hex.EncodeToString(s[:])[:8]
}

// probeBuild is the check before a build, step by step.
func (m *Module) probeBuild(ctx context.Context, h probeHost, spec buildSpec) *probe {
	dest := h.Name
	p := &probe{}
	run := m.runner(ctx, h)
	add := func(s step) { p.Steps = append(p.Steps, s) }
	done := func() *probe { p.OK = allOK(p.Steps); return p }
	c := m.connectStep(h, run, &p.Steps)
	if c == nil {
		return done()
	}
	g := m.gufiStep(h, run, &p.Steps, c.expand(spec.GufiBin), c.Home, c.Mac, true)
	if !g.Ran {
		p.OS = &c.OS
		return done()
	}
	source := *c.expand(&spec.SourceRoot)
	indexDir := ""
	if e := c.expand(spec.IndexRoot); e != nil {
		indexDir = *e
	} else {
		indexDir = defaultIndexDir(c.Home, source)
	}
	if !strings.HasPrefix(source, "/") || !strings.HasPrefix(indexDir, "/") {
		add(step{Name: "source", Detail: fmt.Sprintf("The home folder of the login on %s could not be read, so ~ cannot be resolved; give absolute paths.", dest)})
		p.OS = &c.OS
		return done()
	}
	kv := keyValues(run([]string{"sh", "-c", buildCheck, "_", source, indexDir}, 0).Stdout)
	create := []string{}
	src := kv["source_real"]
	if src == "" {
		src = posixNormalize(source)
	}
	idx := kv["index_real"]
	if idx == "" {
		idx = posixNormalize(indexDir)
	}
	or := func(v, d string) string {
		if v == "" {
			return d
		}
		return v
	}

	switch {
	case src == "/":
		add(step{Name: "source", Detail: "Indexing / is not supported; choose a folder."})
	case kv["source"] == "ok":
		add(step{Name: "source", OK: true, Detail: source + " is readable."})
	case kv["source"] == "create":
		create = append(create, source)
		add(step{Name: "source", OK: true, Detail: source + " does not exist yet; it is created, empty, when the build starts."})
	default:
		var detail string
		switch kv["source"] {
		case "unreadable":
			detail = fmt.Sprintf("%s is there, but the login on %s cannot read it.", source, dest)
		case "notdir":
			detail = source + " is a file, not a folder."
		case "nocreate":
			detail = fmt.Sprintf("%s does not exist, and it cannot be made: %s is not writable by the login.", source, or(kv["source_at"], "its parent"))
		default:
			detail = fmt.Sprintf("The source folder could not be checked on %s.", dest)
		}
		add(step{Name: "source", Detail: detail})
	}

	switch {
	case inside(idx, src):
		add(step{Name: "index", Detail: fmt.Sprintf("The index folder %s is inside the folder being indexed. Choose an index folder outside it.", indexDir)})
	case inside(src, idx):
		add(step{Name: "index", Detail: fmt.Sprintf("%s is inside the index folder %s. Choose an index folder outside it.", source, indexDir)})
	case kv["index"] == "empty":
		add(step{Name: "index", OK: true, Detail: fmt.Sprintf("The index is written in %s.", indexDir)})
	case kv["index"] == "studio":
		if other := kv["index_source"]; other != "" && other != src {
			add(step{Name: "index", Detail: fmt.Sprintf("%s holds the index of %s. Choose another index folder.", indexDir, other)})
		} else {
			add(step{Name: "index", OK: true, Detail: fmt.Sprintf("%s holds an index the Studio built; the new one replaces it when complete.", indexDir)})
		}
	case kv["index"] == "create":
		// A folder the user named is made only once they confirm; the
		// default is the Studio's own, under ~/.activate-studio.
		if spec.IndexRoot != nil {
			create = append(create, indexDir)
			add(step{Name: "index", OK: true, Detail: fmt.Sprintf("%s does not exist yet; it is created when the build starts.", indexDir)})
		} else {
			add(step{Name: "index", OK: true, Detail: fmt.Sprintf("The index is written in %s.", indexDir)})
		}
	default:
		var detail string
		switch kv["index"] {
		case "foreign":
			detail = fmt.Sprintf("%s already holds files the Studio did not write. Choose an empty or new folder for the index.", indexDir)
		case "readonly":
			detail = fmt.Sprintf("%s is not writable by the login on %s.", indexDir, dest)
		case "notdir":
			detail = indexDir + " is a file, not a folder."
		case "nocreate":
			detail = fmt.Sprintf("%s does not exist, and it cannot be made: %s is not writable by the login.", indexDir, or(kv["index_at"], "its parent"))
		default:
			detail = fmt.Sprintf("The index folder could not be checked on %s.", dest)
		}
		add(step{Name: "index", Detail: detail})
	}

	var python, model, platform *string
	if py := kv["python"]; strings.HasPrefix(py, "/") {
		python = &py
		if md := kv["model"]; strings.HasPrefix(md, "/") {
			model = &md
		}
		if pl := kv["platform"]; pl != "" {
			platform = &pl
		}
	}
	if python != nil {
		add(step{Name: "extract", OK: true, Detail: fmt.Sprintf("Text is extracted there with Python %s, so search covers what the files say. %s",
			or(kv["python_version"], "3"), m.meaningThere(platform, kv["embeds"], model))})
	} else {
		add(step{Name: "extract", OK: true, Warn: true, Detail: fmt.Sprintf("No Python 3.10 or later with SQLite's full-text module on %s, so the index has names and metadata only.", dest)})
	}
	p.OS = &c.OS
	p.GufiBin = &g.Bin
	if g.Version != "" {
		p.GufiVersion = &g.Version
	}
	p.SourceRoot, p.IndexDir, p.Create = &source, &indexDir, create
	p.Python, p.Model, p.Platform = python, model, platform
	return done()
}

func fieldsHave(list, v string) bool {
	for _, f := range strings.Fields(list) {
		if f == v {
			return true
		}
	}
	return false
}

// meaningThere is how search by meaning is made there, for the check.
func (m *Module) meaningThere(platform *string, embeds string, gguf *string) string {
	v := ""
	if platform != nil {
		v = m.embedVersion(*platform)
	}
	if v != "" && fieldsHave(embeds, v) {
		return "At a Meaning depth, embeddings are made with the Studio's model, which is there in ~/.activate-studio/embed."
	}
	if v != "" {
		return fmt.Sprintf("At a Meaning depth, embeddings are made with the Studio's model: the first build sends it with its packages for this Python (about %s MB, once) to ~/.activate-studio/embed.",
			jsonv.FormatNumber(m.embedMegabytes(*platform)))
	}
	pl := "this Python"
	if platform != nil {
		pl = *platform
	}
	why := "There are no packages of the Studio's embedding model for " + pl + " (Python 3.11 to 3.13 on Linux x86_64 or aarch64, or macOS arm64)"
	if gguf != nil {
		return why + ", so at a Meaning depth embeddings are made with the GGUF model in ~/.activate-studio/models, which search does not read."
	}
	return why + ", so no embeddings are made there."
}

func newBuildID(id string) string {
	return id + "-" + strconv.FormatInt(time.Now().UnixMilli(), 36)
}

var absAny = regexp.MustCompile(`^/[^\x00-\x1f\x7f]*$`)

// startRemote writes the build to the remote system and starts it there,
// at its depth; what the embedding needs and the system does not have is
// sent while the build runs.
func (m *Module) startRemote(ctx context.Context, site, key string, b *libraries.RemoteBuild, create bool) error {
	depth := b.Depth
	level := libraries.MeaningArgs(libraries.MeaningOfDepth(depth), m.Settings.MeaningSpeed("kb"))
	text := libraries.TextArgs(depth)
	py := deref(b.Python)
	read := func(name string) (string, error) { return m.indexerScript(name) }
	enrich := ""
	if py != "" && text != nil {
		var err error
		if enrich, err = read("enrich.py"); err != nil {
			return err
		}
	}
	// The Python's platform is read again at each build: the check of a
	// library built by an earlier version did not record it.
	platform, version, send, sendText := "", "", false, false
	readers := ""
	if enrich != "" {
		readers = m.readersVersion()
		r, err := m.Pool.Run(ctx, site, key, []string{"sh", "-c", embedProbe, "_", py}, 60*time.Second)
		out := ""
		if err == nil {
			out = r.Stdout
		}
		kv := keyValues(out)
		sendText = !fieldsHave(kv["readers"], readers)
		if level != nil && kv["platform"] != "" {
			if v := m.embedVersion(kv["platform"]); v != "" {
				platform, version = kv["platform"], v
				send = !fieldsHave(kv["embeds"], v)
			}
		}
	}
	embed := ""
	if enrich != "" && level != nil && (deref(b.Model) != "" || version != "") {
		var err error
		if embed, err = read("embed.py"); err != nil {
			return err
		}
	}
	onnx := embed != "" && version != ""
	pick := func(on bool, name string) (string, error) {
		if !on {
			return "", nil
		}
		return read(name)
	}
	onnxScript, err := pick(onnx, "embed_onnx.py")
	if err != nil {
		return err
	}
	fetch, err := pick(enrich != "", "fetch_embed.py")
	if err != nil {
		return err
	}
	deps, err := pick(enrich != "", "embed_deps.json")
	if err != nil {
		return err
	}
	when := func(on bool, v string) string {
		if on {
			return v
		}
		return ""
	}
	createArg := "0"
	if create {
		createArg = "1"
	}
	threads := strconv.Itoa(m.Pool.Threads(ctx, site, key))
	r, err := m.Pool.Run(ctx, site, key, []string{"bash", "-c", buildStart, "_", b.ID, buildRun, enrich, embed,
		onnxScript, fetch, deps, when(onnx && send, version), when(sendText, readers),
		b.GufiBin, b.SourceRoot, b.IndexDir, threads, py, deref(b.Model), createArg, strings.Join(level, " "),
		strings.Join(text, " "), when(onnx, version), when(onnx, platform), readers, depth}, 60*time.Second)
	if err != nil {
		return err
	}
	if r.Code != 0 || !strings.Contains(r.Stdout, "started") {
		return apierr.New(502, "The build could not be started on %s: %s", remote.SiteName(site), lastOrExit(r.Stderr, r.Code))
	}
	// The readers first: the text pass waits for them.
	if sendText {
		go func() {
			m.sendReaders(site, key, readers)
			if onnx && send {
				m.sendEmbed(site, key, platform, version)
			}
		}()
	} else if onnx && send {
		go m.sendEmbed(site, key, platform, version)
	}
	return nil
}

// sessionOf is the session a library's commands go over, and the key they
// act with.
func (m *Module) sessionOf(w who, owner string, d *libraries.UserDef) (site, key string, err error) {
	if d.IsSSH() {
		if owner != single || w.Auth {
			return "", "", errSSHSingle()
		}
		site, err = remote.SSHSite(deref(d.Destination), intOf(d.Port), d.SSHOptions)
		return site, "", err
	}
	key = m.keyOf(w, owner)
	if key == "" {
		return "", "", apierr.New(409, "This library is reached with your own platform key: add your ACTIVATE API key under Settings, Model access.")
	}
	return d.Site, key, nil
}

func preparing() *libraries.BuildProgress {
	return &libraries.BuildProgress{Phase: phaseLabel["prepare"]}
}

// addBuild starts an index on a remote system over the session, after the
// check passes here again; folders that do not exist are made only when
// the request says to.
func (m *Module) addBuild(ctx context.Context, w who, transport string, b *jsonv.Object) (*libraries.UserDef, int, error) {
	r, err := m.buildRequest(w, transport, b)
	if err != nil {
		return nil, 0, err
	}
	p := m.probeBuild(ctx, r.host, r.spec)
	if !p.OK {
		return nil, 0, notPassed("The check did not pass.", p)
	}
	if len(p.Create) > 0 && field(b, "create") != true {
		return nil, 0, apierr.New(409, "These folders do not exist yet: %s. Confirm to create them.", strings.Join(p.Create, ", "))
	}
	defs := m.load(r.owner)
	for _, d := range defs {
		if d.Build != nil && d.Transport == transport && d.Site == r.conn.Site && intOf(d.Port) == r.conn.Port && d.Build.IndexDir == *p.IndexDir {
			return nil, 0, apierr.New(409, "This index folder is already used by %s.", d.Label)
		}
	}
	name := basename(*p.SourceRoot)
	if name == "" {
		name = "folder"
	}
	host := r.conn.Site[strings.LastIndex(r.conn.Site, "@")+1:]
	id := uniqueID(defs, idBase(host+"-"+name))
	// The depth is stored with the library, the default included, so a
	// later default leaves it as built.
	settings := map[string]any{"depth": string(libraries.NewLibraryDepth)}
	if b.Has("depth") {
		settings = map[string]any{"depth": field(b, "depth")}
	} else if b.Has("meaning") {
		settings = map[string]any{"meaning": field(b, "meaning")}
	}
	if err := m.Settings.Save(id, settings); err != nil {
		return nil, 0, err
	}
	build := &libraries.RemoteBuild{ID: newBuildID(id), IndexDir: *p.IndexDir, SourceRoot: *p.SourceRoot, GufiBin: deref(p.GufiBin),
		Python: p.Python, Model: p.Model, StartedAt: isoNow(), Depth: m.Settings.Depth(id), Progress: preparing()}
	creating := false
	for _, c := range p.Create {
		if c == build.SourceRoot {
			creating = true
		}
	}
	if err := m.startRemote(ctx, r.host.Site, r.host.Key, build, creating); err != nil {
		return nil, 0, err
	}
	resource := r.conn.Site
	if transport == "ssh" {
		resource = r.host.Site
	}
	d := libraries.UserDef{ID: id, Label: label(field(b, "label"), name), Transport: transport, Existing: ptr(false), Site: r.conn.Site,
		Destination: r.conn.Destination, OS: nonEmpty(p.OS), GufiVersion: p.GufiVersion, Resource: resource, SourceDir: build.SourceRoot,
		State: "building", Build: build}
	if r.conn.Port != 0 {
		d.Port = ptr(r.conn.Port)
	}
	if len(r.conn.SSHOptions) > 0 {
		d.SSHOptions = r.conn.SSHOptions
	}
	if err := m.appendDef(r.owner, d); err != nil {
		return nil, 0, err
	}
	return &d, m.follow(w, r.owner, id), nil
}

// rebuildRemote builds a library's index again, beside the one there,
// which is searched until the new one replaces it.
func (m *Module) rebuildRemote(ctx context.Context, w who, owner string, d *libraries.UserDef) (*libraries.UserDef, error) {
	if d.State == "building" || d.State == "updating" {
		return d, nil
	}
	if d.IsSSH() && !w.sshAllowed() {
		return nil, errSSHSingle()
	}
	site, key, err := m.sessionOf(w, owner, d)
	if err != nil {
		return nil, err
	}
	build := *d.Build
	// What the system can run is read again: a Python installed since the
	// library was added is what a rebuild is often for.
	if check, err := m.Pool.Run(ctx, site, key, []string{"sh", "-c", buildCheck, "_", d.Build.SourceRoot, d.Build.IndexDir}, 60*time.Second); err == nil && check.Code == 0 {
		kv := keyValues(check.Stdout)
		build.Python, build.Model = nil, nil
		if py := kv["python"]; strings.HasPrefix(py, "/") {
			build.Python = &py
			if md := kv["model"]; strings.HasPrefix(md, "/") {
				build.Model = &md
			}
		}
	}
	build.ID, build.StartedAt, build.Depth, build.Progress = newBuildID(d.ID), isoNow(), m.Settings.Depth(d.ID), preparing()
	if err := m.startRemote(ctx, site, key, &build, false); err != nil {
		return nil, err
	}
	state := "building"
	if deref(d.IndexRoot) != "" {
		state = "updating"
	}
	next := m.update(owner, d.ID, func(x *libraries.UserDef) {
		x.State, x.Error, x.Build = state, nil, &build
	})
	if next == nil {
		return nil, apierr.New(404, "no such library")
	}
	m.follow(w, owner, d.ID)
	return next, nil
}

// madeDepth is whether a finished build has what its depth makes.
func madeDepth(depth string, m *jsonv.Object) bool {
	return (!libraries.DocumentsOfDepth(depth) || get(m, "fullText") == true) && (libraries.MeaningOfDepth(depth) == "off" || vectorsMade(m))
}

// finishBuild is what a finished build left: the library is ready on its
// new index, or says why not, keeping an index it already had.
func (m *Module) finishBuild(owner string, d *libraries.UserDef, st buildStatus) bool {
	b := *d.Build
	b.Progress = nil
	mf := st.Manifest
	index, isIndex := isStr(get(mf, "index"))
	if !(st.State == "finished" && st.Exit != nil && *st.Exit == 0 && mf != nil && isIndex && absAny.MatchString(index)) {
		var msg string
		switch {
		case st.State == "missing":
			msg = "the build's state folder is gone from " + remote.SiteName(d.Resource)
		case st.Error != nil && *st.Error != "":
			msg = *st.Error
		case st.Exit != nil && *st.Exit != 0:
			msg = "the build ended with status " + jsonv.FormatNumber(*st.Exit)
		default:
			msg = "the build wrote no index summary"
		}
		m.update(owner, d.ID, func(x *libraries.UserDef) {
			x.State = "failed"
			if deref(x.IndexRoot) != "" {
				x.State = "ready"
			}
			x.Error, x.Build = &msg, &b
		})
		return false
	}
	source := b.SourceRoot
	if s, ok := isStr(get(mf, "source")); ok && absAny.MatchString(s) {
		source = s
	}
	gufiBin := b.GufiBin
	if s, ok := isStr(get(mf, "gufiBin")); ok && absAny.MatchString(s) {
		gufiBin = s
	}
	builtAt := isoNow()
	if s, ok := isStr(get(mf, "builtAt")); ok {
		builtAt = s
	}
	dirs, files := int64(orZero(get(mf, "directories"))), int64(orZero(get(mf, "files")))
	m.update(owner, d.ID, func(x *libraries.UserDef) {
		x.State, x.Error, x.IndexRoot, x.SourceRoot, x.SourceDir, x.GufiBin = "ready", nil, &index, &source, source, &gufiBin
		x.BuiltAt, x.Directories, x.Files = &builtAt, &dirs, &files
		x.FullText, x.Vectors = ptr(get(mf, "fullText") == true), ptr(vectorsMade(mf))
		x.Gaps = buildGaps(mf)
		x.Build = &b
		if b.Depth != "" && madeDepth(b.Depth, mf) {
			x.BuiltDepth = ptr(b.Depth)
		}
	})
	m.tellBuilt(owner, d.ID)
	return true
}

// follow follows a library's build on its remote system as a background
// index job (GET /api/jobs/:id): it reads the build's state every few
// seconds, keeps the phase and folder count on the job and the library,
// and when it ends, makes the library ready or says why not. A build
// already followed keeps its job.
//
// The session's key is the one the request that started following had:
// Node reads the owner's key again at each look.
func (m *Module) follow(w who, owner, id string) int {
	k := owner + "|" + id
	m.mu.Lock()
	if f := m.followers[k]; f != nil && f.running {
		m.mu.Unlock()
		return f.id
	}
	f := &follower{running: true}
	m.followers[k] = f
	m.mu.Unlock()
	job := m.Jobs().Start(m.Background(), "index", 0, nil, func(ctx context.Context, h *kb.JobHandle) (any, error) {
		defer func() { m.mu.Lock(); f.running = false; m.mu.Unlock() }()
		return m.followLoop(ctx, w, owner, id, h)
	})
	idv, _ := job.Get("id")
	n, _ := idv.(int)
	m.mu.Lock()
	f.id = n
	m.mu.Unlock()
	return n
}

func (m *Module) followLoop(ctx context.Context, w who, owner, id string, h *kb.JobHandle) (any, error) {
	t0 := time.Now()
	misses := 0
	var savedAt time.Time
	savedPhase := ""
	result := func() any { return jsonv.NewObject("ms", float64(time.Since(t0).Milliseconds())) }
	sleep := func() bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(m.PollEvery):
			return true
		}
	}
	for {
		d := find(m.load(owner), id)
		if d == nil || d.Build == nil || (d.State != "building" && d.State != "updating") {
			return result(), nil
		}
		b := *d.Build
		st, err := func() (buildStatus, error) {
			site, key, err := m.sessionOf(w, owner, d)
			if err != nil {
				return buildStatus{}, err
			}
			r, err := m.Pool.Run(ctx, site, key, []string{"sh", "-c", buildStatusSh, "_", b.ID, b.IndexDir}, 60*time.Second)
			if err != nil {
				return buildStatus{}, err
			}
			return parseStatus(r.Stdout), nil
		}()
		if err != nil {
			// The build goes on there either way; the next listing follows it again.
			if misses++; misses >= 5 {
				msg := "The build could not be followed: " + err.Error()
				m.update(owner, id, func(x *libraries.UserDef) { x.Error = &msg })
				return nil, err
			}
			if !sleep() {
				return nil, ctx.Err()
			}
			continue
		}
		misses = 0
		if st.State == "running" {
			phase, ok := phaseLabel[st.Phase]
			if !ok {
				phase = "Starting"
			}
			pr := &libraries.BuildProgress{Phase: phase}
			current := ""
			if st.Dirs != nil {
				pr.Done = int64(*st.Dirs)
				current = localeNumber(*st.Dirs) + " folders"
				pr.Current = &current
			}
			h.Progress(phase, int(pr.Done), current)
			if st.Phase != savedPhase || time.Since(savedAt) > 10*time.Second {
				nb := b
				nb.Progress = pr
				m.update(owner, id, func(x *libraries.UserDef) { x.Error, x.Build = nil, &nb })
				savedAt, savedPhase = time.Now(), st.Phase
			}
			if !sleep() {
				return nil, ctx.Err()
			}
			continue
		}
		if !m.finishBuild(owner, d, st) {
			msg := "the build failed"
			if x := find(m.load(owner), id); x != nil && x.Error != nil {
				msg = *x.Error
			}
			return nil, errors.New(msg)
		}
		return result(), nil
	}
}
