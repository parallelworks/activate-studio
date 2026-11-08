package userlibs

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/search"
)

// The connection test and the check before a build (userLibraries.ts),
// step by step.

// step is one step of a test: ssh, gufi, index, source or extract.
type step struct {
	Name   string
	OK     bool
	Warn   bool // passed, with something the user should know
	Detail string
}

func (s step) json() *jsonv.Object {
	o := jsonv.NewObject("name", s.Name, "ok", s.OK)
	if s.Warn {
		o.Set("warn", true)
	}
	return o.Set("detail", s.Detail)
}

// probe is SshProbe, and BuildProbe's fields after it. A nil pointer is a
// field the test left undefined.
type probe struct {
	OK          bool
	Steps       []step
	GufiBin     *string
	GufiVersion *string
	Directories *float64
	Shell       *bool
	FullText    *bool
	Vectors     *bool
	IndexFormat *libraries.FormatRecord
	OS          *string
	IndexRoot   *string
	SourceRoot  *string
	Files       *float64
	BuiltAt     *string
	// The check before a build.
	IndexDir *string
	Create   []string
	Model    *string
	Platform *string
}

func stepsJSON(steps []step) []any {
	out := make([]any, len(steps))
	for i, s := range steps {
		out[i] = s.json()
	}
	return out
}

func undef[T any](p *T) any {
	if p == nil {
		return jsonv.Undefined
	}
	return *p
}

// testJSON is what the connection test routes answer.
func (p *probe) testJSON() *jsonv.Object {
	return jsonv.NewObject("ok", p.OK, "steps", stepsJSON(p.Steps), "gufiBin", undef(p.GufiBin), "gufiVersion", undef(p.GufiVersion),
		"directories", undef(p.Directories), "shell", undef(p.Shell), "vectors", undef(p.Vectors))
}

// buildJSON is publicBuildProbe.
func (p *probe) buildJSON() *jsonv.Object {
	create := []any{}
	for _, c := range p.Create {
		create = append(create, c)
	}
	return jsonv.NewObject("ok", p.OK, "steps", stepsJSON(p.Steps), "gufiBin", undef(p.GufiBin), "gufiVersion", undef(p.GufiVersion),
		"sourceRoot", undef(p.SourceRoot), "indexDir", undef(p.IndexDir), "create", create)
}

func (p *probe) firstFailed() *step {
	for i := range p.Steps {
		if !p.Steps[i].OK {
			return &p.Steps[i]
		}
	}
	return nil
}

// probeHost is where a connection test runs: the session's site and key,
// the host as messages name it, and what a failed session means there.
type probeHost struct {
	Site    string
	Key     string
	Name    string
	Reached string // how the first step reads once connected
	Advice  func(message string) string
}

type runFn func(argv []string, timeout time.Duration) remote.Result

var gufiToolRe = regexp.MustCompile(`(^|/)gufi_\w+$`)

// runner runs a test's commands: GUFI's through the host's runner, logged
// (gufiLog.ts) and hidden from the trace's own list; a session that fails
// reads as exit -1 with its message.
func (m *Module) runner(ctx context.Context, h probeHost) runFn {
	return func(argv []string, timeout time.Duration) remote.Result {
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
		var r remote.Result
		var err error
		if gufiToolRe.MatchString(argv[0]) {
			r, err = m.engine().RemoteGufi(ctx, h.Site, h.Key, argv, search.GufiOpts{Timeout: timeout, Hide: true, Source: "libraries"})
		} else {
			r, err = m.Pool.Run(ctx, h.Site, h.Key, argv, timeout)
		}
		if err != nil {
			return remote.Result{Code: -1, Stderr: err.Error()}
		}
		return r
	}
}

// onlyGufiQuery is what the ssh step says on a host whose login runs only
// gufi_query.
const onlyGufiQuery = "This host runs only gufi_query: connect existing indexes read only; building an index there is not available."

