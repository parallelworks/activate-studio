package libapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/embed"
	"github.com/parallelworks/activate-studio/server-go/internal/index"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
	"github.com/parallelworks/activate-studio/server-go/internal/search"
	"github.com/parallelworks/activate-studio/server-go/internal/wslpath"
)

// Libraries the Studio builds of a folder on this computer
// (libraryBuild.ts): the check before a build, the build as an index job,
// the sync that keeps the index in step with the folder on the sweep
// interval, a rebuild, and taking up a build a stop cut off. The passes
// are internal/index's (gufi_dir2index, extraction, the words writer and
// the embedder), under the library's own folder, index, caches and depth.

const (
	manifestName    = ".studio-index.json"
	singleUserBuild = "Libraries are built from folders on this computer only on a Studio with sign-in off."
)

type builds struct {
	s *Service

	mu       sync.Mutex
	running  map[string]int // library id -> index job id
	locks    map[string]*sync.Mutex
	denials  map[string][]string
	kicks    map[string]*time.Timer
	syncOn   bool
	stopping atomic.Bool
	ctx      context.Context
	cancel   context.CancelFunc
}

func newBuilds(s *Service) *builds {
	ctx, cancel := context.WithCancel(context.Background())
	b := &builds{s: s, running: map[string]int{}, locks: map[string]*sync.Mutex{}, denials: map[string][]string{}, kicks: map[string]*time.Timer{}, ctx: ctx, cancel: cancel}
	s.Reg.SetBuildProgressSource(func(id string) *libraries.BuildProgress {
		j := b.jobOf(id)
		if j == nil || j.Progress == nil {
			return nil
		}
		p := j.Progress
		return &libraries.BuildProgress{Phase: p.Phase, Done: int64(p.Done), Total: int64(p.Total), Current: p.Current}
	})
	return b
}

func (b *builds) ix() *index.Indexer { return index.Of(b.s.a) }

func (b *builds) lock(id string) func() {
	b.mu.Lock()
	m := b.locks[id]
	if m == nil {
		m = &sync.Mutex{}
		b.locks[id] = m
	}
	b.mu.Unlock()
	m.Lock()
	return m.Unlock
}

func (b *builds) jobOf(id string) *index.IndexJob {
	b.mu.Lock()
	jid, ok := b.running[id]
	b.mu.Unlock()
	if !ok {
		return nil
	}
	j, ok := b.ix().Job(jid)
	if !ok || (j.State != "queued" && j.State != "running") {
		return nil
	}
	return &j
}

func (b *builds) deny(id string, paths ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range paths {
		if !slices.Contains(b.denials[id], p) {
			b.denials[id] = append(b.denials[id], p)
		}
	}
}

// settleDenied records what this build or sync could not read, and starts
// the next count afresh.
func (b *builds) settleDenied(id string) {
	b.mu.Lock()
	d := b.denials[id]
	delete(b.denials, id)
	b.mu.Unlock()
	var folders []string
	for _, p := range d {
		if isDir(p) {
			folders = append(folders, p)
		}
	}
	b.s.Reg.RecordDenied(id, folders, len(d)-len(folders))
}

func (b *builds) log(msg string) {
	b.ix().Log.Write(msg)
	b.s.a.Log.Info(msg)
}

// ---- the check ----

type buildStep struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Warn   bool   `json:"warn,omitempty"`
}

type localCheck struct {
	OK         bool
	Steps      []buildStep
	Create     []string
	SourceRoot string
	IndexDir   string
	Managed    bool
	ID         string
}

func (s *Service) localBuildOffered(r *http.Request) bool { return !s.authEnabled(r) }

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// expand is a path as typed: ~ is the home folder, and a relative path is
// taken from it.
func expand(p string) string {
	// A Windows path typed under WSL (C:\Users\me) is its /mnt/c folder.
	if q, ok := wslpath.FromWindows(p); ok {
		p = q
	}
	home := homeDir()
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(home, p)
}

func within(child, parent string) bool {
	if child == parent {
		return true
	}
	if !strings.HasSuffix(parent, string(filepath.Separator)) {
		parent += string(filepath.Separator)
	}
	return strings.HasPrefix(child, parent)
}

func realOr(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		if a, err := filepath.Abs(r); err == nil {
			return a
		}
		return r
	}
	a, _ := filepath.Abs(p)
	return a
}

func existingAncestor(p string) string {
	a := p
	for !exists(a) && filepath.Dir(a) != a {
		a = filepath.Dir(a)
	}
	return a
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func isDir(p string) bool { st, err := os.Stat(p); return err == nil && st.IsDir() }

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 {
			return true
		}
	}
	return false
}

func (s *Service) gufiTools() bool {
	bin := s.a.Cfg.GufiBin
	return exists(filepath.Join(bin, "gufi_dir2index")) && exists(filepath.Join(bin, "gufi_query"))
}

func (s *Service) embedModel() string {
	return filepath.Join(s.a.Cfg.IndexBase, "models", "minilm384.gguf")
}

// meaningModel says whether search by meaning can be computed here: the
// GGUF model, or the ONNX one the Go embedder also reads.
func (s *Service) meaningModel() bool {
	return exists(s.embedModel()) || embed.ModelPresent(embed.ModelDir(s.a.Cfg.IndexBase))
}

func (s *Service) extractDetail(depth index.Depth) string {
	if depth == index.DepthNames {
		return "Names, sizes and dates only: nothing is read from inside the files."
	}
	meaning := strings.HasPrefix(string(depth), "meaning-") && s.meaningModel()
	images := " and images"
	if depth == index.DepthText {
		images = ""
	}
	tail := ""
	if meaning {
		tail = ", and by meaning"
	}
	return "Text is extracted from documents" + images + ", so search covers what the files say" + tail + "."
}

