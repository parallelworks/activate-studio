package libraries

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/gufitrace"
)

// PrimaryID names the main folder's library.
const PrimaryID = "kb"

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

const (
	probeTTL   = 300 * time.Second
	probeRetry = 30 * time.Second
	exactTTL   = 60 * time.Minute
)

// KnownCaps is what a tree was last found to support (library-caps.json).
type KnownCaps struct {
	Caps     Caps          `json:"caps"`
	ProbedAt string        `json:"probedAt"`
	Stale    bool          `json:"stale"`
	Error    string        `json:"error,omitempty"`
	Format   *FormatRecord `json:"format,omitempty"`
	ExactAt  string        `json:"exactAt,omitempty"`
}

// Options wire a Registry.
type Options struct {
	KBRoot      string
	IndexBase   string
	GufiIndex   string // the main folder's index
	GufiBin     string
	AuthEnabled bool
	Getenv      func(string) string
	// Runner runs the probes' GUFI commands on this host.
	Runner   *gufi.Runner
	Settings IndexSettings
	Users    *Users
	// SSHSite makes a site of an ssh library (remote.SSHSite).
	SSHSite SiteFn
	// ReachedLabel is user@host as ssh reaches a site, once known ("" until
	// then); Learn finds it in the background. Both may be nil.
	ReachedLabel func(site string) string
	Learn        func(site string)
	Log          *slog.Logger
}

// Registry is the library list and what each tree supports.
type Registry struct {
	o Options

	mu         sync.Mutex
	added      []Def
	addedStamp string
	known      map[string]KnownCaps
	knownKeys  []string
	knownStamp string
	probeCache map[string]probeEntry
	probing    map[string]*probeCall
	generation map[string]int
	counting   map[string]bool

	removedHooks []func(id string)
	progressOf   func(id string) *BuildProgress
}

type probeEntry struct {
	caps Caps
	at   time.Time
}

type probeCall struct {
	done chan struct{}
	caps Caps
	err  error
}

// New makes the registry.
func New(o Options) *Registry {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Settings == nil {
		o.Settings = NewFileIndexSettings(o.IndexBase)
	}
	if o.Users == nil {
		o.Users = &Users{IndexBase: o.IndexBase, AuthEnabled: o.AuthEnabled}
	}
	return &Registry{o: o, probeCache: map[string]probeEntry{}, probing: map[string]*probeCall{},
		generation: map[string]int{}, counting: map[string]bool{}, progressOf: func(string) *BuildProgress { return nil }}
}

// Users is the user library store.
func (r *Registry) Users() *Users { return r.o.Users }

// Settings is the index settings the registry reads.
func (r *Registry) Settings() IndexSettings { return r.o.Settings }

// BuiltLibraryDir is builtLibraryDir: where a built library keeps its files.
func (r *Registry) BuiltLibraryDir(id string) string {
	return filepath.Join(r.o.IndexBase, "libraries", id)
}

func (r *Registry) file() string     { return filepath.Join(r.o.IndexBase, "libraries.json") }
func (r *Registry) capsFile() string { return filepath.Join(r.o.IndexBase, "library-caps.json") }

func (r *Registry) gufiInstalled() bool {
	_, err := os.Stat(filepath.Join(r.o.GufiBin, "gufi_query"))
	return err == nil
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func abs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}

// ---- library-caps.json ----

func (r *Registry) knownLocked() map[string]KnownCaps {
	st := fileStamp(r.capsFile())
	if r.known != nil && st == r.knownStamp {
		return r.known
	}
	r.knownStamp = st
	r.known, r.knownKeys = map[string]KnownCaps{}, nil
	b, err := os.ReadFile(r.capsFile())
	if err != nil {
		return r.known
	}
	var raw orderedObject
	if json.Unmarshal(b, &raw) != nil {
		return r.known
	}
	for _, root := range raw.keys {
		var rec struct {
			Caps *struct {
				Index    *bool `json:"index"`
				FullText any   `json:"fullText"`
				Vectors  any   `json:"vectors"`
			} `json:"caps"`
			ProbedAt any           `json:"probedAt"`
			Stale    any           `json:"stale"`
			Error    any           `json:"error"`
			ExactAt  any           `json:"exactAt"`
			Format   *FormatRecord `json:"format"`
		}
		if json.Unmarshal(raw.vals[root], &rec) != nil || rec.Caps == nil || rec.Caps.Index == nil {
			continue
		}
		k := KnownCaps{Caps: Caps{Index: *rec.Caps.Index, FullText: jsTruthy(rec.Caps.FullText), Vectors: jsTruthy(rec.Caps.Vectors)},
			ProbedAt: jsString(rec.ProbedAt), Stale: jsTruthy(rec.Stale), Format: validFormat(rec.Format)}
		if jsTruthy(rec.Error) {
			k.Error = jsString(rec.Error)
		}
		if jsTruthy(rec.ExactAt) {
			k.ExactAt = jsString(rec.ExactAt)
		}
		if k.Format != nil && k.Format.RolledUp != nil {
			k.Format.RolledUp = ptr(*k.Format.RolledUp)
		}
		r.known[root] = k
		r.knownKeys = append(r.knownKeys, root)
	}
	return r.known
}

func jsTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	}
	return true
}

func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (r *Registry) setKnownLocked(root string, k KnownCaps) {
	r.knownLocked()
	if _, ok := r.known[root]; !ok {
		r.knownKeys = append(r.knownKeys, root)
	}
	r.known[root] = k
}

func (r *Registry) saveKnownLocked() {
	var b strings.Builder
	b.WriteString("{")
	for i, root := range r.knownKeys {
		k, ok := r.known[root]
		if !ok {
			continue
		}
		if i > 0 {
			b.WriteString(",")
		}
		kj, _ := marshalJS(root, "")
		v, _ := marshalJS(k, " ")
		b.WriteString("\n " + string(kj) + ": " + strings.ReplaceAll(string(v), "\n", "\n "))
	}
	if len(r.knownKeys) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("}")
	os.MkdirAll(filepath.Dir(r.capsFile()), 0o755)
	if err := writeAtomic(r.capsFile(), []byte(b.String())); err != nil {
		r.o.Log.Warn("library capabilities not saved: " + err.Error())
		return
	}
	r.knownStamp = fileStamp(r.capsFile())
}

// Known is knownCaps: what a tree was last found to support; nil if never probed.
func (r *Registry) Known(indexRoot string) *KnownCaps {
	r.mu.Lock()
	defer r.mu.Unlock()
	if k, ok := r.knownLocked()[abs(indexRoot)]; ok {
		return &k
	}
	return nil
}

// SetKnownForTests is setKnownCapsForTests.
func (r *Registry) SetKnownForTests(indexRoot string, caps Caps, exact bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	at := nowISO()
	k := KnownCaps{Caps: caps, ProbedAt: at}
	if exact {
		k.ExactAt = at
	}
	r.setKnownLocked(abs(indexRoot), k)
}

func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// Lacks is lacks: whether a library is known to hold no words or vectors.
func (r *Registry) Lacks(l Library, what string) bool {
	has := l.Caps.FullText
	if what == "vectors" {
		has = l.Caps.Vectors
	}
	if has {
		return false
	}
	if l.Remote() {
		return !(what == "vectors" && l.VectorsUnknown)
	}
	k := r.Known(l.IndexRoot)
	if k == nil || k.ExactAt == "" || k.Stale {
		return false
	}
	if what == "vectors" {
		return !k.Caps.Vectors
	}
	return !k.Caps.FullText
}

// capsOf is the capabilities a listing shows: a fresh probe, else the
// record, else fallback; a probe runs in the background where none is fresh.
func (r *Registry) capsOf(a string, fallback func() Caps) Caps {
	index := exists(filepath.Join(a, "db.db"))
	r.mu.Lock()
	fresh, hasFresh := r.probeCache[a]
	if hasFresh && time.Since(fresh.at) < probeTTL {
		r.mu.Unlock()
		c := fresh.caps
		c.Index = index
		return c
	}
	k, hasKnown := r.knownLocked()[a]
	r.mu.Unlock()
	if r.gufiInstalled() && index {
		go r.Probe(context.Background(), a)
	}
	var c Caps
	switch {
	case hasKnown:
		c = k.Caps
	case hasFresh:
		c = fresh.caps
	default:
		c = fallback()
	}
	c.Index = index
	return c
}

// ---- probing ----

func (r *Registry) sqlite(ctx context.Context, script string) (string, error) {
	t0 := time.Now()
	res, err := r.o.Runner.Run(ctx, gufi.Command{Tool: gufi.ToolSQLite3, Stdin: []byte(script), Timeout: 10 * time.Second})
	note(ctx, []string{filepath.Join(r.o.GufiBin, "gufi_sqlite3")}, gufitrace.Run{Start: t0, Engine: "gufi_sqlite3", Script: &script, Hide: true, Source: "libraries"}, res, err)
	if err != nil && len(res.Stdout) == 0 {
		return "", err
	}
	if err == nil && res.ExitCode != 0 && len(res.Stdout) == 0 {
		return "", fmt.Errorf("gufi_sqlite3 exited %d: %s", res.ExitCode, firstLineOf(res.Stderr))
	}
	return string(res.Stdout), nil
}

