package search

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/bridge"
	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/gufitrace"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// A query across several libraries as one gufi_vt statement (gufiVt.ts):
// a gufi_vt virtual table per library, unioned, with the overall order and
// limit in the outer SELECT.
//
// A library on this host is a plain gufi_vt location. A library on
// another system is a remote one, whose remote_cmd is this binary's
// gufi-vt-remote subcommand: gufi_vt hands it the gufi_query command line,
// the subcommand splits it without a shell and posts it to this server
// over loopback, and the server runs it on the host through the user's
// session (internal/remote), so no remote shell parses the SQL and the
// user's key never appears in a process's arguments. Each location is
// checked first and one that cannot answer is left out and reported, since
// gufi_vt fails the whole statement when a location exits nonzero with no
// rows.

// VtOrder is the outer ORDER BY and LIMIT of a gufi_vt statement.
type VtOrder struct {
	Col   int
	Desc  bool
	Limit int
}

// VtRow is a row of one library.
type VtRow struct {
	ID  string
	Row []string
}

// VtFailure is a library gufi_vt could not ask.
type VtFailure struct {
	Library string
	Error   string
}

// VtResult is runAcross's answer.
type VtResult struct {
	Rows    []VtRow
	Failed  []VtFailure
	Elapsed time.Duration
}

// VtItem is one library and its plan.
type VtItem struct {
	Lib  libraries.Library
	Plan gufi.Plan
}

const (
	vtTokenTTL = 10 * time.Minute
	vtThreads  = 4
)

// VtSubcommand is the binary's subcommand gufi_vt runs for a remote
// location (cmd/studio-server/sub_gufi_vt_remote.go).
const VtSubcommand = "gufi-vt-remote"

// The subcommand presents the bridge secret (bridge.Secret), both as
// X-Studio-Secret, which the route checks as gufiVt.ts does, and as the
// bridge header, which lets it past the front's refusal of
// /internal/gufi-vt (gufivt_bridge.go) to clients without it.
func vtSecret() string { return bridge.Secret() }

// vtLocation is a remote location gufi_vt asks through the subcommand:
// the session, and the index roots it may run gufi_query on.
type vtLocation struct {
	site, key, gufiBin string
	indexes            []string
	label              string
	err                string
	ran                *vtRan
	at                 time.Time
}

type vtRan struct {
	argv []string
	note gufitrace.Run
}

var (
	vtMu     sync.Mutex
	vtTokens = map[string]*vtLocation{}
)

// vtCommand is the program gufi_vt runs for a remote location: this binary
// and its subcommand. Tests set it.
var vtCommand = func() []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	return []string{exe, VtSubcommand}
}

// SetVtCommandForTests sets the program and leading arguments gufi_vt runs
// for a remote location (nil restores this binary's), and probes again.
func SetVtCommandForTests(argv []string) {
	if argv == nil {
		vtCommand = func() []string {
			exe, err := os.Executable()
			if err != nil {
				return nil
			}
			return []string{exe, VtSubcommand}
		}
	} else {
		vtCommand = func() []string { return argv }
	}
	ResetGufiVtForTests()
}

var vtBadPath = regexp.MustCompile(`[\x00\n\r]|^['"]|['"]$`)

// VtPath is vtPath: a path as a gufi_vt argument, quoted, or "" when it
// cannot be one. gufi_vt strips a value's outer quotes and does not undo
// doubled ones, and runs gufi_query and remote_cmd with execvp, so spaces
// need no escaping: a path is single-quoted, or double-quoted when it holds
// a single quote.
func VtPath(p string) string {
	if p == "" || vtBadPath.MatchString(p) {
		return ""
	}
	if !strings.Contains(p, "'") {
		return "'" + p + "'"
	}
	if !strings.Contains(p, `"`) {
		return `"` + p + `"`
	}
	return ""
}

var vtBadSpace = regexp.MustCompile(`\s\s|[\t\v\f]|\s$`)

