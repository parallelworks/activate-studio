package main

// The Studio's server as a child of the shell: started on 127.0.0.1, waited
// for until GET /api/version answers, started again when it asks for it
// (exit 75 after a settings change, exit 76 after an update), and reported
// as failed for any other exit, until Retry. Stop ends the server and every
// process it started: its process group on Linux and macOS, its Job Object
// on Windows (proc_unix.go, proc_windows.go).
//
// With a payload tree (the Linux app) the server is started through the
// tree's own run script, studio-run.sh --once, which sets up the payload's
// Node, Python, GUFI and tesseract, the login shell's PATH and the models
// exactly as the run loop does without a window; the shell does the
// restarting itself. Without a tree (a checkout, and Windows) it starts
// node directly.
//
// With GoServer set (STUDIO_GO_SERVER=1, see goServerPaths) the server is
// the Go front server (server-go/cmd/studio-server, docs/GO-PORT.md), which
// starts Node behind it on a loopback port (STUDIO_NODE_SPAWN=1) and passes
// on Node's exit status, so 75 and 76 work as before. With a tree the run
// script starts it (STUDIO_GO_SERVER_BIN); without one the supervisor does.
// SemanticMap, when set, is given to the server as STUDIO_SEMANTIC_MAP_BIN.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	exitRestart  = 75 // a setting changed; start again (deploy/mac/studio-run.sh)
	exitRelaunch = 76 // an update replaced the app; start again and reload
)

// ServerEvent is what the supervisor tells the window.
type ServerEvent struct {
	Kind    string // "starting", "ready", "failed"
	Port    int
	Reason  string // why it (re)started: "launch", "exit-75", "exit-76", "relaunch", "retry"
	Message string // for "failed": the end of the server's log
}

type Supervisor struct {
	RunScript   string // the payload's studio-run.sh; when set the server starts through it
	Node        string // the node binary (no RunScript)
	Script      string // server/dist/main.js (no RunScript)
	GoServer    string // studio-server, the Go front server; empty: Node alone
	SemanticMap string // the Go semantic-map binary, for STUDIO_SEMANTIC_MAP_BIN; empty: the server's default
	AppDir      string // the Studio checkout or bundle (the server's working directory)
	DataDir     string // per-user data: studio.env, index, logs
	KBRoot      string // the documents folder
	Events      func(ServerEvent)
	Pause       time.Duration // the wait before starting again after an update's stop (default 2 s; tests shorten it)

	mu       sync.Mutex
	child    *serverProcess
	port     int
	stopping bool
	restart  string // a restart the shell asked for, and its reason
	retry    chan string
	starts   int
	stops    []time.Time // stops of the server shortly after an update, for the rollback
}

// Port is the port the server was last started on.
func (s *Supervisor) Port() int { s.mu.Lock(); defer s.mu.Unlock(); return s.port }

// Run starts the server and keeps it running until Stop. Call once, in its
// own goroutine.
func (s *Supervisor) Run() {
	s.mu.Lock()
	s.retry = make(chan string, 1)
	s.mu.Unlock()
	reason := "launch"
	for {
		s.recoverTree()
		startedAt := time.Now()
		code, err := s.runOnce(reason)
		s.mu.Lock()
		stopping := s.stopping
		asked := s.restart
		s.restart = ""
		s.mu.Unlock()
		if stopping {
			return
		}
		switch {
		case asked != "":
			reason = asked
		case err == nil && code == exitRestart:
			reason = "exit-75"
		case err == nil && code == exitRelaunch:
			reason = "exit-76"
		case s.RunScript != "" && err == nil && code != exitRestart && s.pendingUpdate():
			// Just after an update (updates/pending.json, written by the
			// server when it swaps the new payload in): as the run loop
			// does, start it again quietly, and after three stops within
			// two minutes put the previous payload back.
			if s.afterUpdateStop(startedAt) {
				reason = "rollback"
			} else {
				reason = "after-update"
			}
		default:
			msg := fmt.Sprintf("The Studio's server stopped (exit status %d).", code)
			if err != nil {
				msg = "The Studio's server did not start: " + err.Error()
			}
			if tail := logTail(s.logFile(), 6); tail != "" {
				msg += "\n\n" + tail
			}
			s.emit(ServerEvent{Kind: "failed", Message: msg})
			// Wait for Retry (or Stop, which closes the channel).
			r, ok := <-s.retry
			if !ok {
				return
			}
			reason = r
		}
	}
}