func firstLineOf(s string) string { return strings.SplitN(strings.TrimSpace(s), "\n", 2)[0] }

// note records a finished local run in the trace and the log.
func note(ctx context.Context, argv []string, o gufitrace.Run, res gufi.Result, err error) {
	o.Elapsed = res.Elapsed
	switch {
	case res.Signal != "":
		o.Signal = res.Signal
	case err != nil && res.ExitCode == 0 && len(res.Stdout) == 0:
		o.Error = firstLineOf(err.Error())
	default:
		code := res.ExitCode
		o.Code = &code
	}
	se := res.Stderr
	o.Stderr, o.Cut = &se, res.StderrCut
	gufitrace.Note(ctx, argv, o)
}

// sampleDbs is up to max db.db files under a tree, breadth first.
func sampleDbs(root string, max int) []string {
	var out []string
	queue := []string{root}
	visited := 0
	for len(queue) > 0 && len(out) < max && visited < 400 {
		dir := queue[0]
		queue = queue[1:]
		visited++
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.Name() == "db.db" {
				out = append(out, filepath.Join(dir, "db.db"))
				break
			}
		}
		for _, e := range entries {
			if e.IsDir() {
				queue = append(queue, filepath.Join(dir, e.Name()))
			}
		}
	}
	return out
}

func sqlQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }

func probeThreads() int { return min(max(runtime.NumCPU(), 1), 8) }

var twoNums = regexp.MustCompile(`(?m)^(\d+)\|(\d+)\s*$`)
var oneNum = regexp.MustCompile(`(?m)^(\d+)\s*$`)

func (r *Registry) aggregate(ctx context.Context, a string, args []string) string {
	if !exists(filepath.Join(r.o.GufiBin, "gufi_query")) {
		return ""
	}
	args = append(args, a)
	t0 := time.Now()
	res, err := r.o.Runner.Run(ctx, gufi.Command{Tool: gufi.ToolQuery, Args: args, Timeout: 10 * time.Minute})
	note(ctx, append([]string{filepath.Join(r.o.GufiBin, "gufi_query")}, args...), gufitrace.Run{Start: t0, Hide: true, Source: "libraries"}, res, err)
	return string(res.Stdout)
}

// countTables is countTables: directories with words and with vectors.
func (r *Registry) countTables(ctx context.Context, a string) (words, vectors int64, ok bool) {
	has := func(name string) string {
		return "(SELECT count(*) FROM tree.sqlite_master WHERE name = '" + name + "')"
	}
	out := r.aggregate(ctx, a, []string{"-n", strconv.Itoa(probeThreads()), "-a", "1", "-d", "|",
		"-I", "CREATE TABLE t (w INT64, v INT64);",
		"-K", "CREATE TABLE agg (w INT64, v INT64);",
		"-E", "INSERT INTO t SELECT " + has("words") + ", " + has("gvec") + ";",
		"-J", "INSERT INTO agg SELECT sum(w), sum(v) FROM t;",
		"-G", "SELECT sum(w), sum(v) FROM agg;"})
	m := twoNums.FindStringSubmatch(out)
	if m == nil {
		return 0, 0, false
	}
	w, _ := strconv.ParseInt(m[1], 10, 64)
	v, _ := strconv.ParseInt(m[2], 10, 64)
	return w, v, true
}