// VtRemoteIndex is vtRemoteIndex: whether a remote location's index can
// be written, unquoted, at the end of the line gufi_vt hands remote_cmd.
func VtRemoteIndex(p string) bool {
	if VtPath(p) == "" || vtBadSpace.MatchString(p) {
		return false
	}
	for _, w := range strings.Split(p, " ") {
		if w == "" || w[0] == '\'' || w[0] == '"' {
			return false
		}
	}
	return true
}

// VtStage is vtStage: a SQL stage as gufi_vt can take it, ok false when it
// cannot. A double quote inside a single-quoted literal is written as
// '||char(34)||'; one anywhere else cannot be rewritten.
func VtStage(sql string) (string, bool) {
	var out strings.Builder
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if c == '"' {
			return "", false
		}
		if c == '-' && i+1 < len(sql) && sql[i+1] == '-' {
			if strings.Contains(sql[i:], `"`) {
				return "", false
			}
			return out.String() + sql[i:], true
		}
		if c == '/' && i+1 < len(sql) && sql[i+1] == '*' {
			end := strings.Index(sql[i+2:], "*/")
			comment := sql[i:]
			if end >= 0 {
				comment = sql[i : i+2+end+2]
			}
			if strings.Contains(comment, `"`) {
				return "", false
			}
			out.WriteString(comment)
			i += len(comment) - 1
			continue
		}
		if c != '\'' {
			out.WriteByte(c)
			continue
		}
		j := i + 1
		var body strings.Builder
		for ; j < len(sql); j++ {
			if sql[j] == '\'' {
				if j+1 < len(sql) && sql[j+1] == '\'' {
					body.WriteString("''")
					j++
					continue
				}
				break
			}
			body.WriteByte(sql[j])
		}
		if j >= len(sql) {
			return "", false
		}
		b := body.String()
		if strings.Contains(b, `"`) {
			out.WriteString("('" + strings.Join(strings.Split(b, `"`), "'||char(34)||'") + "')")
		} else {
			out.WriteString("'" + b + "'")
		}
		i = j
	}
	return out.String(), true
}

func stageList(s gufi.Stages) [][2]string {
	return [][2]string{{"I", s.I}, {"E", s.E}, {"K", s.K}, {"J", s.J}, {"G", s.G}}
}

// VtEligible is vtEligible: whether a plan can run as a gufi_vt location.
func VtEligible(p gufi.Plan) bool {
	for _, s := range stageList(p.Stages) {
		if s[1] != "" {
			if _, ok := VtStage(s[1]); !ok {
				return false
			}
		}
	}
	return true
}

// VtLocatable is vtLocatable: whether a library's paths can be passed to
// gufi_vt.
func (e *Engine) VtLocatable(l libraries.Library) bool {
	if !l.Remote() {
		abs, _ := filepath.Abs(l.IndexRoot)
		return VtPath(abs) != ""
	}
	t, err := e.Target(l.IndexRoot)
	if err != nil || t == nil {
		return true
	}
	return VtRemoteIndex(t.IndexRoot) && (t.GufiBin == "" || VtPath(t.GufiBin) != "")
}

var (
	vtAvailMu     sync.Mutex
	vtAvailDone   bool
	vtAvail       bool
	vtForced      *bool
	vtAvailFlight chan struct{}
)

// ResetGufiVtForTests forgets the probe and every location.
func ResetGufiVtForTests() {
	vtAvailMu.Lock()
	vtAvailDone, vtAvail, vtForced = false, false, nil
	vtAvailMu.Unlock()
	vtMu.Lock()
	vtTokens = map[string]*vtLocation{}
	vtMu.Unlock()
}

// ForceGufiVtForTests turns gufi_vt off or on regardless of the probe;
// nil restores the probe.
func ForceGufiVtForTests(v *bool) {
	vtAvailMu.Lock()
	vtForced = v
	vtAvailMu.Unlock()
}