func (s *Service) checkLocalBuild(r *http.Request, body reqbody.Body) (localCheck, error) {
	if !s.localBuildOffered(r) {
		return localCheck{}, apierr.New(403, singleUserBuild)
	}
	typed := jsTrim(body.Str("sourceRoot"))
	if typed == "" {
		return localCheck{}, apierr.New(400, "source folder: the folder to index")
	}
	if hasControl(typed) {
		return localCheck{}, apierr.New(400, "source folder: no control characters")
	}
	label := jsTrim(body.Str("label"))
	id := strings.ToLower(jsTrim(body.Str("id")))
	if id == "" {
		id = s.Reg.IDFor(label, typed)
	}
	var steps []buildStep
	create := []string{}
	cfg := s.a.Cfg
	if s.gufiTools() {
		steps = append(steps, buildStep{Name: "gufi", OK: true, Detail: "GUFI is in " + cfg.GufiBin + "."})
	} else {
		steps = append(steps, buildStep{Name: "gufi", Detail: "GUFI is not installed here: there is no gufi_dir2index in " + cfg.GufiBin + "."})
	}

	source := expand(typed)
	src := realOr(source)
	base := realOr(cfg.IndexBase)
	fail := func(name, detail string) { steps = append(steps, buildStep{Name: name, Detail: detail}) }
	pass := func(name, detail string) { steps = append(steps, buildStep{Name: name, OK: true, Detail: detail}) }
	switch {
	case src == filepath.VolumeName(src)+string(filepath.Separator):
		fail("source", "Indexing the top of the disk is not supported; choose a folder.")
	case src == realOr(cfg.KBRoot):
		fail("source", source+" is the main folder, which is indexed already.")
	case within(src, base) || within(base, src):
		fail("source", source+" holds the Studio's own data ("+cfg.IndexBase+"), or is inside it; choose another folder.")
	case exists(source):
		switch {
		case !isDir(source):
			fail("source", source+" is a file, not a folder.")
		case readable(source):
			pass("source", source+" is readable.")
		default:
			fail("source", source+" is there, but the Studio cannot read it.")
		}
	default:
		at := existingAncestor(source)
		if isDir(at) && writable(at) {
			create = append(create, source)
			pass("source", source+" does not exist yet; it is created, empty, when the build starts.")
		} else {
			fail("source", source+" does not exist, and it cannot be made: "+at+" is not writable.")
		}
	}

	given := jsTrim(body.Str("indexRoot"))
	managed := given == ""
	indexDir := filepath.Join(s.Reg.BuiltLibraryDir(id), "index")
	if !managed {
		indexDir = expand(given)
	}
	idx := realOr(indexDir)
	switch {
	case !managed && hasControl(given):
		fail("index", "The index folder has control characters in its name.")
	case within(idx, src):
		fail("index", "The index folder "+indexDir+" is inside the folder being indexed. Choose an index folder outside it.")
	case within(src, idx):
		fail("index", source+" is inside the index folder "+indexDir+". Choose an index folder outside it.")
	case !managed && within(idx, filepath.Join(base, "gufi")):
		fail("index", indexDir+" is inside the main folder's index. Choose another folder.")
	case exists(indexDir):
		entries, _ := os.ReadDir(indexDir)
		owner := ""
		if raw, err := os.ReadFile(filepath.Join(indexDir, manifestName)); err == nil {
			var m map[string]any
			if json.Unmarshal(raw, &m) == nil {
				owner = reqbody.JSString(m["source"])
			}
		}
		switch {
		case !isDir(indexDir):
			fail("index", indexDir+" is a file, not a folder.")
		case !writable(indexDir):
			fail("index", indexDir+" is not writable.")
		case owner != "" && owner != src:
			fail("index", indexDir+" holds the index of "+owner+". Choose another index folder.")
		case owner != "":
			pass("index", indexDir+" holds an index the Studio built of this folder; the new one replaces it when complete.")
		case len(entries) > 0:
			fail("index", indexDir+" already holds files the Studio did not write. Choose an empty or new folder for the index.")
		default:
			pass("index", "The index is written in "+indexDir+".")
		}
	default:
		at := existingAncestor(indexDir)
		switch {
		case !(isDir(at) && writable(at)):
			fail("index", indexDir+" does not exist, and it cannot be made: "+at+" is not writable.")
		case managed:
			pass("index", "The index is written in the Studio's own data folder.")
		default:
			create = append(create, indexDir)
			pass("index", indexDir+" does not exist yet; it is created when the build starts.")
		}
	}

	// A library not added yet is checked at the depth it would be given.
	depth := index.NewLibraryDepth
	if d := body.Get("depth"); index.IsDepth(d) {
		depth = index.Depth(d.(string))
	} else if s.Reg.AddedDef(id) != nil {
		depth = s.ix().Settings.Get(id).Depth
	}
	pass("extract", s.extractDetail(depth))
	ok := true
	for _, st := range steps {
		ok = ok && st.OK
	}
	return localCheck{OK: ok, Steps: steps, Create: create, SourceRoot: source, IndexDir: indexDir, Managed: managed, ID: id}, nil
}

func (s *Service) ix() *index.Indexer { return index.Of(s.a) }

// jsTrim is String.prototype.trim.
func jsTrim(v string) string {
	return strings.TrimFunc(v, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\v', '\f', '\r', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
			return true
		}
		return r >= 0x2000 && r <= 0x200a
	})
}

// ---- routes ----

