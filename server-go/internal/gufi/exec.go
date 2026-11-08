package gufi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// The tools a GUFI command runs.
const (
	ToolQuery   = "gufi_query"
	ToolSQLite3 = "gufi_sqlite3"
)

// Command is one run of a GUFI tool.
type Command struct {
	Tool    string // ToolQuery or ToolSQLite3
	Args    []string
	Stdin   []byte
	Timeout time.Duration // 0: the runner's
}

// Result is a finished run. A nonzero exit is not an error: gufi_query
// exits nonzero when a step fails in some directory, and the rows it
// printed still count.
type Result struct {
	Stdout    []byte
	Stderr    string // its last StderrTail bytes
	StderrCut bool
	ExitCode  int
	Signal    string
	Elapsed   time.Duration
}

// StderrTail is how much of a run's error output is kept: a walk prints
// a line for every directory where a step failed.
const StderrTail = 16 << 10

// ErrTimeout is a run that outlived its timeout and was killed.
var ErrTimeout = errors.New("gufi: timed out")

// Executor runs GUFI commands somewhere: on this host (LocalExecutor), or
// on a cluster (a later stage).
type Executor interface {
	Exec(ctx context.Context, c Command) (Result, error)
}

// LocalExecutor runs GUFI's tools from Bin with Env. Ending ctx kills the
// process by its PID.
type LocalExecutor struct {
	Bin string
	Env []string // EnvFor's; nil is EnvFor(Bin, os.Environ())
	// MaxStdout ends a run whose output passes it; 0 is no limit.
	MaxStdout int
}

// NewLocal is a LocalExecutor for bin with gufiEnv's environment.
func NewLocal(bin string) *LocalExecutor {
	return &LocalExecutor{Bin: bin, Env: EnvFor(bin, os.Environ(), nil, EnvOptions{})}
}

type tail struct {
	b   []byte
	cut bool
}

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > StderrTail {
		t.b = append([]byte(nil), t.b[len(t.b)-StderrTail:]...)
		t.cut = true
	}
	return len(p), nil
}

// capped is not a bytes.Buffer itself: io.Copy would use the buffer's
// ReadFrom and pass by Write.
type capped struct {
	buf  bytes.Buffer
	max  int
	over bool
	kill func()
}

func (c *capped) Write(p []byte) (int, error) {
	if c.max > 0 && c.buf.Len()+len(p) > c.max {
		if !c.over {
			c.over = true
			c.kill()
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}

// Exec runs c, killing it when ctx ends or its timeout passes.
func (l *LocalExecutor) Exec(ctx context.Context, c Command) (Result, error) {
	if c.Tool != ToolQuery && c.Tool != ToolSQLite3 {
		return Result{}, fmt.Errorf("gufi: unknown tool %q", c.Tool)
	}
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	cmd := exec.Command(filepath.Join(l.Bin, c.Tool), c.Args...)
	cmd.Env = l.Env
	if cmd.Env == nil {
		cmd.Env = EnvFor(l.Bin, os.Environ(), nil, EnvOptions{})
	}
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	var errOut tail
	// os.Process signals its own PID, never a pattern or a group. Start
	// sets Process before it copies any output.
	kill := func() { cmd.Process.Kill() }
	out := &capped{max: l.MaxStdout, kill: kill}
	cmd.Stdout, cmd.Stderr = out, &errOut
	t0 := time.Now()
	if err := cmd.Start(); err != nil {
		return Result{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var werr error
	select {
	case werr = <-done:
	case <-ctx.Done():
		kill()
		werr = <-done
	}
	res := Result{Stdout: out.buf.Bytes(), Stderr: string(errOut.b), StderrCut: errOut.cut, Elapsed: time.Since(t0)}
	if st, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && st.Signaled() {
		res.Signal = st.Signal().String()
		res.ExitCode = -1
	} else if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	var exit *exec.ExitError
	if werr != nil && !errors.As(werr, &exit) {
		return res, werr
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return res, ErrTimeout
	case ctx.Err() != nil:
		return res, ctx.Err()
	case out.over:
		return res, fmt.Errorf("gufi: output passed %d bytes", l.MaxStdout)
	}
	return res, nil
}