// GufiVtAvailable is gufiVtAvailable: this host's gufi_sqlite3 has gufi_vt
// with remote locations. Probed once.
func (e *Engine) GufiVtAvailable(ctx context.Context) bool {
	vtAvailMu.Lock()
	if vtForced != nil {
		v := *vtForced
		vtAvailMu.Unlock()
		return v
	}
	if vtAvailDone {
		v := vtAvail
		vtAvailMu.Unlock()
		return v
	}
	if f := vtAvailFlight; f != nil {
		vtAvailMu.Unlock()
		<-f
		return e.GufiVtAvailable(ctx)
	}
	f := make(chan struct{})
	vtAvailFlight = f
	vtAvailMu.Unlock()
	v := e.probeVt(ctx)
	vtAvailMu.Lock()
	vtAvail, vtAvailDone, vtAvailFlight = v, true, nil
	vtAvailMu.Unlock()
	close(f)
	return v
}

func (e *Engine) probeVt(ctx context.Context) bool {
	nothing := ""
	for _, p := range []string{"/usr/bin/true", "/bin/true"} {
		if exists(p) {
			nothing = p
			break
		}
	}
	cmd := vtCommand()
	if nothing == "" || e.Local == nil || !exists(filepath.Join(e.GufiBin, gufi.ToolSQLite3)) || len(cmd) == 0 {
		return false
	}
	for _, w := range cmd {
		if VtPath(w) == "" {
			return false
		}
	}
	probe := `CREATE VIRTUAL TABLE temp.p USING gufi_vt(index='/', E="SELECT name FROM vrpentries;", remote_cmd='` + nothing + `'); SELECT count(*) FROM temp.p;`
	t0 := time.Now()
	res, err := e.Local.Run(context.WithoutCancel(ctx), gufi.Command{Tool: gufi.ToolSQLite3, Args: []string{":memory:", probe}, Timeout: 15 * time.Second})
	e.noteLocal(ctx, []string{filepath.Join(e.GufiBin, gufi.ToolSQLite3), ":memory:", probe}, t0, res, err, gufitrace.Run{Engine: "gufi_sqlite3", Hide: true})
	return err == nil && res.ExitCode == 0 && strings.TrimSpace(string(res.Stdout)) == "0"
}

// noteLocal notes a run on this host as gufiTrace's execRun does.
func (e *Engine) noteLocal(ctx context.Context, argv []string, t0 time.Time, res gufi.Result, err error, note gufitrace.Run) {
	note.Start = t0
	if err != nil {
		note.Error = err.Error()
	} else if res.Signal != "" {
		note.Signal = res.Signal
	} else {
		c := res.ExitCode
		note.Code = &c
	}
	se := res.Stderr
	note.Stderr, note.Cut = &se, res.StderrCut
	gufitrace.Note(ctx, argv, note)
}

// vtRemote is a remote location's command inputs.
type vtRemote struct{ token, site, gufiBin string }

// vtLoc is one gufi_vt table: its index or indexes, and its plan.
type vtLoc struct {
	id      string
	label   string
	indexes []string
	plan    gufi.Plan
	remote  *vtRemote
	members []vtMember
}

type vtMember struct{ id, label, root, index string }

func viaStudio(r vtRemote) []string { return append(append([]string{}, vtCommand()...), r.token) }

// ViaShell is viaShell: the same location as a person runs it from a
// terminal, over ssh or pw ssh, with the host's GUFI on the PATH there.
func (e *Engine) viaShell(r vtRemote) []string {
	out := append([]string{}, remote.ShellFor(r.site)...)
	if r.gufiBin != "" && !e.Pool.IsDirect(r.site) {
		out = append(out, "env", "PATH="+r.gufiBin+":$PATH")
	}
	return out
}