var tildeRe = regexp.MustCompile(`^~(/|$)`)

func hasTilde(p string) bool { return tildeRe.MatchString(p) }

// connected is what the first step learned: the system, the home, and
// ~ resolved there.
type connected struct {
	OS, Home string
	Mac      bool
}

func (c connected) expand(p *string) *string {
	if p == nil || *p == "" || !hasTilde(*p) || !strings.HasPrefix(c.Home, "/") {
		return p
	}
	s := strings.TrimRight(c.Home, "/") + (*p)[1:]
	return &s
}

func osName(os string, mac bool) string {
	switch {
	case mac:
		return "macOS"
	case os != "":
		return os
	}
	return "Unix"
}

func splitHello(out string) (os, home string) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	os = lines[0]
	if len(lines) > 1 {
		home = lines[1]
	}
	return
}

func exitText(stderr string, code int) string {
	if stderr != "" {
		return stderr
	}
	return fmt.Sprintf("exit %d", code)
}

func lastOrExit(stderr string, code int) string {
	if l := lastLine(stderr); l != "" {
		return l
	}
	return fmt.Sprintf("exit %d", code)
}

// connectStep is the first step of a build check: the session reaches the
// host. Nil when it failed, or when the login runs only gufi_query.
func (m *Module) connectStep(h probeHost, run runFn, steps *[]step) *connected {
	// A test finds out afresh whether the login runs a shell.
	m.Pool.SetDirect(h.Site, false)
	r := run([]string{"sh", "-c", hello}, 45*time.Second)
	if r.Code != 0 {
		detail := onlyGufiQuery
		if !m.Pool.IsDirect(h.Site) {
			detail = h.Advice(exitText(r.Stderr, r.Code))
		}
		*steps = append(*steps, step{Name: "ssh", Detail: detail})
		return nil
	}
	os, home := splitHello(r.Stdout)
	c := &connected{OS: os, Home: home, Mac: os == "Darwin"}
	*steps = append(*steps, step{Name: "ssh", OK: true, Detail: fmt.Sprintf("Connected to %s, a %s host.", h.Reached, osName(os, c.Mac))})
	return c
}

type gufiFound struct {
	Bin, Version string
	Ran          bool
}

// gufiStep is the second step: GUFI is there, in the directory given, else
// on the PATH a non-interactive login has, then where the Studio installs
// it, then the usual places; and gufi_query runs. A build needs
// gufi_dir2index beside it.
func (m *Module) gufiStep(h probeHost, run runFn, steps *[]step, given *string, home string, mac, build bool) gufiFound {
	dest := h.Name
	need := "gufi_query"
	if build {
		need = "gufi_query gufi_dir2index"
	}
	search := searchTools
	where := []string{"/usr/local/bin", home + "/usr/local/bin", "/opt/gufi/bin"}
	if given != nil && *given != "" {
		search = findTools
		where = []string{*given}
	}
	found := run(append([]string{"sh", "-c", search, "_", need}, where...), 0)
	bin := ""
	if found.Code == 0 {
		bin = lastLine(found.Stdout)
	}
	if !strings.HasPrefix(bin, "/") {
		var detail string
		g := ""
		if given != nil {
			g = *given
		}
		switch {
		case build:
			tools := "gufi_dir2index and gufi_query"
			at := tools + " were not found on the PATH an ssh login gets, nor in ~/.activate-studio/gufi, /usr/local/bin, ~/usr/local/bin, or /opt/gufi/bin"
			if g != "" {
				at = "there is no " + tools + " in " + g
			}
			detail = fmt.Sprintf("GUFI is not installed on %s: %s. Install GUFI there (github.com/mar-file-system/GUFI), or give the directory that holds its tools as the GUFI bin directory.", dest, at)
		case g != "":
			detail = fmt.Sprintf("There is no gufi_query in %s on %s. Give the directory that holds it, such as /usr/local/bin.", g, dest)
		default:
			detail = fmt.Sprintf("gufi_query was not found on %s: not on the PATH an ssh login gets, nor in ~/.activate-studio/gufi, /usr/local/bin, ~/usr/local/bin, or /opt/gufi/bin. Install GUFI there, or give the directory that holds gufi_query as the GUFI bin directory.", dest)
		}
		*steps = append(*steps, step{Name: "gufi", Detail: detail})
		return gufiFound{}
	}
	v := run([]string{bin + "/gufi_query", "--version"}, 0)
	version := ""
	if v.Code == 0 {
		version = slice(lastLine(v.Stdout), 80)
		shown := version
		if shown == "" {
			shown = "(no version)"
		}
		*steps = append(*steps, step{Name: "gufi", OK: true, Detail: fmt.Sprintf("gufi_query %s in %s.", shown, bin)})
	} else {
		macNote := ""
		if mac {
			macNote = fmt.Sprintf(" On macOS its libraries are expected in %s/lib.", path.Dir(bin))
		}
		*steps = append(*steps, step{Name: "gufi", Detail: fmt.Sprintf("gufi_query in %s did not run: %s.%s", bin, lastOrExit(v.Stderr, v.Code), macNote)})
	}
	return gufiFound{Bin: bin, Version: version, Ran: v.Code == 0}
}