func (s *Service) buildCheck(w http.ResponseWriter, r *http.Request) error {
	body, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	c, err := s.checkLocalBuild(r, body)
	if err != nil {
		return err
	}
	var dir *string
	if !c.Managed {
		dir = &c.IndexDir
	}
	apierr.JSON(w, 200, struct {
		OK         bool        `json:"ok"`
		Steps      []buildStep `json:"steps"`
		Create     []string    `json:"create"`
		SourceRoot string      `json:"sourceRoot"`
		IndexDir   *string     `json:"indexDir"`
	}{c.OK, c.Steps, c.Create, c.SourceRoot, dir})
	return nil
}

type libraryAndJob struct {
	Library libraries.SettingsLibrary `json:"library"`
	Job     index.IndexJob            `json:"job"`
}

func (s *Service) settingsLibrary(id string) (libraries.SettingsLibrary, error) {
	l, err := s.Reg.Get(id, s.kbLabel(), libraries.Deployment)
	if err != nil {
		return libraries.SettingsLibrary{}, err
	}
	return s.Reg.SettingsView(l), nil
}

func (s *Service) buildAdd(w http.ResponseWriter, r *http.Request) error {
	body, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	c, err := s.checkLocalBuild(r, body)
	if err != nil {
		return err
	}
	if !c.OK {
		detail := ""
		for _, st := range c.Steps {
			if !st.OK {
				detail = st.Detail
				break
			}
		}
		return apierr.New(400, "%s", jsTrim("The check did not pass. "+detail))
	}
	if len(c.Create) > 0 && body.Get("create") != true {
		return apierr.New(409, "These folders do not exist yet: %s. Confirm to create them.", strings.Join(c.Create, ", "))
	}
	if err := s.Reg.CheckNewID(c.ID); err != nil {
		return err
	}
	if body.Has("meaning") && !index.IsMeaningLevel(body.Get("meaning")) {
		return apierr.New(400, "meaning: off, documents or full")
	}
	if body.Has("depth") && !index.IsDepth(body.Get("depth")) {
		return apierr.New(400, "depth: names, text, ocr, meaning-documents or meaning-full")
	}
	for _, d := range c.Create {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	src := realOr(c.SourceRoot)
	label := jsTrim(body.Str("label"))
	if label == "" {
		label = filepath.Base(src)
	}
	if _, err := s.Reg.AddBuilt(libraries.Def{
		ID: c.ID, Label: label, IndexRoot: filepath.Join(c.IndexDir, filepath.Base(src)), SourceRoot: &src,
		Build: &libraries.LocalBuild{IndexDir: c.IndexDir, CacheRoot: filepath.Join(s.Reg.BuiltLibraryDir(c.ID), "extract"), Managed: c.Managed, State: "building"},
	}); err != nil {
		return err
	}
	// The depth is stored with the library, the default included, so a later
	// change of the default leaves it as it was built.
	set := map[string]any{"depth": string(index.NewLibraryDepth)}
	if body.Has("depth") {
		set = map[string]any{"depth": body.Get("depth")}
	} else if body.Has("meaning") {
		set = map[string]any{"meaning": body.Get("meaning")}
	}
	if _, err := s.ix().Settings.Save(c.ID, set); err != nil {
		return err
	}
	job, err := s.builds.start(c.ID)
	if err != nil {
		return err
	}
	lib, err := s.settingsLibrary(c.ID)
	if err != nil {
		return err
	}
	apierr.JSON(w, 200, libraryAndJob{lib, job})
	return nil
}

func (s *Service) rebuild(w http.ResponseWriter, r *http.Request) error {
	if !s.localBuildOffered(r) {
		return apierr.New(403, singleUserBuild)
	}
	id := r.PathValue("id")
	if d := s.Reg.AddedDef(id); d == nil || d.Build == nil {
		return apierr.New(404, "%s is not a library the Studio built", id)
	}
	job, err := s.builds.start(id)
	if err != nil {
		return err
	}
	lib, err := s.settingsLibrary(id)
	if err != nil {
		return err
	}
	apierr.JSON(w, 200, libraryAndJob{lib, job})
	return nil
}

// ---- building and keeping in step ----

// start starts a full build of a library as an index job; one already
// running is returned.
func (b *builds) start(id string) (index.IndexJob, error) {
	if j := b.jobOf(id); j != nil {
		return *j, nil
	}
	job, err := b.ix().StartOwnJob("library:"+id, func(own *index.OwnJob) (int64, error) {
		unlock := b.lock(id)
		defer unlock()
		return b.fullBuild(id, own)
	})
	if err != nil {
		return index.IndexJob{}, err
	}
	b.mu.Lock()
	b.running[id] = job.ID
	b.mu.Unlock()
	return job, nil
}

func (b *builds) paths(d *libraries.Def) index.LibraryPaths {
	own := b.s.Reg.BuiltLibraryDir(d.ID)
	return index.LibraryPaths{
		Source: *d.SourceRoot, Index: d.IndexRoot, IndexDir: d.Build.IndexDir, Cache: d.Build.CacheRoot,
		EmbedCache: filepath.Join(own, "embed-cache.db"), SkipFile: filepath.Join(own, "skip-dirs.txt"),
	}
}

func (b *builds) work(d *libraries.Def, jobID int, s index.Settings) *index.LibraryWork {
	p := b.paths(d)
	// Written for every run, as Node's skipFile does.
	os.MkdirAll(filepath.Dir(p.SkipFile), 0o755)
	os.WriteFile(p.SkipFile, []byte(strings.Join(b.ix().SkipDirs(), "\n")+"\n"), 0o644)
	id := d.ID
	return b.ix().LibraryWork(b.ctx, p, index.LibraryWorkOptions{
		JobID: jobID, Settings: s, Threads: b.ix().Settings.Threads("kb"),
		Denied:   func(paths []string) { b.deny(id, paths...) },
		Embedded: func(time.Duration) {},
	})
}

func (b *builds) scan(id, root string) index.Scan {
	sc := index.ScanLatest(root, index.ExcludeDirs, b.ix().SkipDirs())
	for _, rel := range sc.Denied {
		p := root
		if rel != "" {
			p = filepath.Join(root, rel)
		}
		b.deny(id, p)
	}
	return sc
}

// names is gufi_dir2index of src into stage, made whole: a folder the scan
// found readable that the run left without a db.db (gufi_dir2index, meeting
// a folder it may not open, sometimes stops before the readable folders
// beside it) is indexed again on its own and put in place.
func (b *builds) names(w *index.LibraryWork, src, stage string, sc index.Scan) (string, error) {
	built, err := w.Names(src, stage, false)
	if err != nil {
		return "", err
	}
	missing := index.MissingIndexDirs(built, sc)
	sort.Strings(missing)
	var tops []string
	for _, rel := range missing {
		if rel == "" {
			continue
		}
		covered := false
		for _, t := range tops {
			if strings.HasPrefix(rel, t+"/") {
				covered = true
			}
		}
		if !covered {
			tops = append(tops, rel)
		}
	}
	for i, rel := range tops {
		fill := filepath.Join(filepath.Dir(stage), fmt.Sprintf(".fill-%d-%d", time.Now().UnixMilli(), i))
		got, err := w.Names(filepath.Join(src, rel), fill, false)
		if err == nil {
			dest := filepath.Join(built, rel)
			index.RmTree(dest)
			os.MkdirAll(filepath.Dir(dest), 0o755)
			err = os.Rename(got, dest)
		}
		index.RmTree(fill)
		if err != nil {
			return "", err
		}
		b.log(fmt.Sprintf("library index: %s was left out by gufi_dir2index; indexed it again", filepath.Join(src, rel)))
	}
	return built, nil
}

// subScan is the part of a scan at or below rel, with paths made relative to it.
func subScan(sc index.Scan, rel string) index.Scan {
	out := index.Scan{Latest: map[string]int64{}}
	cut := func(p string) (string, bool) {
		switch {
		case p == rel:
			return "", true
		case strings.HasPrefix(p, rel+"/"):
			return p[len(rel)+1:], true
		}
		return "", false
	}
	for _, p := range sc.Order {
		if q, ok := cut(p); ok {
			out.Order = append(out.Order, q)
			out.Latest[q] = sc.Latest[p]
		}
	}
	for _, p := range sc.Denied {
		if q, ok := cut(p); ok {
			out.Denied = append(out.Denied, q)
		}
	}
	return out
}

type progressFn func(phase string, done int, current *string)

// enrich reads the text and computes meaning over index, a tree of the
// whole library (or of rel, or of its top folder's own files).
func (b *builds) enrich(w *index.LibraryWork, d *libraries.Def, indexTree, rel string, noRecurse, live bool, progress progressFn, s index.Settings) error {
	if s.Depth == index.DepthNames {
		return nil
	}
	progress("Extracting text", 0, nil)
	if err := w.Documents(rel, noRecurse); err != nil {
		return err
	}
	kbRoot, cache := *d.SourceRoot, d.Build.CacheRoot
	if rel != "" {
		kbRoot, cache = filepath.Join(kbRoot, rel), filepath.Join(cache, rel)
	}
	if err := w.Text(kbRoot, indexTree, cache, noRecurse); err != nil {
		return err
	}
	if index.MeaningArgs(s.Meaning, s.MeaningSpeed) != nil && b.s.meaningModel() {
		// Search by meaning comes last. Its words are found now, so search
		// reads them while it runs and says meaning is still being computed.
		if live {
			b.s.Reg.StaleProbe(d.IndexRoot)
			b.s.Reg.Probe(b.ctx, d.IndexRoot)
		}
		progress("Computing search by meaning", 0, nil)
		if err := w.Meaning(indexTree, noRecurse, rel == "" && !noRecurse); err != nil {
			return err
		}
	}
	return nil
}

type counts struct {
	files, dirs int64
	builtAt     string
}

// settle is the counts, the manifest, and the caches that remember the
// old tree.
func (b *builds) settle(d *libraries.Def) (counts, error) {
	// Probed again at once. Until it answers, the library keeps the
	// capabilities last found, never "none".
	b.s.Reg.StaleProbe(d.IndexRoot)
	b.s.Reg.Probe(b.ctx, d.IndexRoot)
	search.InvalidateDbList()
	search.InvalidateCounts()
	var c counts
	if st, err := b.s.Engine.TreeStats(b.ctx, d.IndexRoot, ""); err == nil {
		c.files, c.dirs = int64(st.Files), int64(st.Dirs)
	}
	c.builtAt = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	m := struct {
		Version     int    `json:"version"`
		Source      string `json:"source"`
		Index       string `json:"index"`
		IndexDir    string `json:"indexDir"`
		BuiltAt     string `json:"builtAt"`
		GufiBin     string `json:"gufiBin"`
		Directories int64  `json:"directories"`
		Files       int64  `json:"files"`
	}{1, *d.SourceRoot, d.IndexRoot, d.Build.IndexDir, c.builtAt, b.s.a.Cfg.GufiBin, c.dirs, c.files}
	raw, err := marshalIndent(m, "  ")
	if err != nil {
		return c, err
	}
	return c, os.WriteFile(filepath.Join(d.Build.IndexDir, manifestName), raw, 0o644)
}

func marshalIndent(v any, indent string) ([]byte, error) {
	var buf bytes.Buffer
	e := json.NewEncoder(&buf)
	e.SetEscapeHTML(false)
	e.SetIndent("", indent)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func (b *builds) stateFile(id string) string {
	return filepath.Join(b.s.Reg.BuiltLibraryDir(id), "sync-state.json")
}
func (b *builds) depthFile(id string) string {
	return filepath.Join(b.s.Reg.BuiltLibraryDir(id), "depth-state.json")
}

// syncState is a sync's record: each folder's latest change, in the order
// the scan walked them (JSON.stringify of Object.fromEntries).
type syncState struct {
	order  []string
	latest map[string]int64
}

func (st syncState) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range jsKeyOrder(st.order) {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		fmt.Fprintf(&buf, ":%d", st.latest[k])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// jsKeyOrder is the order Object.fromEntries keeps: integer-like keys
// first, ascending, then the rest as inserted.
func jsKeyOrder(keys []string) []string {
	var ints, rest []string
	for _, k := range keys {
		if isArrayIndex(k) {
			ints = append(ints, k)
		} else {
			rest = append(rest, k)
		}
	}
	sort.SliceStable(ints, func(i, j int) bool {
		if len(ints[i]) != len(ints[j]) {
			return len(ints[i]) < len(ints[j])
		}
		return ints[i] < ints[j]
	})
	return append(ints, rest...)
}

func isArrayIndex(k string) bool {
	if k == "" || len(k) > 10 || (len(k) > 1 && k[0] == '0') {
		return false
	}
	var n uint64
	for _, c := range k {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + uint64(c-'0')
	}
	return n < 1<<32-1
}

func (b *builds) loadState(id string) *syncState {
	raw, err := os.ReadFile(b.stateFile(id))
	if err != nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil
	}
	st := &syncState{latest: map[string]int64{}}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil
		}
		k, _ := kt.(string)
		var v float64
		if err := dec.Decode(&v); err != nil {
			return nil
		}
		if _, had := st.latest[k]; !had {
			st.order = append(st.order, k)
		}
		st.latest[k] = int64(v)
	}
	return st
}