// vtScript is vtScript: one table per location, then the union.
func vtScript(locs []vtLoc, order *VtOrder, remoteCmd func(vtRemote) []string) (string, error) {
	val := func(k, v string) (string, error) {
		if v == "" {
			return "", nil
		}
		st, ok := VtStage(v)
		if !ok {
			return "", fmt.Errorf("its %s stage has a double quote outside a string, which gufi_vt cannot take", k)
		}
		return fmt.Sprintf(`, %s="%s"`, k, st), nil
	}
	q := func(v string) (string, error) {
		s := VtPath(v)
		if s == "" {
			return "", fmt.Errorf("%s cannot be passed to gufi_vt", v)
		}
		return s, nil
	}
	var tables, union []string
	for i, l := range locs {
		var stages strings.Builder
		for _, s := range stageList(l.plan.Stages) {
			v, err := val(s[0], s[1])
			if err != nil {
				return "", err
			}
			stages.WriteString(v)
		}
		rem := ""
		if l.remote != nil {
			cmd := remoteCmd(*l.remote)
			if len(cmd) > 0 {
				c, err := q(cmd[0])
				if err != nil {
					return "", err
				}
				rem = ", remote_cmd=" + c
				for _, a := range cmd[1:] {
					qa, err := q(a)
					if err != nil {
						return "", err
					}
					rem += ", remote_arg=" + qa
				}
			}
		}
		var idx []string
		for _, ix := range l.indexes {
			qi, err := q(ix)
			if err != nil {
				return "", err
			}
			idx = append(idx, "index="+qi)
		}
		tables = append(tables, fmt.Sprintf("CREATE VIRTUAL TABLE temp.loc_%d USING gufi_vt(%s, threads=%d%s%s);", i, strings.Join(idx, ", "), vtThreads, stages.String(), rem))
		union = append(union, fmt.Sprintf("SELECT '%s', * FROM temp.loc_%d", l.id, i))
	}
	tail := ""
	if order != nil {
		dir := "ASC"
		if order.Desc {
			dir = "DESC"
		}
		tail = fmt.Sprintf(" ORDER BY %d %s LIMIT %d", order.Col+2, dir, order.Limit)
	}
	return strings.Join(tables, "\n") + "\n" + strings.Join(union, " UNION ALL ") + tail + ";", nil
}

// VtURL is where the subcommand reaches this server; Of sets it.
func (e *Engine) vtURL() string {
	if e.VtURL != nil {
		return e.VtURL()
	}
	return "http://127.0.0.1:4080/internal/gufi-vt"
}

// SelfVtURL is selfUrl for a server on host and port.
func SelfVtURL(host string, port int) string {
	h := host
	switch {
	case h == "0.0.0.0" || h == "::" || h == "":
		h = "127.0.0.1"
	case strings.Contains(h, ":"):
		h = "[" + h + "]"
	}
	return fmt.Sprintf("http://%s:%d/internal/gufi-vt", h, port)
}

func randomToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func errText(err error) string { return jsSlice(err.Error(), 300) }