// passedProbeFor is how long a passing test is kept for adding the
// library: the add runs the test again right after the page ran it.
const passedProbeFor = 2 * time.Minute

type passedProbe struct {
	at time.Time
	p  *probe
}

type indexSpec struct {
	IndexRoot  string
	GufiBin    *string
	SourceRoot *string
}

func (m *Module) probeIndex(ctx context.Context, h probeHost, spec indexSpec, reuse bool) *probe {
	idb, _ := json.Marshal([]any{h.Site, slice16(h.Key), spec.IndexRoot, spec.GufiBin, spec.SourceRoot})
	id := string(idb)
	m.mu.Lock()
	hit, ok := m.passed[id]
	m.mu.Unlock()
	if reuse && ok && time.Since(hit.at) < passedProbeFor {
		return hit.p
	}
	p := m.probeIndexNow(ctx, h, spec)
	m.mu.Lock()
	if p.OK {
		m.passed[id] = passedProbe{time.Now(), p}
	} else {
		delete(m.passed, id)
	}
	m.mu.Unlock()
	return p
}

type indexCount struct {
	Directories, Files *float64
	Vectors            *bool
	Slow               bool
	Error              string
}

func parseSums(out string) []float64 {
	parts := strings.Split(lastLine(out), "|")
	vals := make([]float64, len(parts))
	for i, s := range parts {
		if jsTrim(s) == "" {
			vals[i] = math.NaN()
		} else {
			vals[i] = number(s)
		}
	}
	return vals
}

