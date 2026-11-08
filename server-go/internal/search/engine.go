// Package search runs the Studio's searches and queries over its libraries:
// the one walk per index for names, folder paths, words and meaning
// (gufi.ts walkSearch, searchWordsAndNames, searchVector), the scope of
// several libraries fused by reciprocal rank (libraryScope.ts searchScope),
// queries on libraries on other systems over the session pool
// (userLibraries.ts remoteQuery), and the routes built on them: search,
// Query, library details and the topology view.
//
// Every GUFI run goes through a gufi.Runner: this host's for local
// indexes, and one per remote host, key and GUFI install for the rest.
package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/gufitrace"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// Delim separates gufi_query's columns.
const Delim = gufi.Delim

// Engine holds what a search needs.
type Engine struct {
	Reg       *libraries.Registry
	Keys      libraries.Keys
	Local     *gufi.Runner
	GufiBin   string
	GufiIndex string
	KBRoot    string
	IndexBase string
	Pool      *remote.Pool
	Remote    *remote.Runners
	Embed     Embedder
	// Tags are the files track's labels (internal/kb), beside each hit.
	Tags *kb.Tags
	Getenv    func(string) string
	// SearchEngine is Settings, General, "Search across libraries":
	// "gufi_vt" or the default.
	SearchEngine func() string
	Log          *slog.Logger
}

func (e *Engine) env(k string) string {
	if e.Getenv == nil {
		return os.Getenv(k)
	}
	return e.Getenv(k)
}

// localThreads is LOCAL_THREADS: one per core up to 16.
func localThreads() int { return min(max(runtime.NumCPU(), 1), 16) }

var unknownOptionRe = regexp.MustCompile(`(?i)unrecognized option|invalid option|unknown option`)

// errUnknownOption marks gufi_query not knowing an option: an older GUFI.
type errUnknownOption struct{ msg string }

func (e *errUnknownOption) Error() string { return e.msg }

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// runLocal is runQuery: gufi_query here through the runner, noted in the
// trace. A nonzero exit with rows is rows, unless its error says an
// option is unknown.
func (e *Engine) runLocal(ctx context.Context, args []string, timeout time.Duration) (string, error) {
	t0 := time.Now()
	res, err := e.Local.Run(ctx, gufi.Command{Tool: gufi.ToolQuery, Args: args, Timeout: timeout})
	argv := append([]string{filepath.Join(e.GufiBin, "gufi_query")}, args...)
	o := gufitrace.Run{Start: t0, Elapsed: res.Elapsed}
	if res.Signal != "" {
		o.Signal = res.Signal
	} else if err != nil && res.Elapsed == 0 {
		o.Error = err.Error()
	} else {
		c := res.ExitCode
		o.Code = &c
	}
	if res.Elapsed > 0 || res.Stderr != "" {
		se := res.Stderr
		o.Stderr, o.Cut = &se, res.StderrCut
	}
	gufitrace.Note(ctx, argv, o)
	stdout := string(res.Stdout)
	if err != nil && !errors.Is(err, gufi.ErrTimeout) && res.Elapsed == 0 {
		return "", err
	}
	if res.ExitCode != 0 || res.Signal != "" || err != nil {
		if stdout == "" || unknownOptionRe.MatchString(res.Stderr) {
			why := lastLine(res.Stderr)
			if why == "" {
				why = res.Signal
			}
			if why == "" {
				why = "exit " + strconv.Itoa(res.ExitCode)
			}
			if unknownOptionRe.MatchString(res.Stderr) {
				return "", &errUnknownOption{"gufi_query failed: " + why}
			}
			return "", fmt.Errorf("gufi_query failed: %s", why)
		}
	}
	return stdout, nil
}

// ParseRows is parseRows.
func ParseRows(stdout string, fields int) [][]string { return gufi.ParseRows(stdout, fields) }

// Target resolves a remote root (remoteTarget), marking a host found to
// run only gufi_query as direct.
func (e *Engine) Target(root string) (*libraries.Target, error) {
	t, err := e.Reg.Users().Target(root, e.Keys, remote.SSHSite)
	if err != nil || t == nil {
		return t, err
	}
	if t.Def.Shell != nil && !*t.Def.Shell {
		e.Pool.SetDirect(t.Site, true)
	}
	return t, nil
}

// GufiOpts are a remote GUFI command's options (remoteGufi's).
type GufiOpts struct {
	Timeout time.Duration
	Hide    bool
	Source  string
}