func (b *builds) saveState(id string, st syncState) error {
	f := b.stateFile(id)
	os.MkdirAll(filepath.Dir(f), 0o755)
	raw, _ := st.MarshalJSON()
	return os.WriteFile(f, raw, 0o644)
}

func stateOf(sc index.Scan) syncState {
	st := syncState{order: append([]string{}, sc.Order...), latest: map[string]int64{}}
	for k, v := range sc.Latest {
		st.latest[k] = v
	}
	return st
}

func (st *syncState) set(k string, v int64) {
	if _, had := st.latest[k]; !had {
		st.order = append(st.order, k)
	}
	st.latest[k] = v
}

func (st *syncState) del(k string) {
	if _, had := st.latest[k]; !had {
		return
	}
	delete(st.latest, k)
	st.order = slices.DeleteFunc(st.order, func(x string) bool { return x == k })
}

func (st syncState) clone() syncState {
	c := syncState{order: append([]string{}, st.order...), latest: map[string]int64{}}
	for k, v := range st.latest {
		c.latest[k] = v
	}
	return c
}

func strp(s string) *string { return &s }

// fullBuild is the whole folder, written beside the index there and
// swapped in when complete, so search keeps answering while it runs.
// Where the library has an index already (a rebuild, or a build a restart
// interrupted), the new one gets its text before the swap; a first build
// is swapped in at once, so its names answer while the text is read.
func (b *builds) fullBuild(id string, own *index.OwnJob) (int64, error) {
	t0 := time.Now()
	d := b.s.Reg.AddedDef(id)
	if d == nil || d.Build == nil || d.SourceRoot == nil {
		return 0, errors.New("no such library")
	}
	src, tree, bd := *d.SourceRoot, d.IndexRoot, d.Build
	var jobID int
	progress := func(phase string, done int, current *string) {
		own.Set(func(j *index.IndexJob) {
			jobID = j.ID
			j.Progress = &index.Progress{Phase: phase, Done: done, Current: current, Failed: []string{}}
		})
	}
	own.Set(func(j *index.IndexJob) { jobID = j.ID })
	// Read once, so the depth recorded is the one the build read at.
	s := b.ix().Settings.Built(id)
	// A stop between the two renames of an earlier swap left the old index aside.
	if index.RecoverSwap(tree, []string{filepath.Join(bd.IndexDir, ".old")}) != "" {
		b.log(fmt.Sprintf("library %s: its index was put aside by a swap a stop cut off; restored it", id))
	}
	state := "building"
	if exists(filepath.Join(tree, "db.db")) {
		state = "updating"
	}
	b.s.Reg.UpdateBuild(id, func(lb *libraries.LocalBuild) { lb.State, lb.Error = state, nil })
	err := func() error {
		if !isDir(src) {
			return fmt.Errorf("%s is not a folder any more", src)
		}
		// Taken first, so a change made during the build is still new to the sync.
		sc := b.scan(id, src)
		progress("Indexing folders", 0, nil)
		if err := os.MkdirAll(bd.IndexDir, 0o755); err != nil {
			return err
		}
		if err := os.MkdirAll(bd.CacheRoot, 0o755); err != nil {
			return err
		}
		stage := filepath.Join(bd.IndexDir, ".build")
		if err := index.RmTree(stage); err != nil {
			return err
		}
		w := b.work(d, jobID, s)
		stop := make(chan struct{})
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					n := index.CountDbs(stage)
					progress("Indexing folders", n, strp(enUS(n)+" folders"))
				}
			}
		}()
		built, err := b.names(w, src, stage, sc)
		close(stop)
		if err != nil {
			return err
		}
		if !exists(filepath.Join(built, "db.db")) {
			return fmt.Errorf("gufi_dir2index wrote no index at %s", built)
		}
		// The new index answers a query before it takes the old one's place.
		if err := w.VerifyTree(built); err != nil {
			return err
		}
		replacing := exists(filepath.Join(tree, "db.db"))
		if replacing {
			if err := b.enrich(w, d, built, "", false, false, progress, s); err != nil {
				return err
			}
		}
		old := filepath.Join(bd.IndexDir, ".old")
		if err := index.RmTree(old); err != nil {
			return err
		}
		// Moving a folder to another changes its '..', which takes leave to write it.
		if exists(tree) {
			index.OpenDir(tree)
			if err := os.Rename(tree, old); err != nil {
				return err
			}
		}
		if err := os.Rename(built, tree); err != nil {
			return err
		}
		index.RmTree(old)
		index.RmTree(stage)
		if !replacing {
			if err := b.enrich(w, d, tree, "", false, true, progress, s); err != nil {
				return err
			}
		}
		// Its words were written without the converter's provenance line.
		if err := index.MarkTextClean(tree); err != nil {
			return err
		}
		progress("Counting", 0, nil)
		c, err := b.settle(d)
		if err != nil {
			return err
		}
		b.settleDenied(id)
		if err := b.saveState(id, stateOf(sc)); err != nil {
			return err
		}
		if err := index.WriteDepthRecord(b.depthFile(id), index.DepthRecordOf(s)); err != nil {
			return err
		}
		b.s.Reg.UpdateBuild(id, func(lb *libraries.LocalBuild) {
			lb.State, lb.Error, lb.BuiltAt, lb.Files, lb.Dirs = "ready", nil, &c.builtAt, &c.files, &c.dirs
		})
		b.remapAfterMeaning(id, s, 5*time.Second)
		return nil
	}()
	if err != nil {
		// Cut off by a stop: left 'building' or 'updating' for the next start.
		if b.stopping.Load() {
			return 0, err
		}
		msg := truncRunes(libraries.PlainBuildError(err.Error()), 400)
		state := "failed"
		if exists(filepath.Join(tree, "db.db")) {
			state = "ready"
		}
		b.s.Reg.UpdateBuild(id, func(lb *libraries.LocalBuild) { lb.State, lb.Error = state, &msg })
		return 0, err
	}
	return time.Since(t0).Milliseconds(), nil
}

