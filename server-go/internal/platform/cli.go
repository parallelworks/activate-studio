package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// NoCLIMessage is config.ts's NO_CLI_MESSAGE.
const NoCLIMessage = "The Studio cannot find the pw CLI, so platform actions (workflows, clusters, status) cannot run from here. " +
	"Install it, or choose where it is under Settings, Model access, pw CLI; in a container, bind the CLI and its credentials in, or set PW_CLI to its path."

// CLI sources, as PwCliSource.
const (
	SourceEnv    = "env"
	SourceChosen = "chosen"
	SourcePath   = "path"
	SourceCommon = "common"
	SourceNone   = "none"
)

func (p *Platform) cliChoiceFile() string { return filepath.Join(p.indexBase, "pw-cli.json") }

// CommonCLIDirs is pwCliCommonDirs.
func (p *Platform) CommonCLIDirs() []string {
	h := p.home
	return []string{filepath.Join(h, ".local", "bin"), filepath.Join(h, "bin"), filepath.Join(h, "pw"), "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"}
}

func runnableFile(f string) bool {
	st, err := os.Stat(f)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	return syscall.Access(f, 0x1) == nil
}

// ChosenCLI is chosenPwCli: the location chosen in Model access, or "".
func (p *Platform) ChosenCLI() string {
	b, err := os.ReadFile(p.cliChoiceFile())
	if err != nil {
		return ""
	}
	var v struct {
		Path any `json:"path"`
	}
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	s, _ := v.Path.(string)
	return s
}

// FindCLI is findPwCli: look for the pw CLI again and keep where it is.
// It returns the path (empty when not found) and the source.
func (p *Platform) FindCLI() (string, string) {
	set := func(path, source string) (string, string) {
		p.mu.Lock()
		p.cliPath, p.cliSource = path, source
		p.mu.Unlock()
		return path, source
	}
	pathDirs := filepath.SplitList(p.Getenv("PATH"))
	env := p.Getenv("PW_CLI")
	if env != "" && strings.Contains(env, "/") {
		// A path the deployment set is the CLI it means, found or not.
		if runnableFile(env) {
			return set(env, SourceEnv)
		}
		set(env, SourceNone)
		return "", SourceNone
	} else if env != "" {
		for _, d := range pathDirs {
			if d != "" && runnableFile(filepath.Join(d, env)) {
				return set(filepath.Join(d, env), SourceEnv)
			}
		}
	}
	if c := p.ChosenCLI(); c != "" && runnableFile(c) {
		return set(c, SourceChosen)
	}
	for _, d := range pathDirs {
		if d != "" && runnableFile(filepath.Join(d, "pw")) {
			return set(filepath.Join(d, "pw"), SourcePath)
		}
	}
	for _, d := range p.CommonCLIDirs() {
		if runnableFile(filepath.Join(d, "pw")) {
			return set(filepath.Join(d, "pw"), SourceCommon)
		}
	}
	if env == "" {
		env = "pw"
	}
	set(env, SourceNone)
	return "", SourceNone
}

// CLI is PW_CLI: what the Studio runs as the pw CLI.
func (p *Platform) CLI() string { p.mu.Lock(); defer p.mu.Unlock(); return p.cliPath }

// CLISource is PW_CLI_SOURCE.
func (p *Platform) CLISource() string { p.mu.Lock(); defer p.mu.Unlock(); return p.cliSource }

// ChooseCLI is choosePwCli: keep a location for the CLI, or forget it ("").
func (p *Platform) ChooseCLI(path string) (string, string, error) {
	if path == "" {
		os.Remove(p.cliChoiceFile())
	} else {
		if !filepath.IsAbs(path) {
			return "", "", errors.New("The location has to be a full path, such as /Users/me/bin/pw.")
		}
		if !runnableFile(path) {
			return "", "", errors.New("There is no program at " + path + ", or it cannot be run.")
		}
		if err := os.MkdirAll(filepath.Dir(p.cliChoiceFile()), 0o755); err != nil {
			return "", "", err
		}
		b, _ := json.Marshal(map[string]string{"path": path})
		if err := os.WriteFile(p.cliChoiceFile(), b, 0o644); err != nil {
			return "", "", err
		}
	}
	a, b := p.FindCLI()
	p.mu.Lock()
	p.cliInfo = nil
	p.mu.Unlock()
	if err := p.AccessChanged(false); err != nil {
		p.log.Warn("pw CLI location: Node did not answer", "err", err.Error())
	}
	return a, b, nil
}