// recoverTree finishes or undoes an update that was cut off between its two
// directory moves, which leaves the payload at no path (studio-recover.sh,
// in the payload: see docs/LINUX-APP.md, Updates). It runs before every
// start of the server, and does nothing when the payload is there and the
// server left no swap file. The script is run from the payload, or, when
// that is gone, from the staged or the previous copy beside it.
func (s *Supervisor) recoverTree() {
	if s.RunScript == "" {
		return
	}
	if _, err := os.Stat(s.RunScript); err == nil {
		if _, err := os.Stat(filepath.Join(s.DataDir, "updates", "swap.json")); err != nil {
			return
		}
	}
	tree := filepath.Dir(s.RunScript)
	beside := filepath.Join(filepath.Dir(tree), "."+filepath.Base(tree))
	for _, dir := range []string{tree, beside + ".new", beside + ".previous"} {
		script := filepath.Join(dir, "studio-recover.sh")
		if _, err := os.Stat(script); err != nil {
			continue
		}
		_ = exec.Command("/bin/bash", script, tree, s.DataDir).Run()
		return
	}
	// A payload that predates the script: put the previous one back.
	if _, err := os.Stat(s.RunScript); err != nil {
		if _, err := os.Stat(beside + ".previous"); err == nil {
			_ = os.Rename(beside+".previous", tree)
		}
	}
}

func (s *Supervisor) pendingFile() string { return filepath.Join(s.DataDir, "updates", "pending.json") }

func (s *Supervisor) pendingUpdate() bool {
	_, err := os.Stat(s.pendingFile())
	return err == nil
}

// afterUpdateStop handles a stop of the server while an update is pending
// (the rules of studio-run.sh's loop). It reports whether the previous
// payload was put back.
func (s *Supervisor) afterUpdateStop(started time.Time) (rolledBack bool) {
	now := time.Now()
	if now.Sub(started) > 60*time.Second {
		// It stayed up a minute: the update is good.
		_ = os.Remove(s.pendingFile())
		s.stops = nil
		return false
	}
	recent := []time.Time{now}
	for _, t := range s.stops {
		if now.Sub(t) <= 2*time.Minute {
			recent = append(recent, t)
		}
	}
	s.stops = recent
	if len(recent) >= 3 {
		s.stops = nil
		_ = exec.Command("/bin/bash", s.RunScript, "--rollback", s.DataDir, strconv.Itoa(len(recent))).Run()
		return true
	}
	time.Sleep(s.afterUpdatePause())
	return false
}

func (s *Supervisor) afterUpdatePause() time.Duration {
	if s.Pause != 0 {
		return s.Pause
	}
	return 2 * time.Second
}

// Restart stops the running server and starts it again (the page's
// relaunch request). The window reloads once it answers.
func (s *Supervisor) Restart(reason string) {
	s.mu.Lock()
	s.restart = reason
	c := s.child
	s.mu.Unlock()
	if c != nil {
		c.stop(5 * time.Second)
	}
}

// Retry starts the server again after it failed.
func (s *Supervisor) Retry() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retry != nil && !s.stopping {
		select {
		case s.retry <- "retry":
		default:
		}
	}
}

// Stop ends the server and everything it started, and the loop with it.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return
	}
	s.stopping = true
	c := s.child
	if s.retry != nil {
		close(s.retry)
	}
	s.mu.Unlock()
	if c != nil {
		c.stop(5 * time.Second)
	}
}

func (s *Supervisor) emit(e ServerEvent) {
	if s.Events != nil {
		s.Events(e)
	}
}

func (s *Supervisor) logFile() string { return filepath.Join(s.DataDir, "logs", "studio.log") }