func truncRunes(s string, n int) string {
	u := []rune(s)
	if len(u) > n {
		return string(u[:n])
	}
	return s
}

// enUS is toLocaleString('en-US') for a whole number.
func enUS(n int) string {
	s := fmt.Sprint(n)
	var out strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 && s[0] != '-' {
			out.WriteByte(',')
		}
		out.WriteRune(r)
	}
	return out.String()
}

// remapAfterMeaning asks for a library's semantic map again once its
// embeddings changed, where its depth reaches search by meaning.
func (b *builds) remapAfterMeaning(id string, s index.Settings, delay time.Duration) {
	if s.Meaning != "off" {
		b.s.Reg.ScheduleMap(id, delay)
	}
}

// indexSubtree indexes one folder of a library again into a staging folder
// and swaps it in, with its text and meaning read before the swap.
func (b *builds) indexSubtree(w *index.LibraryWork, d *libraries.Def, rel string, sc index.Scan, s index.Settings) error {
	stage := filepath.Join(d.Build.IndexDir, fmt.Sprintf(".inc-%d-%d", time.Now().UnixMilli(), rand.IntN(1_000_000)))
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}
	defer index.RmTree(stage)
	built, err := b.names(w, filepath.Join(*d.SourceRoot, rel), stage, subScan(sc, rel))
	if err != nil {
		return err
	}
	if err := b.enrich(w, d, built, rel, false, false, func(string, int, *string) {}, s); err != nil {
		return err
	}
	target := filepath.Join(d.IndexRoot, rel)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := index.RmTree(target); err != nil {
		return err
	}
	return os.Rename(built, target)
}