// RunAcross is runAcross: each library's plan as one gufi_vt statement.
// Rows come back tagged with their library's id, paths relative to it.
func (e *Engine) RunAcross(ctx context.Context, items []VtItem, order *VtOrder) (VtResult, error) {
	t0 := time.Now()
	var mu sync.Mutex
	failed := []VtFailure{}
	fail := func(label string, err error) {
		mu.Lock()
		failed = append(failed, VtFailure{label, errText(err)})
		mu.Unlock()
	}
	var locs []vtLoc
	type group struct {
		site, key, gufiBin string
		plan               gufi.Plan
		members            []vtMember
	}
	groups := map[string]*group{}
	var groupOrder []string
	var wg sync.WaitGroup
	for _, it := range items {
		wg.Add(1)
		go func(l libraries.Library, plan gufi.Plan) {
			defer wg.Done()
			sub := strings.Trim(plan.Subtree, "/")
			err := func() error {
				for _, s := range strings.Split(sub, "/") {
					if s == ".." {
						return errors.New("scope escapes the index")
					}
				}
				if l.Remote() {
					t, err := e.Target(l.IndexRoot)
					if err != nil {
						return err
					}
					if t == nil {
						return errors.New("its owner has no key to act with")
					}
					index := t.IndexRoot
					if sub != "" {
						index = path.Join(t.IndexRoot, sub)
					}
					if !VtRemoteIndex(index) || (t.GufiBin != "" && VtPath(t.GufiBin) == "") {
						return errors.New("its index path cannot be passed to gufi_vt")
					}
					k := "lib:" + l.ID
					if !plan.Raw && e.env("STUDIO_STACK_REMOTE") != "0" {
						if host, err := e.RemoteHostKey(l.IndexRoot); err == nil {
							b, _ := json.Marshal([]any{host, plan.Stages, plan.Columns})
							k = string(b)
						}
					}
					mu.Lock()
					g := groups[k]
					if g == nil {
						g = &group{site: t.Site, key: t.Key, gufiBin: t.GufiBin, plan: plan}
						groups[k] = g
						groupOrder = append(groupOrder, k)
					}
					g.members = append(g.members, vtMember{l.ID, l.Label, t.IndexRoot, index})
					mu.Unlock()
					return nil
				}
				root, _ := filepath.Abs(l.IndexRoot)
				index := root
				if sub != "" {
					index = filepath.Join(root, sub)
				}
				if index != root && !strings.HasPrefix(index, root+"/") {
					return errors.New("scope escapes the index")
				}
				if VtPath(index) == "" {
					return errors.New("its index path cannot be passed to gufi_vt")
				}
				if !exists(filepath.Join(index, "db.db")) {
					return errors.New("index not built yet")
				}
				mu.Lock()
				locs = append(locs, vtLoc{id: l.ID, label: l.Label, indexes: []string{index}, plan: plan, members: []vtMember{{l.ID, l.Label, root, index}}})
				mu.Unlock()
				return nil
			}()
			if err != nil {
				fail(l.Label, err)
			}
		}(it.Lib, it.Plan)
	}
	wg.Wait()
	stacks := 0
	for _, k := range groupOrder {
		g := groups[k]
		wg.Add(1)
		go func(g *group) {
			defer wg.Done()
			answering := e.checkRemote(ctx, g.site, g.key, g.gufiBin, g.members, fail)
			if len(answering) == 0 {
				return
			}
			token := randomToken()
			var indexes, labels []string
			for _, m := range answering {
				indexes = append(indexes, m.index)
				labels = append(labels, m.label)
			}
			label := strings.Join(labels, ", ")
			vtMu.Lock()
			vtTokens[token] = &vtLocation{site: g.site, key: g.key, gufiBin: g.gufiBin, indexes: indexes, label: label, at: time.Now()}
			vtMu.Unlock()
			mu.Lock()
			id := answering[0].id
			if len(answering) > 1 {
				id = "stack" + strconv.Itoa(stacks)
				stacks++
			}
			locs = append(locs, vtLoc{id: id, label: label, indexes: indexes, plan: g.plan, members: answering, remote: &vtRemote{token, g.site, g.gufiBin}})
			mu.Unlock()
		}(g)
	}
	wg.Wait()
	// The union follows the libraries' order, not the order the checks ended in.
	at := func(id string) int {
		for i, it := range items {
			if it.Lib.ID == id {
				return i
			}
		}
		return -1
	}
	sort.SliceStable(locs, func(i, j int) bool { return at(locs[i].members[0].id) < at(locs[j].members[0].id) })
	if len(locs) == 0 {
		return VtResult{Rows: []VtRow{}, Failed: failed, Elapsed: time.Since(t0)}, nil
	}
	defer func() {
		vtMu.Lock()
		for _, l := range locs {
			if l.remote != nil {
				delete(vtTokens, l.remote.token)
			}
		}
		vtMu.Unlock()
	}()
	script, err := vtScript(locs, order, viaStudio)
	if err != nil {
		return VtResult{}, err
	}
	sqlite := filepath.Join(e.GufiBin, gufi.ToolSQLite3)
	t1 := time.Now()
	res, runErr := e.Local.Run(ctx, gufi.Command{Tool: gufi.ToolSQLite3, Args: []string{"-d", "x", ":memory:", script}, Timeout: 300 * time.Second,
		Env: []string{"STUDIO_GUFI_VT_URL=" + e.vtURL(), "STUDIO_GUFI_VT_SECRET=" + vtSecret()}})
	// Shown as a person would run it: gufi_vt's own remote form, then what
	// each host ran.
	shown, serr := vtScript(locs, order, e.viaShell)
	if serr != nil {
		shown = script
	}
	e.noteLocal(ctx, []string{sqlite, "-d", "x", ":memory:"}, t1, res, runErr, gufitrace.Run{Engine: "gufi_vt", Script: &shown})
	for _, l := range locs {
		if l.remote == nil {
			continue
		}
		vtMu.Lock()
		loc := vtTokens[l.remote.token]
		var ran *vtRan
		var lerr string
		if loc != nil {
			ran, lerr = loc.ran, loc.err
		}
		vtMu.Unlock()
		if ran != nil {
			gufitrace.Note(ctx, ran.argv, ran.note)
		}
		if lerr != "" {
			for _, m := range l.members {
				failed = append(failed, VtFailure{m.label, lerr})
			}
		}
	}
	if runErr == nil && (res.ExitCode != 0 || res.Signal != "") {
		why := res.Stderr
		if why == "" {
			why = fmt.Sprintf("gufi_sqlite3 exited %d", res.ExitCode)
		}
		runErr = errors.New(why)
	}
	if runErr != nil {
		lines := strings.Split(strings.TrimSpace(runErr.Error()), "\n")
		if len(lines) > 2 {
			lines = lines[len(lines)-2:]
		}
		return VtResult{}, errors.New(jsSlice(strings.Join(lines, " "), 300))
	}
	byID := map[string]vtLoc{}
	for _, l := range locs {
		byID[l.id] = l
	}
	rows := []VtRow{}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if line == "" {
			continue
		}
		cells := strings.Split(line, "\x1e")
		row := cells[1:]
		loc, ok := byID[cells[0]]
		if !ok {
			rows = append(rows, VtRow{cells[0], row})
			continue
		}
		member := loc.members[0]
		if len(loc.members) > 1 {
			roots := append([]vtMember{}, loc.members...)
			sort.SliceStable(roots, func(i, j int) bool { return len(roots[i].root) > len(roots[j].root) })
		find:
			for _, m := range roots {
				for _, c := range row {
					if c == m.root || strings.HasPrefix(c, m.root+"/") {
						member = m
						break find
					}
				}
			}
		}
		prefix := member.root + "/"
		out := make([]string, len(row))
		for i, c := range row {
			out[i] = strings.TrimPrefix(c, prefix)
		}
		rows = append(rows, VtRow{member.id, out})
	}
	return VtResult{Rows: rows, Failed: failed, Elapsed: time.Since(t0)}, nil
}