// runOnce starts one server, reports it ready, and waits for it to exit.
func (s *Supervisor) runOnce(reason string) (int, error) {
	settings := readSettings(filepath.Join(s.DataDir, "studio.env"))
	port := s.choosePort(settings)
	s.emit(ServerEvent{Kind: "starting", Port: port, Reason: reason})

	if err := os.MkdirAll(filepath.Join(s.DataDir, "logs"), 0o755); err != nil {
		return -1, err
	}
	log, err := os.OpenFile(s.logFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return -1, err
	}
	defer log.Close()
	fmt.Fprintf(log, "%s shell: starting the server on http://127.0.0.1:%d/ (%s)\n", time.Now().Format(time.RFC3339), port, reason)

	cmd := s.command(port, settings)
	cmd.Stdout = log
	cmd.Stderr = log
	child, err := startServerProcess(cmd)
	if err != nil {
		return -1, err
	}
	s.mu.Lock()
	s.child = child
	s.port = port
	s.starts++
	s.mu.Unlock()

	var waitErr error
	go func() { waitErr = cmd.Wait(); child.exited() }()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	readyErr := waitForServer(ctx, port, child.done)
	cancel()
	if readyErr == nil {
		s.emit(ServerEvent{Kind: "ready", Port: port, Reason: reason})
	} else {
		fmt.Fprintf(log, "%s shell: %v\n", time.Now().Format(time.RFC3339), readyErr)
		child.stop(5 * time.Second)
	}
	<-child.done
	// Whatever the server left behind (a build it started, a helper) goes
	// with it, so a restart starts clean.
	child.killTree()
	child.close()
	s.mu.Lock()
	s.child = nil
	s.mu.Unlock()

	code := cmd.ProcessState.ExitCode()
	fmt.Fprintf(log, "%s shell: the server exited with status %d\n", time.Now().Format(time.RFC3339), code)
	if readyErr != nil && code == 0 {
		return -1, readyErr
	}
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		return code, waitErr
	}
	return code, nil
}

// command is the process that runs the server on port: the payload's run
// script for one start, or node itself.
func (s *Supervisor) command(port int, settings map[string]string) *exec.Cmd {
	if s.RunScript != "" {
		cmd := exec.Command("/bin/bash", s.RunScript, "--once", s.DataDir)
		cmd.Dir = s.DataDir
		// studio.env still wins over STUDIO_RUN_PORT, as choosePort expects.
		// Which server runs is the shell's choice, not an inherited variable's.
		cmd.Env = append(envWithout(os.Environ(), "STUDIO_GO_SERVER_BIN"), "STUDIO_RUN_PORT="+strconv.Itoa(port))
		if s.KBRoot != "" {
			cmd.Env = append(cmd.Env, "STUDIO_DEFAULT_KB="+s.KBRoot)
		}
		if s.GoServer != "" {
			cmd.Env = append(cmd.Env, "STUDIO_GO_SERVER_BIN="+s.GoServer)
			if s.SemanticMap != "" {
				cmd.Env = append(envWithout(cmd.Env, "STUDIO_SEMANTIC_MAP_BIN"), "STUDIO_SEMANTIC_MAP_BIN="+s.SemanticMap)
			}
		}
		return cmd
	}
	if s.GoServer != "" {
		cmd := exec.Command(s.GoServer)
		cmd.Dir = s.AppDir
		cmd.Env = append(s.serverEnv(port, settings),
			"STUDIO_NODE_SPAWN=1", "STUDIO_NODE_BIN="+s.Node, "STUDIO_NODE_MAIN="+s.Script, "STUDIO_PROJECT_ROOT="+s.AppDir)
		if s.SemanticMap != "" {
			cmd.Env = append(envWithout(cmd.Env, "STUDIO_SEMANTIC_MAP_BIN"), "STUDIO_SEMANTIC_MAP_BIN="+s.SemanticMap)
		}
		return cmd
	}
	cmd := exec.Command(s.Node, s.Script)
	cmd.Dir = s.AppDir
	cmd.Env = s.serverEnv(port, settings)
	return cmd
}

// envWithout is env less the variables named.
func envWithout(env []string, names ...string) []string {
	out := make([]string, 0, len(env))
next:
	for _, kv := range env {
		for _, n := range names {
			if strings.HasPrefix(kv, n+"=") {
				continue next
			}
		}
		out = append(out, kv)
	}
	return out
}