// indexTop indexes the top folder's own files: its db.db alone.
func (b *builds) indexTop(w *index.LibraryWork, d *libraries.Def, s index.Settings) error {
	stage := filepath.Join(d.Build.IndexDir, fmt.Sprintf(".inc-top-%d", time.Now().UnixMilli()))
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}
	defer index.RmTree(stage)
	built, err := w.Names(*d.SourceRoot, stage, true)
	if err != nil {
		return err
	}
	if err := b.enrich(w, d, built, "", true, false, func(string, int, *string) {}, s); err != nil {
		return err
	}
	return os.Rename(filepath.Join(built, "db.db"), filepath.Join(d.IndexRoot, "db.db"))
}

// pendingDepthChange is the depth change the library's next sync applies:
// nil when there is nothing to run; a depth that only went down, or a
// library with no record yet, is recorded here.
func (b *builds) pendingDepthChange(id string, s index.Settings) *index.DepthChange {
	now := index.DepthRecordOf(s)
	was := index.ReadDepthRecord(b.depthFile(id))
	var ch *index.DepthChange
	if was != nil {
		ch = index.DepthChangeOf(*was, now)
	}
	if was == nil || (ch != nil && !ch.Text && !ch.Meaning) {
		index.WriteDepthRecord(b.depthFile(id), now)
	}
	if ch != nil && (ch.Text || ch.Meaning) {
		return ch
	}
	return nil
}