// checkRemote is checkRemote: the members of a remote group whose index
// answers, checked with gufi_query alone, one row per root, in one remote
// command for the whole group; when fewer rows come back, each is asked
// on its own to find which.
func (e *Engine) checkRemote(ctx context.Context, site, key, gufiBin string, members []vtMember, fail func(string, error)) []vtMember {
	ask := func(indexes []string) (remote.Result, error) {
		argv := append([]string{libraries.GufiTool(gufiBin, ""), "-n", "1", "-d", "|", "--min-level", "0", "--max-level", "0", "-S", "SELECT 1;"}, indexes...)
		return e.RemoteGufi(ctx, site, key, argv, GufiOpts{Timeout: 60 * time.Second, Hide: true})
	}
	lines := func(s string) int {
		n := 0
		for _, l := range strings.Split(s, "\n") {
			if strings.TrimSpace(l) != "" {
				n++
			}
		}
		return n
	}
	why := func(r remote.Result) string {
		var keep []string
		for _, l := range strings.Split(strings.TrimSpace(r.Stderr), "\n") {
			if l != "" {
				keep = append(keep, l)
			}
		}
		if len(keep) > 0 {
			return keep[len(keep)-1]
		}
		return fmt.Sprintf("the index did not answer (exit %d)", r.Code)
	}
	if len(members) > 1 {
		var idx []string
		for _, m := range members {
			idx = append(idx, m.index)
		}
		r, err := ask(idx)
		if err != nil {
			for _, m := range members {
				fail(m.label, err)
			}
			return nil
		}
		if lines(r.Stdout) >= len(members) {
			return members
		}
	}
	ok := make([]bool, len(members))
	var wg sync.WaitGroup
	for i, m := range members {
		wg.Add(1)
		go func(i int, m vtMember) {
			defer wg.Done()
			r, err := ask([]string{m.index})
			if err != nil {
				fail(m.label, err)
				return
			}
			if lines(r.Stdout) > 0 {
				ok[i] = true
				return
			}
			fail(m.label, errors.New(why(r)))
		}(i, m)
	}
	wg.Wait()
	var out []vtMember
	for i, m := range members {
		if ok[i] {
			out = append(out, m)
		}
	}
	return out
}

