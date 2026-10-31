package main

// The Studio's server as a child of the shell: started on 127.0.0.1, waited
// for until GET /api/version answers, started again when it asks for it
// (exit 75 after a settings change, exit 76 after an update), and reported
// as failed for any other exit, until Retry. Stop ends the server and every
// process it started: its process group on Linux and macOS, its Job Object
// on Windows (proc_unix.go, proc_windows.go).

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
	Node    string   // the node binary
	Script  string   // server/dist/main.js
	AppDir  string   // the Studio checkout or bundle (the server's working directory)
	DataDir string   // per-user data: studio.env, index, logs
	KBRoot  string   // the documents folder
	Extra   []string // extra node arguments before the script (test mode's preload)
	Events  func(ServerEvent)

	mu       sync.Mutex
	child    *serverProcess
	port     int
	stopping bool
	restart  string // a restart the shell asked for, and its reason
	retry    chan string
	starts   int
	pids     []int
	exits    []int
}

// Port is the port the server was last started on.
func (s *Supervisor) Port() int { s.mu.Lock(); defer s.mu.Unlock(); return s.port }

// Stats for the self test: how many starts, which PIDs, which exit codes.
func (s *Supervisor) Stats() (starts int, pids []int, exits []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts, append([]int(nil), s.pids...), append([]int(nil), s.exits...)
}

// Run starts the server and keeps it running until Stop. Call once, in its
// own goroutine.
func (s *Supervisor) Run() {
	s.mu.Lock()
	s.retry = make(chan string, 1)
	s.mu.Unlock()
	reason := "launch"
	for {
		code, err := s.runOnce(reason)
		s.mu.Lock()
		stopping := s.stopping
		asked := s.restart
		s.restart = ""
		s.exits = append(s.exits, code)
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

	cmd := exec.Command(s.Node, append(append([]string{}, s.Extra...), s.Script)...)
	cmd.Dir = s.AppDir
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.Env = s.serverEnv(port, settings)
	child, err := startServerProcess(cmd)
	if err != nil {
		return -1, err
	}
	s.mu.Lock()
	s.child = child
	s.port = port
	s.starts++
	s.pids = append(s.pids, cmd.Process.Pid)
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

// choosePort: the port studio.env names (Settings, Server saves one there),
// else the port used last if it is still free, else a free one.
func (s *Supervisor) choosePort(settings map[string]string) int {
	if p, err := strconv.Atoi(settings["PORT"]); err == nil && p >= 1024 && p <= 65535 {
		return p
	}
	s.mu.Lock()
	last := s.port
	s.mu.Unlock()
	if last != 0 && portFree(last) {
		return last
	}
	return freePort()
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