// applyDepthChange applies a change of depth over the whole library, on
// its index in place: only the steps the new depth adds.
func (b *builds) applyDepthChange(w *index.LibraryWork, d *libraries.Def, ch *index.DepthChange, s index.Settings) error {
	if ch.Text && s.Depth != index.DepthNames {
		if err := w.Documents("", false); err != nil {
			return err
		}
		if err := w.Text(*d.SourceRoot, d.IndexRoot, d.Build.CacheRoot, false); err != nil {
			return err
		}
	}
	if ch.Meaning && s.Depth != index.DepthNames && index.MeaningArgs(s.Meaning, s.MeaningSpeed) != nil && b.s.meaningModel() {
		if err := w.Meaning(d.IndexRoot, false, true); err != nil {
			return err
		}
	}
	return index.WriteDepthRecord(b.depthFile(d.ID), index.DepthRecordOf(s))
}

// sync brings a ready library's index up to its folder: folders whose
// content changed since the last pass are indexed again (the topmost of
// each changed branch), the top folder's own files when they changed, and
// the index of a folder that is gone is removed; then a change of depth
// since the last build or sync is applied over the whole library.
// Returns what changed.
func (b *builds) sync(id string) ([]string, error) {
	unlock := b.lock(id)
	defer unlock()
	d := b.s.Reg.AddedDef(id)
	if d == nil || d.Build == nil || d.Build.State != "ready" || d.SourceRoot == nil {
		return []string{}, nil
	}
	if !isDir(*d.SourceRoot) {
		msg := *d.SourceRoot + " is not a folder any more"
		b.s.Reg.UpdateBuild(id, func(lb *libraries.LocalBuild) { lb.Error = &msg })
		return []string{}, nil
	}
	s := b.ix().Settings.Built(id)
	depth := b.pendingDepthChange(id, s)
	// An index from before the provenance line was dropped: cleaned in place once.
	tidy := index.TextCleanNeeded(d.IndexRoot)
	sc := b.scan(id, *d.SourceRoot)
	index.OpenIndex(d.IndexRoot, sc.Locked)
	scan := stateOf(sc)
	saved := b.loadState(id)
	before := scan.clone()
	if saved != nil {
		before = *saved
	}
	// A folder the Studio may not read now is kept as it was last indexed:
	// not taken for removed, and not indexed again.
	for _, rel := range sc.Denied {
		for _, k := range before.order {
			if k == rel || rel == "" || strings.HasPrefix(k, rel+"/") {
				scan.set(k, before.latest[k])
			}
		}
	}
	if saved == nil {
		b.saveState(id, scan)
	}
	top := before.latest[""] < scan.latest[""]
	var changed []string
	for _, rel := range scan.order {
		if rel != "" && before.latest[rel] < scan.latest[rel] {
			changed = append(changed, rel)
		}
	}
	// Folders inside a read-only one that such a build left out: indexed now.
	for _, l := range sc.Locked {
		for _, dd := range l.Dirs {
			rel := dd
			if l.Rel != "" {
				rel = l.Rel + "/" + dd
			}
			if _, in := scan.latest[rel]; in && !exists(filepath.Join(d.IndexRoot, rel, "db.db")) {
				changed = append(changed, rel)
			}
		}
	}
	slices.Sort(changed)
	changed = slices.Compact(changed)
	changed = slices.DeleteFunc(slices.Clone(changed), func(rel string) bool {
		for _, o := range changed {
			if o != rel && strings.HasPrefix(rel, o+"/") {
				return true
			}
		}
		return false
	})
	var removed []string
	for _, rel := range before.order {
		if rel == "" {
			continue
		}
		if _, in := scan.latest[rel]; in {
			continue
		}
		inChanged := false
		for _, k := range scan.order {
			if strings.HasPrefix(rel, k+"/") && slices.Contains(changed, k) {
				inChanged = true
			}
		}
		if !inChanged {
			removed = append(removed, rel)
		}
	}
	if !top && len(changed) == 0 && len(removed) == 0 && depth == nil && !tidy {
		b.settleDenied(id)
		return []string{}, nil
	}
	done := []string{}
	var failed []string
	next := scan.clone()
	w := b.work(d, 0, s)
	for _, rel := range removed {
		index.RmTree(filepath.Join(d.IndexRoot, rel))
		done = append(done, rel+" (removed)")
	}
	for _, rel := range changed {
		if err := b.indexSubtree(w, d, rel, sc, s); err != nil {
			failed = append(failed, rel+": "+err.Error())
			for _, k := range slices.Clone(next.order) {
				if k == rel || strings.HasPrefix(k, rel+"/") {
					if v, had := before.latest[k]; had {
						next.set(k, v)
					} else {
						next.del(k)
					}
				}
			}
			continue
		}
		done = append(done, rel)
	}
	if top {
		if err := b.indexTop(w, d, s); err != nil {
			failed = append(failed, "(top folder): "+err.Error())
			if v, had := before.latest[""]; had {
				next.set("", v)
			} else {
				next.del("")
			}
		} else {
			done = append(done, "(top folder)")
		}
	}
	b.saveState(id, next)
	if depth != nil {
		// Shown as updating while it runs; a failure keeps the old record, so
		// the next sync tries again.
		b.s.Reg.UpdateBuild(id, func(lb *libraries.LocalBuild) { lb.State, lb.Pending, lb.HasPending = "updating", strp("depth"), true })
		if err := b.applyDepthChange(w, d, depth, s); err != nil {
			failed = append(failed, "index depth: "+err.Error())
		} else {
			done = append(done, "(index depth: "+string(s.Depth)+")")
		}
		// Cut off by a stop, it stays 'updating', and the next start finishes it with a sync.
		if !b.stopping.Load() {
			b.s.Reg.UpdateBuild(id, func(lb *libraries.LocalBuild) { lb.State, lb.Pending, lb.HasPending = "ready", nil, true })
		}
		if b.stopping.Load() {
			return done, nil
		}
	}
	if tidy {
		if _, rows, err := index.CleanIndexedText(d.IndexRoot, func() bool { return b.stopping.Load() }); err != nil {
			failed = append(failed, "tidying the indexed text: "+err.Error())
		} else {
			index.MarkTextClean(d.IndexRoot)
			if rows > 0 {
				done = append(done, "(indexed text tidied)")
			}
		}
	}
	c, _ := b.settle(d)
	b.settleDenied(id)
	var errMsg *string
	if len(failed) > 0 {
		e := truncRunes(strings.Join(failed, "; "), 400)
		errMsg = &e
	}
	b.s.Reg.UpdateBuild(id, func(lb *libraries.LocalBuild) {
		lb.Error, lb.BuiltAt, lb.Files, lb.Dirs = errMsg, &c.builtAt, &c.files, &c.dirs
	})
	// A new depth that adds meaning maps soon; a sync's changes, later.
	if len(done) > 0 {
		delay := 10 * time.Minute
		if depth != nil && depth.Meaning {
			delay = 5 * time.Second
		}
		b.remapAfterMeaning(id, s, delay)
	}
	return done, nil
}