var vtStageFlag = regexp.MustCompile(`^-[IEKJG]$`)

// CheckRemoteArgs is checkRemoteArgs: the gufi_query arguments gufi_vt
// sends, and nothing else: the stage SQL, its fixed flags, at most
// vtThreads threads, then the location's indexes, joined back where a
// space split them. Nil when args are anything else.
func CheckRemoteArgs(args []string, roots []string) []string {
	if len(args) == 0 || args[0] != "gufi_query" {
		return nil
	}
	i := 1
	for ; i < len(args); i++ {
		a := args[i]
		if a == "--print-tlv" || a == "-x" {
			continue
		}
		if a == "--threads" {
			i++
			if i >= len(args) {
				return nil
			}
			n, err := strconv.ParseFloat(args[i], 64)
			if err != nil || !(n >= 1 && n <= vtThreads) {
				return nil
			}
			continue
		}
		if vtStageFlag.MatchString(a) && i+1 < len(args) {
			i++
			continue
		}
		break
	}
	if i >= len(args) || strings.Join(args[i:], " ") != strings.Join(roots, " ") {
		return nil
	}
	return append(append([]string{}, args[1:i]...), roots...)
}

// AddVtLocationForTests adds a remote location the route answers for.
func AddVtLocationForTests(token, site, key, gufiBin string, indexes []string, label string) {
	vtMu.Lock()
	vtTokens[token] = &vtLocation{site: site, key: key, gufiBin: gufiBin, indexes: indexes, label: label, at: time.Now()}
	vtMu.Unlock()
}

// VtSecretForTests is the secret the subcommand presents.
func VtSecretForTests() string { return vtSecret() }

func textReply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