// RemoteGufi is remoteGufi: a GUFI command on another system, through that
// host's runner, noted as it ended or as its session failed.
func (e *Engine) RemoteGufi(ctx context.Context, site, key string, argv []string, o GufiOpts) (remote.Result, error) {
	t0 := time.Now()
	note := gufitrace.Run{Start: t0, SiteName: remote.SiteName(site), Hide: o.Hide, Source: o.Source}
	if sh, err := remote.ParseSSHSite(site); err == nil {
		_ = sh
		note.ShellLine = strings.Join(remote.ShellFor(site), " ")
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	// The tool's directory and name: the runner's executor puts them back together.
	bin, tool := path.Split(argv[0])
	bin = strings.TrimSuffix(bin, "/")
	var res gufi.Result
	var err error
	if tool == gufi.ToolQuery || tool == gufi.ToolSQLite3 {
		res, err = e.Remote.For(site, key, bin).Run(ctx, gufi.Command{Tool: tool, Args: argv[1:], Timeout: timeout})
	} else {
		var r remote.Result
		r, err = e.Pool.Run(ctx, site, key, argv, timeout)
		res = gufi.Result{Stdout: r.Raw, Stderr: r.Stderr, StderrCut: r.StderrCut, ExitCode: r.Code}
	}
	if err != nil {
		note.Error = err.Error()
		gufitrace.Note(ctx, argv, note)
		return remote.Result{}, err
	}
	c := res.ExitCode
	note.Code = &c
	se := res.Stderr
	note.Stderr, note.Cut = &se, res.StderrCut
	gufitrace.Note(ctx, argv, note)
	return remote.Result{Code: res.ExitCode, Stdout: string(res.Stdout), Raw: res.Stdout, Stderr: res.Stderr, StderrCut: res.StderrCut}, nil
}

var cleanSeg = regexp.MustCompile(`^[^\x00]+$`)

// CleanRel is cleanRel: a path under a remote root, checked.
func CleanRel(rel string) (string, error) {
	var parts []string
	for _, p := range strings.Split(rel, "/") {
		if p == "" || p == "." {
			continue
		}
		if p == ".." || !cleanSeg.MatchString(p) {
			return "", apierr.New(400, "invalid path")
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "/"), nil
}

func withThreads(args []string, n int) []string {
	for i, a := range args {
		if a == "-n" && i+1 < len(args) {
			out := append([]string{}, args...)
			out[i+1] = strconv.Itoa(n)
			return out
		}
	}
	return args
}

// RemoteQuery is remoteQuery: gufi_query on the library's host, with that
// host's threads in place of the -n asked for.
func (e *Engine) RemoteQuery(ctx context.Context, root string, args []string, subdir string) (string, error) {
	t, err := e.Target(root)
	if err != nil {
		return "", err
	}
	target := t.IndexRoot
	if subdir != "" {
		s, err := CleanRel(subdir)
		if err != nil {
			return "", err
		}
		target = path.Join(t.IndexRoot, s)
	}
	args = withThreads(args, e.Pool.Threads(ctx, t.Site, t.Key))
	r, err := e.RemoteGufi(ctx, t.Site, t.Key, append(append([]string{libraries.GufiTool(t.GufiBin, "")}, args...), target), GufiOpts{})
	if err != nil {
		return "", err
	}
	if r.Code != 0 && r.Stdout == "" {
		why := lastLine(r.Stderr)
		if why == "" {
			why = strconv.Itoa(r.Code)
		}
		return "", apierr.New(502, "gufi_query on %s failed: %s", remote.SiteName(t.Site), why)
	}
	return r.Stdout, nil
}

func (e *Engine) hostOf(site string) string {
	if r := e.Pool.ReachedKnown(site); r != nil {
		return r.Key
	}
	return site
}

// RemoteQueryMany is remoteQueryMany: one gufi_query over several of a
// host's indexes in one remote command.
func (e *Engine) RemoteQueryMany(ctx context.Context, roots, args []string) (string, error) {
	var ts []*libraries.Target
	for _, r := range roots {
		t, err := e.Target(r)
		if err != nil {
			return "", err
		}
		ts = append(ts, t)
	}
	t := ts[0]
	for _, x := range ts {
		if e.hostOf(x.Site) != e.hostOf(t.Site) || x.Key != t.Key || x.GufiBin != t.GufiBin {
			return "", apierr.New(400, "indexes on different hosts cannot share a query")
		}
	}
	args = withThreads(args, e.Pool.Threads(ctx, t.Site, t.Key))
	cmd := append([]string{libraries.GufiTool(t.GufiBin, "")}, args...)
	for _, x := range ts {
		cmd = append(cmd, x.IndexRoot)
	}
	r, err := e.RemoteGufi(ctx, t.Site, t.Key, cmd, GufiOpts{})
	if err != nil {
		return "", err
	}
	if r.Code != 0 && (r.Stdout == "" || unknownOptionRe.MatchString(r.Stderr)) {
		why := lastLine(r.Stderr)
		if why == "" {
			why = strconv.Itoa(r.Code)
		}
		msg := fmt.Sprintf("gufi_query on %s failed: %s", remote.SiteName(t.Site), why)
		if unknownOptionRe.MatchString(r.Stderr) {
			return "", &errUnknownOption{msg}
		}
		return "", apierr.New(502, "%s", msg)
	}
	return r.Stdout, nil
}

// RemoteHostKey is remoteHostKey: libraries that share it can share a query.
func (e *Engine) RemoteHostKey(root string) (string, error) {
	t, err := e.Target(root)
	if err != nil || t == nil {
		return "", err
	}
	if remote.IsSSH(t.Site) {
		e.Pool.ReachedHost(t.Site)
	}
	b, _ := marshalJS([]string{e.hostOf(t.Site), t.Key, t.GufiBin})
	return string(b), nil
}

// RemoteIndexRoot is remoteIndexRoot.
func (e *Engine) RemoteIndexRoot(root string) (string, error) {
	t, err := e.Target(root)
	if err != nil {
		return "", err
	}
	return t.IndexRoot, nil
}

// QueryPerDir is queryPerDir.
func (e *Engine) QueryPerDir(ctx context.Context, sql string, indexRoot, subdir string, threads int) ([][]string, error) {
	if indexRoot == "" {
		indexRoot = e.GufiIndex
	}
	if threads <= 0 {
		threads = localThreads()
	}
	args := []string{"-n", strconv.Itoa(threads), "-d", Delim, "-E", sql}
	if strings.HasPrefix(indexRoot, libraries.RemotePrefix) {
		out, err := e.RemoteQuery(ctx, indexRoot, args, subdir)
		if err != nil {
			return nil, err
		}
		return ParseRows(out, 1), nil
	}
	target := indexRoot
	if subdir != "" {
		target = filepath.Join(indexRoot, subdir)
	}
	out, err := e.runLocal(ctx, append(args, target), 60*time.Second)
	if err != nil {
		return nil, err
	}
	return ParseRows(out, 1), nil
}

// QueryAggregate is queryAggregate: the merge happens where the index is.
func (e *Engine) QueryAggregate(ctx context.Context, stages []string, indexRoot, subdir string) ([][]string, error) {
	if indexRoot == "" {
		indexRoot = e.GufiIndex
	}
	args := append([]string{"-n", strconv.Itoa(localThreads()), "-d", Delim, "-a", "1"}, stages...)
	if strings.HasPrefix(indexRoot, libraries.RemotePrefix) {
		out, err := e.RemoteQuery(ctx, indexRoot, args, subdir)
		if err != nil {
			return nil, err
		}
		return ParseRows(out, 1), nil
	}
	target := indexRoot
	if subdir != "" {
		target = filepath.Join(indexRoot, subdir)
	}
	out, err := e.runLocal(ctx, append(args, target), 60*time.Second)
	if err != nil {
		return nil, err
	}
	return ParseRows(out, 1), nil
}

// gufiInstalled is gufiInstalled.
func (e *Engine) gufiInstalled() bool {
	_, err := os.Stat(filepath.Join(e.GufiBin, "gufi_query"))
	return err == nil
}

// toKbRel is toKbRel: a printed path relative to the index root.
func (e *Engine) toKbRel(p, indexRoot string) string {
	if strings.HasPrefix(indexRoot, libraries.RemotePrefix) {
		idx, err := e.RemoteIndexRoot(indexRoot)
		if err == nil {
			if strings.HasPrefix(p, idx+"/") {
				return p[len(idx)+1:]
			}
			if p == idx {
				return ""
			}
		}
		return strings.TrimPrefix(p, "./")
	}
	idx, _ := filepath.Abs(indexRoot)
	a, _ := filepath.Abs(p)
	if strings.HasPrefix(a, idx+string(filepath.Separator)) {
		return a[len(idx)+1:]
	}
	if a == idx {
		return ""
	}
	return strings.TrimPrefix(p, "./")
}

// GufiInstalled is gufiInstalled.
func (e *Engine) GufiInstalled() bool { return e.gufiInstalled() }

// CheckFTS is fts5's own parser on expr, run in gufi_sqlite3: its message
// for a malformed expression, "" for a good one or no parser here.
func (e *Engine) CheckFTS(ctx context.Context, expr string) string {
	if e.Local == nil {
		return ""
	}
	res, err := e.Local.Run(ctx, gufi.Command{Tool: gufi.ToolSQLite3, Stdin: []byte("CREATE VIRTUAL TABLE t USING fts5(x);\nSELECT count(*) FROM t WHERE t MATCH " + q(expr) + ";\n"), Timeout: 10 * time.Second})
	if err != nil {
		return ""
	}
	se := strings.TrimSpace(res.Stderr)
	if se == "" {
		return ""
	}
	if strings.Contains(se, "no such module") {
		return ""
	}
	line := lastLine(se)
	// gufi_sqlite3 writes "Error: SQL error: <sqlite's message>".
	line = strings.TrimPrefix(line, "Error: ")
	return strings.TrimPrefix(line, "SQL error: ")
}