// goServerPaths: with STUDIO_GO_SERVER=1, the Go front server and the Go
// semantic map, from the tree (app/bin/) or a checkout (server-go/bin/), or
// STUDIO_GO_SERVER_BIN and STUDIO_SEMANTIC_MAP_BIN when set. Empty when the
// opt-in is off or studio-server is not there, which leaves Node alone.
func goServerPaths(getenv func(string) string, tree, appDir string) (server, semanticMap string) {
	if getenv("STUDIO_GO_SERVER") != "1" {
		return "", ""
	}
	dir := filepath.Join(appDir, "server-go", "bin")
	if tree != "" {
		dir = filepath.Join(tree, "app", "bin")
	}
	server = getenv("STUDIO_GO_SERVER_BIN")
	if server == "" {
		server = filepath.Join(dir, "studio-server")
	}
	if st, err := os.Stat(server); err != nil || st.IsDir() {
		return "", ""
	}
	semanticMap = getenv("STUDIO_SEMANTIC_MAP_BIN")
	if semanticMap == "" {
		semanticMap = filepath.Join(dir, "semantic-map")
	}
	if _, err := os.Stat(semanticMap); err != nil {
		semanticMap = ""
	}
	return server, semanticMap
}

// choosePort: the port studio.env names (Settings, Server saves one there),
// else the port used last, remembered in the data folder so the window's
// address (and with it the page's saved settings) stays the same from one
// launch to the next, if it is still free, else 4180 if free, else a free
// one.
func (s *Supervisor) choosePort(settings map[string]string) int {
	if p, err := strconv.Atoi(settings["PORT"]); err == nil && p >= 1024 && p <= 65535 {
		return p
	}
	file := filepath.Join(s.DataDir, "run", "server-port")
	s.mu.Lock()
	last := s.port
	s.mu.Unlock()
	if last == 0 {
		last = readPort(file)
	}
	for _, p := range []int{last, 4180} {
		if p != 0 && portFree(p) {
			writePort(file, p)
			return p
		}
	}
	p := freePort()
	writePort(file, p)
	return p
}

// readPort and writePort keep a port number in a small file.
func readPort(file string) int {
	b, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	p, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || p < 1024 || p > 65535 {
		return 0
	}
	return p
}

func writePort(file string, p int) {
	_ = os.MkdirAll(filepath.Dir(file), 0o755)
	_ = os.WriteFile(file, []byte(strconv.Itoa(p)+"\n"), 0o644)
}

// serverEnv is the environment deploy/mac/studio-run.sh gives the server,
// without the macOS parts (bundled Python, GUFI and OCR are out of scope
// here), then studio.env on top.
func (s *Supervisor) serverEnv(port int, settings map[string]string) []string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	delete(env, "KB_ROOT")
	env["INDEX_BASE"] = filepath.Join(s.DataDir, "index")
	env["NODE_ENV"] = "production"
	if _, set := env["AI_ENABLED"]; !set {
		env["AI_ENABLED"] = "0"
	}
	for k, v := range settings {
		env[k] = v
	}
	if env["KB_ROOT"] == "" {
		env["KB_ROOT"] = s.KBRoot
		_ = os.MkdirAll(s.KBRoot, 0o755)
	} else if _, set := env["KB_STARTER_DIRS"]; !set {
		// A folder the person chose holds their own material.
		env["KB_STARTER_DIRS"] = ""
	}
	// Not settings: loopback only, the restart and settings-file contract.
	env["PORT"] = strconv.Itoa(port)
	env["HOST"] = "127.0.0.1"
	env["STUDIO_CONFIG_FILE"] = filepath.Join(s.DataDir, "studio.env")
	env["STUDIO_RESTARTABLE"] = "1"
	env["STUDIO_DATA_DIR"] = s.DataDir
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// readSettings reads studio.env: KEY='value' lines, single-quoted as
// server/src/lifecycle.ts writes them.
func readSettings(file string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(file)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || k == "" || strings.HasPrefix(k, "#") {
			continue
		}
		if strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") && len(v) >= 2 {
			v = strings.ReplaceAll(v[1:len(v)-1], `'\''`, "'")
		}
		out[k] = v
	}
	return out
}

// waitForServer polls GET /api/version until it answers 200, the server
// exits, or the context ends.
func waitForServer(ctx context.Context, port int, exited <-chan struct{}) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/api/version", port)
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		if res, err := client.Get(url); err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-exited:
			return errors.New("the server exited before it answered")
		case <-ctx.Done():
			return errors.New("the server did not answer in time")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 4180
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func portFree(p int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// logTail is the last n non-empty lines of a file.
func logTail(file string, n int) string {
	b, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