// ExecOptions are execFile's options as the Studio passes them.
type ExecOptions struct {
	// Timeout kills the CLI (SIGTERM, as execFile) after this long; 0 is none.
	Timeout time.Duration
	// MaxBuffer bounds stdout and stderr each, as execFile's maxBuffer
	// (default 1 MiB).
	MaxBuffer int
	// Env is set over the environment Environ gives; "" removes a variable.
	Env map[string]string
	// Dir is the working directory.
	Dir string
	// Stdin, when set, is written to the CLI's standard input.
	Stdin []byte
}

// ExecError is a CLI run that failed, with what execFile's callback gets.
type ExecError struct {
	Stdout, Stderr string
	// Code is the exit code, or -1 when the CLI was killed or did not start.
	Code int
	// Killed: the timeout ended it.
	Killed bool
	// Missing: the program is not there (ENOENT).
	Missing bool
	// MaxBuffer: its output passed MaxBuffer.
	MaxBuffer bool
	msg       string
}

// Error is Node's err.message for the same failure.
func (e *ExecError) Error() string { return e.msg }

// Text is `stderr || err.message`, how most callers report a failure.
func (e *ExecError) Text() string {
	if e.Stderr != "" {
		return e.Stderr
	}
	return e.msg
}

// IsMissing reports whether err is the CLI not being found (isMissingCli).
func IsMissing(err error) bool {
	var e *ExecError
	return errors.As(err, &e) && e.Missing
}

// ErrText is `stderr || err.message` for a CLI failure, else err's text.
func ErrText(err error) string {
	var e *ExecError
	if errors.As(err, &e) {
		return e.Text()
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

// limited is a buffer that records whether it overflowed.
type limited struct {
	buf  bytes.Buffer
	max  int
	over bool
	stop func()
}

func (l *limited) Write(b []byte) (int, error) {
	if l.over {
		return len(b), nil
	}
	if l.buf.Len()+len(b) > l.max {
		l.buf.Write(b[:l.max-l.buf.Len()])
		l.over = true
		if l.stop != nil {
			l.stop()
		}
		return len(b), nil
	}
	return l.buf.Write(b)
}

// Exec runs the pw CLI with args as execFile(PW_CLI, args, ...) does and
// returns its stdout; a failure is an *ExecError.
func (p *Platform) Exec(ctx context.Context, args []string, o ExecOptions) (string, error) {
	return p.ExecFile(ctx, p.CLI(), args, o)
}

// ExecFile runs file as execFile does, with the platform's environment.
func (p *Platform) ExecFile(ctx context.Context, file string, args []string, o ExecOptions) (string, error) {
	if o.MaxBuffer <= 0 {
		o.MaxBuffer = 1024 * 1024
	}
	cmdline := strings.Join(append([]string{file}, args...), " ")
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	var timer *time.Timer
	var killed atomic.Bool
	cmd := exec.Command(file, args...)
	cmd.Env = p.Environ(o.Env)
	cmd.Dir = o.Dir
	if o.Stdin != nil {
		cmd.Stdin = bytes.NewReader(o.Stdin)
	}
	stdout := &limited{max: o.MaxBuffer}
	stderr := &limited{max: o.MaxBuffer}
	term := func() {
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
		}
	}
	stdout.stop, stderr.stop = term, term
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		e := &ExecError{Code: -1, msg: "spawn " + file + " " + errnoName(err)}
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, exec.ErrNotFound) || errors.Is(err, syscall.ENOENT) {
			e.Missing = true
			e.msg = "spawn " + file + " ENOENT"
		}
		return "", e
	}
	if o.Timeout > 0 {
		timer = time.AfterFunc(o.Timeout, func() { killed.Store(true); term() })
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-run.Done():
		// The request ended: the CLI is stopped as Node's would be when its
		// process exits; callers that outlive requests pass a background ctx.
		term()
		err = <-done
	}
	if timer != nil {
		timer.Stop()
	}
	out, errOut := stdout.buf.String(), stderr.buf.String()
	if stdout.over || stderr.over {
		which := "stdout"
		if stderr.over {
			which = "stderr"
		}
		return out, &ExecError{Stdout: out, Stderr: errOut, Code: -1, MaxBuffer: true, msg: which + " maxBuffer length exceeded"}
	}
	if err != nil {
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return out, &ExecError{Stdout: out, Stderr: errOut, Code: code, Killed: killed.Load(), msg: "Command failed: " + cmdline + "\n" + errOut}
	}
	return out, nil
}

func errnoName(err error) string {
	var en syscall.Errno
	if errors.As(err, &en) {
		switch en {
		case syscall.EACCES:
			return "EACCES"
		case syscall.ENOENT:
			return "ENOENT"
		}
	}
	return err.Error()
}

// ansi strips terminal colors, as the routes do before showing CLI text.
var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// StripANSI removes terminal color codes.
func StripANSI(s string) string { return ansi.ReplaceAllString(s, "") }