// ServeGufiVt is POST /internal/gufi-vt: the subcommand's way in. The
// per-process secret admits it; the path is outside /api, so sign-in does
// not apply.
func (e *Engine) ServeGufiVt(w http.ResponseWriter, r *http.Request) {
	given := []byte(r.Header.Get("X-Studio-Secret"))
	if len(given) == 0 || subtle.ConstantTimeCompare(given, []byte(vtSecret())) != 1 {
		textReply(w, 403, "forbidden")
		return
	}
	var body struct {
		Token any `json:"token"`
		Args  any `json:"args"`
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	json.Unmarshal(raw, &body)
	token, _ := body.Token.(string)
	vtMu.Lock()
	loc := vtTokens[token]
	vtMu.Unlock()
	if loc == nil || time.Since(loc.at) > vtTokenTTL {
		textReply(w, 404, "unknown location")
		return
	}
	var args []string
	list, isList := body.Args.([]any)
	for _, a := range list {
		s, ok := a.(string)
		if !ok {
			isList = false
			break
		}
		args = append(args, s)
	}
	var rest []string
	if isList {
		rest = CheckRemoteArgs(args, loc.indexes)
	}
	if rest == nil {
		textReply(w, 400, "not a gufi_query on this location")
		return
	}
	argv := append([]string{libraries.GufiTool(loc.gufiBin, "")}, rest...)
	t0 := time.Now()
	res, err := e.Pool.Run(r.Context(), loc.site, loc.key, argv, 300*time.Second)
	note := gufitrace.Run{Start: t0, SiteName: remote.SiteName(loc.site), ShellLine: strings.Join(remote.ShellFor(loc.site), " "), Elapsed: time.Since(t0)}
	if err != nil {
		note.Error = err.Error()
		res = remote.Result{Code: -1, Stderr: err.Error()}
	} else {
		c := res.Code
		se := res.Stderr
		note.Code, note.Stderr, note.Cut = &c, &se, res.StderrCut
	}
	vtMu.Lock()
	loc.ran = &vtRan{argv: argv, note: note}
	if res.Code != 0 && len(res.Raw) == 0 {
		lines := strings.Split(strings.TrimSpace(res.Stderr), "\n")
		why := lines[len(lines)-1]
		if why == "" {
			why = fmt.Sprintf("exit %d", res.Code)
		}
		loc.err = jsSlice(why, 300)
	}
	vtMu.Unlock()
	w.Header().Set("Content-Type", "application/octet-stream")
	if res.Stderr != "" {
		se := res.Stderr
		if n := len(se); n > 2000 {
			se = se[n-2000:]
		}
		w.Header().Set("X-Stderr", base64.StdEncoding.EncodeToString([]byte(se)))
	}
	w.WriteHeader(200)
	w.Write(res.Raw)
}

// SplitCommandLine is splitCommandLine: a gufi_vt command line as
// arguments, double- or single-quoted values taken literally, other words
// split on spaces, nothing expanded.
func SplitCommandLine(line string) ([]string, error) {
	var out []string
	i := 0
	for i < len(line) {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		if i >= len(line) {
			break
		}
		if q := line[i]; q == '"' || q == '\'' {
			end := strings.IndexByte(line[i+1:], q)
			if end < 0 {
				return nil, errors.New("unterminated quote in the gufi_vt command line")
			}
			out = append(out, line[i+1:i+1+end])
			i += end + 2
			continue
		}
		end := strings.IndexByte(line[i:], ' ')
		if end < 0 {
			end = len(line) - i
		}
		out = append(out, line[i:i+end])
		i += end
	}
	return out, nil
}

// VtRemoteMain is the gufi-vt-remote subcommand (gufiVtRemote.ts main):
// `<token> "<gufi_query command line>"`, with STUDIO_GUFI_VT_URL and
// STUDIO_GUFI_VT_SECRET in the environment. It exits 0 even when the
// location fails, writing why to stderr: a nonzero exit with no rows fails
// the whole gufi_vt statement, and the server already knows which location
// failed.
func VtRemoteMain(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	url, secret := getenv("STUDIO_GUFI_VT_URL"), getenv("STUDIO_GUFI_VT_SECRET")
	if len(args) < 2 || args[0] == "" || args[1] == "" || url == "" || secret == "" {
		fmt.Fprint(stderr, "gufi_vt remote: missing token, command line, or Studio address\n")
		return 0
	}
	argv, err := SplitCommandLine(args[1])
	if err != nil {
		fmt.Fprintf(stderr, "gufi_vt remote: %s\n", err)
		return 0
	}
	timeout := 300 * time.Second
	if n, err := strconv.ParseFloat(getenv("STUDIO_GUFI_VT_TIMEOUT_MS"), 64); err == nil && n > 0 {
		timeout = time.Duration(n) * time.Millisecond
	}
	body, _ := json.Marshal(map[string]any{"token": args[0], "args": argv})
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(body)))
	if err != nil {
		fmt.Fprintf(stderr, "gufi_vt remote: %s\n", err)
		return 0
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Studio-Secret", secret)
	req.Header.Set(bridge.Header, secret)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "gufi_vt remote: %s\n", err)
		return 0
	}
	defer res.Body.Close()
	got, err := io.ReadAll(res.Body)
	if err != nil {
		fmt.Fprintf(stderr, "gufi_vt remote: %s\n", err)
		return 0
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg := []rune(string(got))
		if len(msg) > 500 {
			msg = msg[:500]
		}
		fmt.Fprintf(stderr, "gufi_vt remote: %s\n", string(msg))
		return 0
	}
	if h := res.Header.Get("X-Stderr"); h != "" {
		if b, err := base64.StdEncoding.DecodeString(h); err == nil {
			stderr.Write(b)
		}
	}
	stdout.Write(got)
	return 0
}