func (b *builds) builtIDs() []string {
	var out []string
	for _, l := range b.s.Reg.List(b.s.kbLabel(), libraries.Deployment) {
		if l.Build != nil {
			out = append(out, l.ID)
		}
	}
	return out
}

func (b *builds) sweepSec() float64 { return float64(b.s.a.Settings.Effective().SweepIntervalSec) }

// requestSync asks for a library's sync soon, as a change of its depth
// does: after a short wait, behind a build that is running. Only where the
// background sync runs and is not paused.
func (b *builds) requestSync(id string, delay time.Duration) {
	if d := b.s.Reg.AddedDef(id); d == nil || d.Build == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.syncOn || b.sweepSec() <= 0 {
		return
	}
	if t := b.kicks[id]; t != nil {
		t.Stop()
	}
	b.kicks[id] = time.AfterFunc(delay, func() {
		b.mu.Lock()
		delete(b.kicks, id)
		b.mu.Unlock()
		if _, err := b.sync(id); err != nil {
			b.log(fmt.Sprintf("library %s: sync failed: %s", id, err))
		}
	})
}

func (b *builds) requestAllSyncs() {
	for _, id := range b.builtIDs() {
		b.requestSync(id, 2*time.Second)
	}
}

// startSync keeps built libraries in step with their folders on the sweep
// interval (0 pauses it), one library at a time, and finishes a build a
// restart interrupted.
func (b *builds) startSync(ctx context.Context) {
	b.mu.Lock()
	b.syncOn = true
	b.mu.Unlock()
	b.stopping.Store(false)
	for _, id := range b.builtIDs() {
		d := b.s.Reg.AddedDef(id)
		if d == nil || d.Build == nil {
			continue
		}
		// A swap a stop cut off between its renames: the old index is put back before anything else.
		if index.RecoverSwap(d.IndexRoot, []string{filepath.Join(d.Build.IndexDir, ".old")}) != "" {
			b.log(fmt.Sprintf("library %s: restored its index from a swap a stop cut off", id))
		}
		st := d.Build.State
		switch {
		case st == "updating" && d.Build.Pending != nil && *d.Build.Pending == "depth" && exists(filepath.Join(d.IndexRoot, "db.db")):
			// A change of depth a stop cut off: its index is whole, and the depth
			// record was not written, so a sync applies the change again.
			b.s.Reg.UpdateBuild(id, func(lb *libraries.LocalBuild) { lb.State, lb.Pending, lb.HasPending = "ready", nil, true })
			b.log(fmt.Sprintf("library %s: applying its index depth was interrupted; finishing it", id))
			go func() {
				if _, err := b.sync(id); err != nil {
					b.log(fmt.Sprintf("library %s: sync failed: %s", id, err))
				}
			}()
		case st == "building" || st == "updating":
			b.log(fmt.Sprintf("library %s: the build was interrupted; building it again from what was read so far", id))
			b.start(id)
		}
	}
	wait := func() time.Duration { return time.Duration(max(b.sweepSec(), 60) * float64(time.Second)) }
	t := time.NewTimer(wait())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if b.sweepSec() > 0 {
			for _, id := range b.builtIDs() {
				if ctx.Err() != nil {
					return
				}
				if b.jobOf(id) != nil {
					continue
				}
				changed, err := b.sync(id)
				if err != nil {
					b.log(fmt.Sprintf("library %s: sync failed: %s", id, err))
				} else if len(changed) > 0 {
					b.log(fmt.Sprintf("library %s: indexed again %s", id, strings.Join(changed, ", ")))
				}
			}
		}
		t.Reset(wait())
	}
}

// stop ends the programs of every library build as the server stops.
// Their state stays 'building' or 'updating', so the next start resumes
// them. Returns how many builds were running.
func (b *builds) stop() int {
	b.stopping.Store(true)
	b.mu.Lock()
	for _, t := range b.kicks {
		t.Stop()
	}
	b.kicks = map[string]*time.Timer{}
	b.mu.Unlock()
	n := 0
	for _, id := range b.builtIDs() {
		if b.jobOf(id) != nil {
			n++
		}
	}
	b.cancel()
	return n
}
