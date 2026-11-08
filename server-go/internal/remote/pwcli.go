package remote

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
)

// FindPwCLI is config.ts's findPwCli: PW_CLI (a path is taken as it is,
// found or not), the location chosen in Model access
// (INDEX_BASE/pw-cli.json), PATH, then the usual folders; bare "pw" when
// there is none.
// TODO(access track): move to the package that owns PUT /api/access/cli/path.
func FindPwCLI(getenv func(string) string, indexBase string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	env := getenv("PW_CLI")
	if strings.Contains(env, "/") {
		return env
	}
	if env != "" {
		for _, d := range filepath.SplitList(getenv("PATH")) {
			if d != "" && runnable(filepath.Join(d, env)) {
				return filepath.Join(d, env)
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(indexBase, "pw-cli.json")); err == nil {
		var c struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(b, &c) == nil && c.Path != "" && runnable(c.Path) {
			return c.Path
		}
	}
	for _, d := range filepath.SplitList(getenv("PATH")) {
		if d != "" && runnable(filepath.Join(d, "pw")) {
			return filepath.Join(d, "pw")
		}
	}
	home, _ := os.UserHomeDir()
	for _, d := range []string{filepath.Join(home, ".local", "bin"), filepath.Join(home, "bin"), filepath.Join(home, "pw"), "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"} {
		if runnable(filepath.Join(d, "pw")) {
			return filepath.Join(d, "pw")
		}
	}
	if env != "" {
		return env
	}
	return "pw"
}

// PwCLIFound reports whether FindPwCLI found a program.
func PwCLIFound(getenv func(string) string, indexBase string) bool {
	return runnable(FindPwCLI(getenv, indexBase))
}

func runnable(f string) bool {
	st, err := os.Stat(f)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

// Executor runs GUFI's tools on a remote host through the pool, behind the
// same interface as gufi.LocalExecutor, so the gufi.Runner's admission
// covers remote runs too. Bin is GUFI's bin directory there, "" for the
// tool from the login's PATH.
type Executor struct {
	Pool *Pool
	Site string
	Key  string
	Bin  string
}

// Exec runs c's tool with its arguments there. Stdin is not carried by
// the session loop; a command with input goes as its own call.
func (e *Executor) Exec(ctx context.Context, c gufi.Command) (gufi.Result, error) {
	tool := c.Tool
	if e.Bin != "" {
		tool = e.Bin + "/" + c.Tool
	}
	argv := append([]string{tool}, c.Args...)
	var r Result
	var err error
	if c.Stdin != nil {
		r, err = e.Pool.RunOnce(ctx, e.Site, e.Key, argv, c.Timeout, strings.NewReader(string(c.Stdin)))
	} else {
		r, err = e.Pool.Run(ctx, e.Site, e.Key, argv, c.Timeout)
	}
	if err != nil {
		return gufi.Result{}, err
	}
	return gufi.Result{Stdout: r.Raw, Stderr: r.Stderr, StderrCut: r.StderrCut, ExitCode: r.Code}, nil
}

// Runners admits remote GUFI runs per host, key and GUFI install: each
// gets a gufi.Runner over an Executor there, with as many slots as the
// pool opens sessions to a host, so a host is never sent more at once than
// it has sessions for. Threads are left to the caller (Pool.Threads).
type Runners struct {
	Pool    *Pool
	Options gufi.RunnerOptions

	mu sync.Mutex
	m  map[string]*gufi.Runner
}

// For is the runner for one host, key and bin directory.
func (rs *Runners) For(site, key, bin string) *gufi.Runner {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.m == nil {
		rs.m = map[string]*gufi.Runner{}
	}
	id := channelID(site, key) + "|" + bin
	r := rs.m[id]
	if r == nil {
		o := rs.Options
		o.Threads = 0
		if o.Concurrency <= 0 {
			o.Concurrency = rs.Pool.poolMax
		}
		r = gufi.NewRunner(&Executor{Pool: rs.Pool, Site: site, Key: key, Bin: bin}, nil, o)
		rs.m[id] = r
	}
	return r
}