func (r *Registry) countRolledUp(ctx context.Context, a string) *bool {
	out := r.aggregate(ctx, a, []string{"-n", strconv.Itoa(probeThreads()), "-a", "1", "-d", "|",
		"-I", "CREATE TABLE t (n INT64);", "-K", "CREATE TABLE agg (n INT64);",
		"-E", "INSERT INTO t SELECT count(*) FROM summary WHERE rollupscore > 0;",
		"-J", "INSERT INTO agg SELECT sum(n) FROM t;",
		"-G", "SELECT sum(n) FROM agg;"})
	m := oneNum.FindStringSubmatch(out)
	if m == nil {
		return nil
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	return ptr(n > 0)
}

// DetectFormat is detectFormat: which GUFI wrote a tree; nil if unknown.
func (r *Registry) DetectFormat(ctx context.Context, a string) *FormatRecord {
	out, err := r.sqlite(ctx, "ATTACH '"+sqlQuote(filepath.Join(a, "db.db"))+"' AS d;\nSELECT group_concat(name) FROM pragma_table_info('summary', 'd');\n")
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	f := FormatOfColumns(lines[len(lines)-1])
	if f == "" {
		return nil
	}
	if f != FormatOlder {
		return &FormatRecord{Format: f}
	}
	return &FormatRecord{Format: f, RolledUp: r.countRolledUp(ctx, a)}
}

var wordsRe = regexp.MustCompile(`\bwords\b`)
var gvecRe = regexp.MustCompile(`\bgvec\b`)

// Probe is probeLibrary: what a tree supports, one probe at a time per tree.
func (r *Registry) Probe(ctx context.Context, indexRoot string) (Caps, error) {
	a := abs(indexRoot)
	r.mu.Lock()
	if c, ok := r.probeCache[a]; ok && time.Since(c.at) < probeTTL {
		r.mu.Unlock()
		return c.caps, nil
	}
	if call, ok := r.probing[a]; ok {
		r.mu.Unlock()
		<-call.done
		return call.caps, call.err
	}
	call := &probeCall{done: make(chan struct{})}
	r.probing[a] = call
	r.mu.Unlock()
	// A probe outlives the request that started it, as Node's does.
	call.caps, call.err = r.probeNow(context.WithoutCancel(ctx), a)
	r.mu.Lock()
	delete(r.probing, a)
	r.mu.Unlock()
	close(call.done)
	return call.caps, call.err
}

func (r *Registry) probeNow(ctx context.Context, a string) (Caps, error) {
	caps := Caps{}
	tried, failed := 0, 0
	lastError := ""
	exactAt := ""
	count := false
	r.mu.Lock()
	was, hadWas := r.knownLocked()[a]
	r.mu.Unlock()
	if exists(filepath.Join(a, "db.db")) {
		caps.Index = true
		if r.gufiInstalled() {
			for _, db := range sampleDbs(a, 12) {
				tried++
				out, err := r.sqlite(ctx, "ATTACH '"+sqlQuote(db)+"' AS d;\nSELECT name FROM d.sqlite_master WHERE name IN ('words','gvec');\n")
				if err != nil {
					failed++
					lastError = firstLineOf(err.Error())
					if len(lastError) > 200 {
						lastError = lastError[:200]
					}
					continue
				}
				if wordsRe.MatchString(out) {
					caps.FullText = true
				}
				if gvecRe.MatchString(out) {
					caps.Vectors = true
				}
				if caps.FullText && caps.Vectors {
					break
				}
			}
			if caps.FullText && caps.Vectors {
				exactAt = nowISO()
			} else if !(tried > 0 && failed == tried) {
				recent := false
				if hadWas && was.ExactAt != "" && !was.Stale {
					if t, err := time.Parse(time.RFC3339Nano, was.ExactAt); err == nil && time.Since(t) < exactTTL {
						recent = true
					}
				}
				if recent {
					caps.FullText = caps.FullText || was.Caps.FullText
					caps.Vectors = caps.Vectors || was.Caps.Vectors
					exactAt = was.ExactAt
				} else {
					count = true
				}
			}
		}
	}
	format := (*FormatRecord)(nil)
	if hadWas {
		format = was.Format
	}
	allFailed := tried > 0 && failed == tried
	if caps.Index && r.gufiInstalled() && !allFailed {
		if f := r.DetectFormat(ctx, a); f != nil {
			format = f
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if allFailed {
		keep := caps
		probedAt := ""
		if hadWas {
			keep, probedAt = was.Caps, was.ProbedAt
		}
		k := KnownCaps{Caps: keep, ProbedAt: probedAt, Stale: true, Error: lastError}
		if hadWas && was.Format != nil {
			k.Format = was.Format
		}
		r.setKnownLocked(a, k)
		r.saveKnownLocked()
		r.probeCache[a] = probeEntry{caps: keep, at: time.Now().Add(-probeTTL + probeRetry)}
		return keep, nil
	}
	sameFormat := func(x, y *FormatRecord) bool {
		if x == nil || y == nil {
			return x == nil && y == nil
		}
		return x.Format == y.Format && ((x.RolledUp == nil && y.RolledUp == nil) || (x.RolledUp != nil && y.RolledUp != nil && *x.RolledUp == *y.RolledUp))
	}
	changed := !hadWas || was.Stale || was.Caps != caps || was.ExactAt != exactAt || !sameFormat(was.Format, format)
	r.setKnownLocked(a, KnownCaps{Caps: caps, ProbedAt: nowISO(), ExactAt: exactAt, Format: format})
	if changed {
		r.saveKnownLocked()
	}
	r.probeCache[a] = probeEntry{caps: caps, at: time.Now()}
	if count {
		r.countInBackgroundLocked(a)
	}
	return caps, nil
}

func (r *Registry) countInBackgroundLocked(a string) {
	if r.counting[a] {
		return
	}
	r.counting[a] = true
	gen := r.generation[a]
	go func() {
		w, v, ok := r.countTables(context.Background(), a)
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.counting, a)
		if r.generation[a] != gen {
			r.countInBackgroundLocked(a)
			return
		}
		cur, has := r.knownLocked()[a]
		if !ok || !has || cur.Stale {
			return
		}
		cur.Caps.FullText = cur.Caps.FullText || w > 0
		cur.Caps.Vectors = cur.Caps.Vectors || v > 0
		cur.ExactAt = nowISO()
		r.setKnownLocked(a, cur)
		r.saveKnownLocked()
		if fresh, ok := r.probeCache[a]; ok {
			r.probeCache[a] = probeEntry{caps: cur.Caps, at: fresh.at}
		}
	}()
}

// CountedForTests waits until no tree is being counted.
func (r *Registry) CountedForTests() {
	for {
		r.mu.Lock()
		n := len(r.counting)
		r.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// StaleProbe is staleProbe: after a tree was built or changed.
func (r *Registry) StaleProbe(indexRoot string) {
	a := abs(indexRoot)
	r.mu.Lock()
	delete(r.probeCache, a)
	r.generation[a]++
	if k, ok := r.knownLocked()[a]; ok && !k.Stale {
		k.Stale = true
		r.setKnownLocked(a, k)
		r.saveKnownLocked()
	}
	r.mu.Unlock()
	if r.gufiInstalled() {
		go r.Probe(context.Background(), a)
	}
}

// ---- the stored lists ----

func (r *Registry) loadAddedLocked() []Def {
	st := fileStamp(r.file())
	if r.added != nil && st == r.addedStamp {
		return r.added
	}
	r.addedStamp = st
	r.added = []Def{}
	b, err := os.ReadFile(r.file())
	if err != nil {
		return r.added
	}
	r.added = parseDefs(b)
	return r.added
}

func parseDefs(b []byte) []Def {
	var raw []json.RawMessage
	out := []Def{}
	if json.Unmarshal(b, &raw) != nil {
		return out
	}
	for _, x := range raw {
		var d Def
		if json.Unmarshal(x, &d) == nil && idRe.MatchString(d.ID) && d.IndexRoot != "" {
			out = append(out, d)
		}
	}
	return out
}

func (r *Registry) saveAddedLocked(defs []Def) error {
	os.MkdirAll(filepath.Dir(r.file()), 0o755)
	b, err := marshalJS(defs, "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(r.file(), b); err != nil {
		return err
	}
	r.added, r.addedStamp = defs, fileStamp(r.file())
	return nil
}

func (r *Registry) pinnedDefs() []Def {
	raw := r.o.Getenv("STUDIO_LIBRARIES")
	if raw == "" {
		return nil
	}
	return parseDefs([]byte(raw))
}

// ---- listing ----

func (r *Registry) primaryIndexed() bool {
	return r.gufiInstalled() && exists(r.o.GufiIndex) && exists(filepath.Join(r.o.GufiIndex, "db.db"))
}

func (r *Registry) primaryLibrary(label string) Library {
	a := abs(r.o.GufiIndex)
	indexed := r.primaryIndexed()
	c := r.capsOf(a, func() Caps { return Caps{Index: indexed, FullText: indexed} })
	if indexed {
		c.Index = true
	} else {
		c = Caps{}
	}
	return Library{ID: PrimaryID, Label: label, IndexRoot: r.o.GufiIndex, SourceRoot: r.o.KBRoot, Writable: true, Primary: true, Pinned: true,
		Caps: c, Meaning: r.o.Settings.Meaning(PrimaryID), Depth: r.o.Settings.Depth(PrimaryID), Format: r.formatOf(a)}
}

func (r *Registry) formatOf(a string) *FormatRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	if k, ok := r.knownLocked()[a]; ok {
		return k.Format
	}
	return nil
}

func (r *Registry) fromDef(d Def, pinned bool) Library {
	a := abs(d.IndexRoot)
	l := Library{ID: d.ID, Label: d.Label, IndexRoot: a, Build: d.Build, Pinned: pinned,
		Caps:   r.capsOf(a, func() Caps { return Caps{Index: exists(filepath.Join(a, "db.db"))} }),
		Format: r.formatOf(a), ReadThrough: StoredReadThrough(d.ReadThrough, d.ID, r.o.Log)}
	if l.Label == "" {
		l.Label = filepath.Base(a)
		if l.Label == "" || l.Label == "/" {
			l.Label = d.ID
		}
	}
	if d.SourceRoot != nil && *d.SourceRoot != "" {
		l.SourceRoot = abs(*d.SourceRoot)
	}
	if d.Build != nil {
		l.CacheRoot = d.Build.CacheRoot
		l.Meaning, l.Depth = r.o.Settings.Meaning(d.ID), r.o.Settings.Depth(d.ID)
	}
	return l
}

// Viewer names who a listing is for: nil is the deployment's own listing,
// which never shows a user's libraries; "" is the one user of a Studio
// without sign-in.
type Viewer = *string

// Deployment is the deployment's own listing.
var Deployment Viewer = nil

// ViewerOf is a viewer by id ("" with sign-in off).
func ViewerOf(id string) Viewer { return &id }

func (r *Registry) userLibrariesOf(viewer Viewer) []Library {
	if viewer == nil {
		return nil
	}
	u := r.o.Users
	owner := u.OwnerOf(*viewer)
	if owner == "" {
		return nil
	}
	defs := u.Load(owner)
	r.learnReached(defs)
	var out []Library
	for _, d := range defs {
		if d.IndexRoot == nil || *d.IndexRoot == "" || d.GufiBin == nil {
			continue
		}
		l := Library{ID: d.ID, Label: d.Label, IndexRoot: RemoteRootOf(owner, d.ID),
			Caps: Caps{Index: true, FullText: d.FullText != nil && *d.FullText, Vectors: d.Vectors != nil && *d.Vectors},
			Site: d.Site, Format: d.IndexFormat, ReadThrough: StoredReadThrough(d.ReadThrough, d.ID, r.o.Log)}
		if lbl := r.ReachedLabelOf(d); lbl != "" {
			l.Site = lbl
		}
		if d.Vectors == nil && d.Build == nil {
			l.VectorsUnknown = true
		}
		if d.Build != nil {
			l.Meaning, l.Depth = r.o.Settings.Meaning(d.ID), r.o.Settings.Depth(d.ID)
		}
		out = append(out, l)
	}
	return out
}

// SSHSiteOf is the ssh site of a library over plain ssh; "" for a cluster.
func (r *Registry) SSHSiteOf(d UserDef) string {
	if !d.IsSSH() || d.Destination == nil || *d.Destination == "" || r.o.SSHSite == nil {
		return ""
	}
	port := 0
	if d.Port != nil {
		port = *d.Port
	}
	s, err := r.o.SSHSite(*d.Destination, port, d.SSHOptions)
	if err != nil {
		return ""
	}
	return s
}

// ReachedLabelOf is reachedLabel: user@host as ssh reaches the library.
func (r *Registry) ReachedLabelOf(d UserDef) string {
	site := r.SSHSiteOf(d)
	if site == "" || r.o.ReachedLabel == nil {
		return ""
	}
	return r.o.ReachedLabel(site)
}

func (r *Registry) learnReached(defs []UserDef) {
	if r.o.Learn == nil {
		return
	}
	for _, d := range defs {
		if site := r.SSHSiteOf(d); site != "" {
			go r.o.Learn(site)
		}
	}
}

// List is listLibraries: primary first, pinned, added, then the viewer's own.
func (r *Registry) List(primaryLabel string, viewer Viewer) []Library {
	if primaryLabel == "" {
		primaryLabel = filepath.Base(r.o.KBRoot)
	}
	seen := map[string]bool{PrimaryID: true}
	out := []Library{r.primaryLibrary(primaryLabel)}
	r.mu.Lock()
	added := append([]Def(nil), r.loadAddedLocked()...)
	r.mu.Unlock()
	for _, group := range []struct {
		defs   []Def
		pinned bool
	}{{r.pinnedDefs(), true}, {added, false}} {
		for _, d := range group.defs {
			if seen[d.ID] {
				continue
			}
			seen[d.ID] = true
			out = append(out, r.fromDef(d, group.pinned))
		}
	}
	for _, u := range r.userLibrariesOf(viewer) {
		if !seen[u.ID] {
			seen[u.ID] = true
			out = append(out, u)
		}
	}
	return out
}

// ProbeAll is probeAll: each library's capabilities refreshed.
func (r *Registry) ProbeAll(ctx context.Context, primaryLabel string) []Library {
	libs := r.List(primaryLabel, Deployment)
	for i := range libs {
		libs[i].Caps, _ = r.Probe(ctx, libs[i].IndexRoot)
	}
	return libs
}

// Get is getLibrary.
func (r *Registry) Get(id, primaryLabel string, viewer Viewer) (Library, error) {
	want := strings.TrimSpace(id)
	if want == "" {
		want = PrimaryID
	}
	if strings.ToLower(want) == "all" {
		return Library{}, apierr.New(400, "This action works in one library at a time, and the items are in more than one (All libraries is selected).")
	}
	for _, l := range r.List(primaryLabel, viewer) {
		if l.ID == want {
			return l, nil
		}
	}
	return Library{}, apierr.New(404, "no library named %s", want)
}

// RequireWritable is requireWritable.
func RequireWritable(l Library) error {
	if !l.Writable {
		return apierr.New(403, "%s is read only", l.Label)
	}
	return nil
}

var slugNon = regexp.MustCompile(`[^a-z0-9]+`)
var dashEdges = regexp.MustCompile(`^-+|-+$`)
var dashEnd = regexp.MustCompile(`-+$`)

func slug(s string) string {
	s = dashEdges.ReplaceAllString(slugNon.ReplaceAllString(strings.ToLower(s), "-"), "")
	if len(s) > 34 {
		s = s[:34]
	}
	return dashEnd.ReplaceAllString(s, "")
}

// IDFor is libraryIdFor.
func (r *Registry) IDFor(label, indexRoot string) string {
	if indexRoot == "" {
		indexRoot = "."
	}
	base := slug(label)
	if base == "" {
		base = slug(filepath.Base(abs(indexRoot)))
	}
	if base == "" {
		base = "library"
	}
	if strings.HasPrefix(base, "u-") {
		base = "lib-" + base
		if len(base) > 34 {
			base = base[:34]
		}
	}
	taken := map[string]bool{PrimaryID: true, "all": true}
	for _, l := range r.List("", Deployment) {
		taken[l.ID] = true
	}
	id := base
	for i := 2; taken[id]; i++ {
		id = base + "-" + strconv.Itoa(i)
	}
	return id
}

// CheckNewID is checkNewId.
func (r *Registry) CheckNewID(id string) error {
	if !idRe.MatchString(id) {
		return apierr.New(400, "a library needs an id (letters, digits, dash, underscore) and an index root")
	}
	if id == "all" {
		return apierr.New(409, "all is reserved: it names every library at once")
	}
	if id == PrimaryID || slices.ContainsFunc(r.List("", Deployment), func(l Library) bool { return l.ID == id }) {
		return apierr.New(409, "a library named %s already exists", id)
	}
	return nil
}

func truncRunes(s string, n int) string {
	u := utf16Units(s)
	if len(u) > n {
		return utf16String(u[:n])
	}
	return s
}

// Add is addLibrary: an existing index, after a probe finds a db.db.
func (r *Registry) Add(ctx context.Context, def Def) (Library, error) {
	if def.ID == "" && def.IndexRoot != "" {
		def.ID = r.IDFor(def.Label, def.IndexRoot)
	}
	if !idRe.MatchString(def.ID) || def.IndexRoot == "" {
		return Library{}, apierr.New(400, "a library needs an id (letters, digits, dash, underscore) and an index root")
	}
	if err := r.CheckNewID(def.ID); err != nil {
		return Library{}, err
	}
	a := abs(def.IndexRoot)
	if !exists(a) {
		return Library{}, apierr.New(400, "no such directory: %s", def.IndexRoot)
	}
	if def.SourceRoot != nil && *def.SourceRoot != "" && !exists(abs(*def.SourceRoot)) {
		return Library{}, apierr.New(400, "no such directory: %s", *def.SourceRoot)
	}
	caps, _ := r.Probe(ctx, a)
	if !caps.Index {
		return Library{}, apierr.New(400, "%s does not look like a GUFI index (no db.db at its root)", def.IndexRoot)
	}
	var src *string
	if def.SourceRoot != nil && *def.SourceRoot != "" {
		src = ptr(abs(*def.SourceRoot))
	}
	r.mu.Lock()
	err := r.saveAddedLocked(append(append([]Def{}, r.loadAddedLocked()...), Def{ID: def.ID, Label: truncRunes(def.Label, 60), IndexRoot: a, SourceRoot: src}))
	r.mu.Unlock()
	if err != nil {
		return Library{}, err
	}
	return r.Get(def.ID, "", Deployment)
}

// AddBuilt is addBuiltLibrary.
func (r *Registry) AddBuilt(def Def) (Library, error) {
	if err := r.CheckNewID(def.ID); err != nil {
		return Library{}, err
	}
	d := Def{ID: def.ID, Label: truncRunes(def.Label, 60), IndexRoot: abs(def.IndexRoot), Build: def.Build}
	if def.SourceRoot != nil && *def.SourceRoot != "" {
		d.SourceRoot = ptr(abs(*def.SourceRoot))
	}
	r.mu.Lock()
	err := r.saveAddedLocked(append(append([]Def{}, r.loadAddedLocked()...), d))
	r.mu.Unlock()
	if err != nil {
		return Library{}, err
	}
	return r.Get(def.ID, "", Deployment)
}

// AddedDef is addedDef.
func (r *Registry) AddedDef(id string) *Def {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.loadAddedLocked() {
		if d.ID == id {
			c := d
			return &c
		}
	}
	return nil
}

// AddedDefs is every library added in Settings.
func (r *Registry) AddedDefs() []Def {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Def{}, r.loadAddedLocked()...)
}

// ModifyAdded changes one added library's definition; false if none.
func (r *Registry) ModifyAdded(id string, fn func(*Def)) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	defs := append([]Def{}, r.loadAddedLocked()...)
	for i := range defs {
		if defs[i].ID == id {
			fn(&defs[i])
			r.saveAddedLocked(defs)
			return true
		}
	}
	return false
}

// SetAddedReadThrough is setAddedReadThrough.
func (r *Registry) SetAddedReadThrough(id string, rt *ReadThrough) bool {
	return r.ModifyAdded(id, func(d *Def) { d.ReadThrough = rt })
}

// UpdateBuild is updateBuild: a built library's state; nil if none.
func (r *Registry) UpdateBuild(id string, patch func(*LocalBuild)) *Def {
	var out *Def
	r.ModifyAdded(id, func(d *Def) {
		if d.Build == nil {
			return
		}
		b := *d.Build
		patch(&b)
		d.Build = &b
		c := *d
		out = &c
	})
	return out
}

// OnRemoved registers what drops a module's own state for a removed library.
func (r *Registry) OnRemoved(fn func(id string)) { r.removedHooks = append(r.removedHooks, fn) }

// SetBuildProgressSource is setBuildProgressSource.
func (r *Registry) SetBuildProgressSource(fn func(id string) *BuildProgress) { r.progressOf = fn }

// Remove is removeLibrary.
func (r *Registry) Remove(id string) error {
	lib, err := r.Get(id, "", Deployment)
	if err != nil {
		return err
	}
	if lib.Primary || lib.Pinned {
		return apierr.New(403, "%s is set by the deployment and cannot be removed here", lib.Label)
	}
	def := r.AddedDef(id)
	if def != nil && def.Build != nil && (def.Build.State == "building" || def.Build.State == "updating") {
		return apierr.New(409, "%s is being indexed; remove it once the build has finished", lib.Label)
	}
	r.mu.Lock()
	err = r.saveAddedLocked(slices.DeleteFunc(append([]Def{}, r.loadAddedLocked()...), func(d Def) bool { return d.ID == id }))
	r.mu.Unlock()
	if err != nil {
		return err
	}
	dir := r.BuiltLibraryDir(id)
	if def != nil && def.Build != nil {
		os.RemoveAll(dir)
	}
	os.Remove(filepath.Join(dir, "labels.json"))
	os.Remove(filepath.Join(dir, "labels.json.bak"))
	for _, fn := range r.removedHooks {
		fn(id)
	}
	r.o.Settings.Forget(id)
	return nil
}

// SettingsView is settingsLibrary.
func (r *Registry) SettingsView(l Library) SettingsLibrary {
	v := SettingsLibrary{PublicLibrary: Public(l), IndexRoot: l.IndexRoot, SourceRoot: strOrNil(l.SourceRoot), ReadThrough: PublicReadThrough(l.ReadThrough)}
	if b := l.Build; b != nil {
		v.Build = &PublicBuild{IndexDir: b.IndexDir, Managed: b.Managed, State: b.State, Error: b.Error, BuiltAt: b.BuiltAt, Files: b.Files, Dirs: b.Dirs, Progress: r.progressOf(l.ID)}
	}
	return v
}

// ResetForTests is resetLibrariesForTests.
func (r *Registry) ResetForTests(keepKnown bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.added, r.known = nil, nil
	r.probeCache = map[string]probeEntry{}
	if !keepKnown {
		os.Remove(r.capsFile())
	}
}