func at(v []float64, i int) float64 {
	if i < len(v) {
		return v[i]
	}
	return math.NaN()
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

var didNotFinish = regexp.MustCompile(`did not finish`)

// countIndex is the index's directories and files in one aggregate
// gufi_query, and the directories with the Studio's vector table; a GUFI
// that cannot answer that is asked for the counts alone.
func (m *Module) countIndex(ctx context.Context, h probeHost, run runFn, bin, indexRoot string) indexCount {
	threads := strconv.Itoa(m.Pool.Threads(ctx, h.Site, h.Key))
	walk := func(vectors bool) remote.Result {
		v, sel, sum := "", "", ""
		if vectors {
			v, sel, sum = ", v INT64", ", (SELECT count(*) FROM tree.sqlite_master WHERE name = 'gvec')", ", sum(v)"
		}
		return run([]string{libraries.GufiTool(bin, ""), "-n", threads, "-d", "|", "-a", "1",
			"-I", "CREATE TABLE t (d INT64, f INT64" + v + ");", "-K", "CREATE TABLE agg (d INT64, f INT64" + v + ");",
			"-E", "INSERT INTO t SELECT count(*), sum(totfiles)" + sel + " FROM vrsummary;",
			"-J", "INSERT INTO agg SELECT * FROM t;",
			"-G", "SELECT sum(d), sum(f)" + sum + " FROM agg;", indexRoot}, 60*time.Second)
	}
	c := walk(true)
	vals := parseSums(c.Stdout)
	d, f, vv := at(vals, 0), at(vals, 1), at(vals, 2)
	if !finite(d) && !didNotFinish.MatchString(c.Stderr) {
		c = walk(false)
		vals = parseSums(c.Stdout)
		d, f, vv = at(vals, 0), at(vals, 1), math.NaN()
	}
	if finite(d) {
		n := indexCount{Directories: &d}
		if finite(f) {
			n.Files = &f
		}
		if finite(vv) {
			b := vv > 0
			n.Vectors = &b
		}
		return n
	}
	return indexCount{Slow: didNotFinish.MatchString(c.Stderr), Error: lastOrExit(c.Stderr, c.Code)}
}

// indexDetail is what the connection test says of an index: its counts,
// and what search covers there.
func indexDetail(directories float64, files *float64, fullText, vectors *bool) string {
	counts := "A GUFI index of " + localeNumber(directories) + " directories"
	if files != nil {
		counts += " and " + localeNumber(*files) + " files"
	}
	ft := fullText != nil && *fullText
	words := ""
	if ft {
		words = ", with full-text tables, so search covers the words in its files"
	}
	meaning := ""
	switch {
	case vectors != nil && *vectors && ft:
		meaning = ", and vectors, so search by meaning covers it too"
	case vectors != nil && *vectors:
		meaning = ", with vectors, so search by meaning covers it"
	case vectors != nil:
		meaning = ". It has no vectors, so search by meaning does not cover it"
	}
	return counts + words + meaning + "."
}

// hasWords is whether an index carries the Studio's full-text tables.
func (m *Module) hasWords(ctx context.Context, h probeHost, run runFn, bin, indexRoot string) bool {
	r := run([]string{libraries.GufiTool(bin, ""), "-n", strconv.Itoa(m.Pool.Threads(ctx, h.Site, h.Key)), "-d", "|", "--max-level", "2",
		"-E", "SELECT 1 FROM words LIMIT 1;", indexRoot}, 30*time.Second)
	return jsTrim(r.Stdout) != ""
}

// remoteFormat is which GUFI wrote an index there; nil where gufi_query
// could not say.
func (m *Module) remoteFormat(ctx context.Context, h probeHost, run runFn, bin, indexRoot string) *libraries.FormatRecord {
	r := run([]string{libraries.GufiTool(bin, ""), "-n", "1", "-d", "|", "--min-level", "0", "--max-level", "0", "-E", libraries.SummaryColumnsSQL, indexRoot}, 30*time.Second)
	format := libraries.FormatOfColumns(lastLine(r.Stdout))
	if format == "" {
		return nil
	}
	if format != "older" {
		return &libraries.FormatRecord{Format: format}
	}
	c := run([]string{libraries.GufiTool(bin, ""), "-n", strconv.Itoa(m.Pool.Threads(ctx, h.Site, h.Key)), "-d", "|", "-a", "1",
		"-I", "CREATE TABLE t (n INT64);", "-K", "CREATE TABLE agg (n INT64);",
		"-E", "INSERT INTO t SELECT count(*) FROM summary WHERE rollupscore > 0;", "-J", "INSERT INTO agg SELECT sum(n) FROM t;",
		"-G", "SELECT sum(n) FROM agg;", indexRoot}, 60*time.Second)
	last := jsTrim(lastLine(c.Stdout))
	n := number(last)
	if finite(n) && last != "" {
		rolled := n > 0
		return &libraries.FormatRecord{Format: format, RolledUp: &rolled}
	}
	return &libraries.FormatRecord{Format: format}
}

var (
	notDirRe     = regexp.MustCompile(`(?i)Could not stat directory|No such file|is not a directory`)
	deniedRe     = regexp.MustCompile(`(?i)Permission denied`)
	noDatabaseRe = regexp.MustCompile(`(?i)Cannot attach database|unable to open database`)
)

// probeQueryOnly is the test where the login runs only gufi_query: no
// shell, so no ~ to resolve, no search for GUFI, and no files to read.
func (m *Module) probeQueryOnly(ctx context.Context, h probeHost, spec indexSpec, first remote.Result) *probe {
	dest := h.Name
	run := m.runner(ctx, h)
	p := &probe{Shell: ptr(false)}
	add := func(s step) { p.Steps = append(p.Steps, s) }
	done := func() *probe { p.OK = allOK(p.Steps); return p }
	add(step{Name: "ssh", OK: true, Warn: true, Detail: fmt.Sprintf("Connected to %s. %s", h.Reached, onlyGufiQuery)})
	if spec.GufiBin != nil && hasTilde(*spec.GufiBin) {
		add(step{Name: "gufi", Detail: fmt.Sprintf("The login on %s runs no shell, so ~ cannot be resolved there: give the GUFI bin directory as an absolute path, or leave it empty for gufi_query on the login's PATH.", dest)})
		return done()
	}
	bin := deref(spec.GufiBin)
	if first.Code != 0 {
		why := lastOrExit(first.Stderr, first.Code)
		if bin != "" {
			add(step{Name: "gufi", Detail: fmt.Sprintf("gufi_query in %s did not run on %s: %s. A restricted login may refuse a path; leave the GUFI bin directory empty to run gufi_query from the login's PATH.", bin, dest, why)})
		} else {
			add(step{Name: "gufi", Detail: fmt.Sprintf("gufi_query did not run on %s: %s. The login runs nothing else, so the Studio cannot look for GUFI elsewhere; give the directory that holds gufi_query as the GUFI bin directory.", dest, why)})
		}
		return done()
	}
	version := slice(lastLine(first.Stdout), 80)
	shown, where := version, "on the login's PATH"
	if shown == "" {
		shown = "(no version)"
	}
	if bin != "" {
		where = "in " + bin
	}
	add(step{Name: "gufi", OK: true, Detail: fmt.Sprintf("gufi_query %s %s.", shown, where)})
	p.GufiBin = &bin
	if version != "" {
		p.GufiVersion = &version
	}
	if hasTilde(spec.IndexRoot) {
		add(step{Name: "index", Detail: fmt.Sprintf("The login on %s runs no shell, so ~ cannot be resolved there: give the index directory as an absolute path.", dest)})
		return done()
	}
	indexRoot := spec.IndexRoot
	n := m.countIndex(ctx, h, run, bin, indexRoot)
	switch {
	case n.Directories != nil:
		ft := m.hasWords(ctx, h, run, bin, indexRoot)
		p.FullText = &ft
		p.IndexFormat = m.remoteFormat(ctx, h, run, bin, indexRoot)
		add(step{Name: "index", OK: true, Detail: indexDetail(*n.Directories, n.Files, p.FullText, n.Vectors)})
	case n.Slow:
		add(step{Name: "index", OK: true, Detail: fmt.Sprintf("gufi_query reads %s; counting its directories took over a minute, so the count is left out.", indexRoot)})
	default:
		var detail string
		switch {
		case notDirRe.MatchString(n.Error):
			detail = fmt.Sprintf("%s is not a directory on %s.", indexRoot, dest)
		case deniedRe.MatchString(n.Error):
			detail = fmt.Sprintf("The login on %s cannot read %s.", dest, indexRoot)
		case noDatabaseRe.MatchString(n.Error):
			detail = fmt.Sprintf("%s has no db.db the login can read, so it is not the top of a GUFI index. Give the directory gufi_dir2index created, the one holding db.db.", indexRoot)
		default:
			detail = fmt.Sprintf("gufi_query could not read the index in %s: %s.", indexRoot, n.Error)
		}
		add(step{Name: "index", Detail: detail})
	}
	if spec.SourceRoot != nil && *spec.SourceRoot != "" {
		add(step{Name: "source", OK: true, Warn: true, Detail: fmt.Sprintf("The login on %s runs only gufi_query, so files cannot be opened from the Studio; browsing and search work from the index.", dest)})
	}
	p.Directories, p.Files, p.Vectors = n.Directories, n.Files, n.Vectors
	p.IndexRoot = &indexRoot
	return done()
}

func allOK(steps []step) bool {
	for _, s := range steps {
		if !s.OK {
			return false
		}
	}
	return true
}

func (m *Module) probeIndexNow(ctx context.Context, h probeHost, spec indexSpec) *probe {
	dest := h.Name
	run := m.runner(ctx, h)
	p := &probe{}
	add := func(s step) { p.Steps = append(p.Steps, s) }
	done := func() *probe { p.OK = allOK(p.Steps); return p }

	// The session first, which needs a shell there. A login that refuses it
	// is marked to run only gufi_query, not failed: the test goes on with
	// gufi_query --version as its own call.
	m.Pool.SetDirect(h.Site, false)
	hi := run([]string{"sh", "-c", hello}, 45*time.Second)
	if hi.Code != 0 && m.Pool.IsDirect(h.Site) {
		given := ""
		if spec.GufiBin != nil && strings.HasPrefix(*spec.GufiBin, "/") {
			given = *spec.GufiBin
		}
		first, err := m.Pool.RunOnce(ctx, h.Site, h.Key, []string{libraries.GufiTool(given, ""), "--version"}, 45*time.Second, nil)
		if err != nil {
			add(step{Name: "ssh", Detail: h.Advice(err.Error())})
			return done()
		}
		return m.probeQueryOnly(ctx, h, spec, first)
	}
	if hi.Code != 0 {
		add(step{Name: "ssh", Detail: h.Advice(exitText(hi.Stderr, hi.Code))})
		return done()
	}
	os, home := splitHello(hi.Stdout)
	c := connected{OS: os, Home: home, Mac: os == "Darwin"}
	add(step{Name: "ssh", OK: true, Detail: fmt.Sprintf("Connected to %s, a %s host.", h.Reached, osName(os, c.Mac))})
	g := m.gufiStep(h, run, &p.Steps, c.expand(spec.GufiBin), home, c.Mac, false)
	bin, version := g.Bin, g.Version

	indexRoot := *c.expand(&spec.IndexRoot)
	r := run([]string{"sh", "-c", indexCheck, "_", indexRoot}, 0)
	if r.Code == 0 {
		mtime := number(lastLine(r.Stdout))
		if mtime > 0 {
			s := time.UnixMilli(int64(mtime * 1000)).UTC().Format("2006-01-02T15:04:05.000Z")
			p.BuiltAt = &s
		}
		if version == "" {
			add(step{Name: "index", OK: true, Detail: fmt.Sprintf("%s has a db.db; its directories are counted once gufi_query runs.", indexRoot)})
		} else {
			n := m.countIndex(ctx, h, run, bin, indexRoot)
			switch {
			case n.Directories != nil:
				p.Directories, p.Files = n.Directories, n.Files
				ft := m.hasWords(ctx, h, run, bin, indexRoot)
				p.FullText = &ft
				p.IndexFormat = m.remoteFormat(ctx, h, run, bin, indexRoot)
				p.Vectors = n.Vectors
				add(step{Name: "index", OK: true, Detail: indexDetail(*n.Directories, p.Files, p.FullText, p.Vectors)})
			case n.Slow:
				add(step{Name: "index", OK: true, Detail: fmt.Sprintf("%s has a db.db; counting its directories took over a minute, so the count is left out.", indexRoot)})
			default:
				add(step{Name: "index", Detail: fmt.Sprintf("gufi_query could not read the index in %s: %s.", indexRoot, n.Error)})
			}
		}
	} else {
		var detail string
		switch r.Code {
		case 3:
			detail = fmt.Sprintf("%s is not a directory on %s.", indexRoot, dest)
		case 4:
			below := []string{}
			for _, l := range strings.Split(strings.TrimSpace(r.Stdout), "\n") {
				if l != "" {
					below = append(below, l)
				}
			}
			hint := "Give the directory gufi_dir2index created, the one holding db.db."
			if len(below) > 0 {
				hint = "The index may be one level down: " + strings.Join(below, ", ") + "."
			}
			detail = fmt.Sprintf("%s has no db.db, so it is not the top of a GUFI index. %s", indexRoot, hint)
		case 5:
			detail = fmt.Sprintf("The ssh login cannot read %s/db.db.", indexRoot)
		default:
			detail = fmt.Sprintf("The index could not be checked: %s.", lastOrExit(r.Stderr, r.Code))
		}
		add(step{Name: "index", Detail: detail})
	}

	sourceRoot := c.expand(spec.SourceRoot)
	if sourceRoot != nil && *sourceRoot != "" {
		s := run([]string{"sh", "-c", sourceCheck, "_", *sourceRoot}, 0)
		switch s.Code {
		case 0:
			add(step{Name: "source", OK: true, Detail: fmt.Sprintf("%s is readable; files open from there.", *sourceRoot)})
		case 3:
			add(step{Name: "source", Detail: fmt.Sprintf("%s is not a directory on %s. Leave the source directory empty to browse and search names only.", *sourceRoot, dest)})
		case 4:
			add(step{Name: "source", Detail: fmt.Sprintf("%s is there, but the ssh login cannot read it.", *sourceRoot)})
		default:
			add(step{Name: "source", Detail: fmt.Sprintf("The source directory could not be checked: %s.", lastOrExit(s.Stderr, s.Code))})
		}
	}
	p.Shell = ptr(true)
	if strings.HasPrefix(bin, "/") {
		p.GufiBin = &bin
	}
	if version != "" {
		p.GufiVersion = &version
	}
	p.OS, p.IndexRoot, p.SourceRoot = &os, &indexRoot, sourceRoot
	return done()
}

// sshAdvice is what a failed ssh session means, and what the user can do
// about it.
func sshAdvice(message, dest string, port int) string {
	site, _ := remote.SSHSite(dest, port, nil)
	said := strings.Replace(message, remote.SiteName(site)+": ", "", 1)
	p := 22
	if port != 0 {
		p = port
	}
	switch {
	case strings.Contains(said, "ENOENT"):
		return "There is no ssh client on the machine the Studio runs on."
	case regexp.MustCompile(`(?i)Permission denied`).MatchString(said):
		return fmt.Sprintf("%s did not accept a key and asked for a password or another prompt, which the Studio cannot answer. Set up key access (for example ssh-copy-id %s), load the key into your ssh agent or name it in ~/.ssh/config, then test again.", dest, dest)
	case regexp.MustCompile(`(?i)Host key verification failed|IDENTIFICATION HAS CHANGED`).MatchString(said):
		return fmt.Sprintf("The host key of %s is not known here yet, or has changed. Connect once from a terminal (ssh %s) and check and accept the key, then test again.", dest, dest)
	case regexp.MustCompile(`(?i)Could not resolve hostname|not known|nodename nor servname`).MatchString(said):
		return fmt.Sprintf("The host name in %s does not resolve. Check the spelling, or the Host entry in ~/.ssh/config.", dest)
	case regexp.MustCompile(`(?i)Connection refused`).MatchString(said):
		return fmt.Sprintf("Nothing accepted the connection on port %d. Check the port, and that the ssh server runs on %s.", p, dest)
	case regexp.MustCompile(`(?i)timed out|No route to host|Network is unreachable|did not finish`).MatchString(said):
		return fmt.Sprintf("%s did not answer. Check the address, and that this machine can reach it (a VPN, for example).", dest)
	case regexp.MustCompile(`(?i)bash: .*not found|bash: No such file`).MatchString(said):
		return fmt.Sprintf("bash is not installed on %s, so the Studio can run only gufi_query there: connect an existing index read only.", dest)
	}
	return "The ssh connection failed: " + said
}

// pwAdvice is what a failed pw ssh session means.
func pwAdvice(message, cluster string) string {
	said := strings.Replace(message, cluster+": ", "", 1)
	switch {
	case strings.Contains(said, "ENOENT"):
		return "The pw CLI is not installed on the machine the Studio runs on."
	case regexp.MustCompile(`(?i)authenticat|unauthori[sz]ed|\b401\b|expired|not logged in|sign in`).MatchString(said):
		return "The pw CLI was refused: its sign-in is missing or has expired. Run pw auth on the machine the Studio runs on, then test again."
	case regexp.MustCompile(`(?i)not found|no such|does not exist|not running|not active|unknown (?:resource|cluster)|could not find`).MatchString(said):
		return fmt.Sprintf("%s is not one of your running clusters. Start it on ACTIVATE, or choose another.", cluster)
	case regexp.MustCompile(`(?i)timed out|did not finish|No route to host|Network is unreachable`).MatchString(said):
		return fmt.Sprintf("%s did not answer. Check that it is running.", cluster)
	case regexp.MustCompile(`(?i)bash: .*not found|bash: No such file`).MatchString(said):
		return fmt.Sprintf("bash is not installed on %s, so the Studio can run only gufi_query there: connect an existing index read only.", cluster)
	}
	return fmt.Sprintf("pw ssh to %s failed: %s", cluster, said)
}

// reachedOver is how the ssh step names the host once connected: with its
// port, and the jump hosts ssh goes through.
func (m *Module) reachedOver(site, destination string, port int) string {
	m.Pool.ReachedHost(site)
	jump, proxy := m.Pool.Route(site)
	via := ""
	if jump != "" {
		via = " through " + jump
	} else if proxy {
		via = " through the ProxyCommand in your ~/.ssh/config"
	}
	return destination + portText(port) + via
}

func portText(port int) string {
	if port == 0 {
		return ""
	}
	return fmt.Sprintf(" on port %d", port)
}

// probeSSH is the connection test over plain ssh; reuse takes a test that
// passed in the last two minutes.
func (m *Module) probeSSH(ctx context.Context, spec sshSpec, reuse bool) (*probe, error) {
	site, err := remote.SSHSite(spec.Destination, spec.Port, spec.SSHOptions)
	if err != nil {
		return nil, err
	}
	h := probeHost{Site: site, Name: spec.Destination, Reached: m.reachedOver(site, spec.Destination, spec.Port),
		Advice: func(msg string) string { return sshAdvice(msg, spec.Destination, spec.Port) }}
	return m.probeIndex(ctx, h, indexSpec{spec.IndexRoot, spec.GufiBin, spec.SourceRoot}, reuse), nil
}

// probePw is the connection test over pw ssh, with the owner's key.
func (m *Module) probePw(ctx context.Context, spec pwSpec, key string, reuse bool) *probe {
	h := probeHost{Site: spec.Cluster, Key: key, Name: spec.Cluster, Reached: spec.Cluster,
		Advice: func(msg string) string { return pwAdvice(msg, spec.Cluster) }}
	return m.probeIndex(ctx, h, indexSpec{spec.IndexRoot, spec.GufiBin, spec.SourceRoot}, reuse)
}
