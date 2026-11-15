package transfers

import (
	"context"
	"io"
	"os/exec"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// PoolRunner is a Runner over the server's shared ssh pool.
type PoolRunner struct{ P *remote.Pool }

func (p PoolRunner) RunOnce(ctx context.Context, site, key string, argv []string, timeout time.Duration, input io.Reader) (remote.Result, error) {
	return p.P.RunOnce(ctx, site, key, argv, timeout, input)
}

func (p PoolRunner) Command(site, key string, argv []string) (Cmd, error) {
	c, err := p.P.Command(site, key, argv)
	if err != nil {
		return nil, err
	}
	return &execCmd{c}, nil
}

// Subsystem starts an ssh subsystem (sftp) on a plain ssh site.
func (p PoolRunner) Subsystem(site, key, name string) (Cmd, error) {
	c, err := p.P.Subsystem(site, name)
	if err != nil {
		return nil, err
	}
	return &execCmd{c}, nil
}

// RemoteShell is how rsync's -e reaches the site.
func (p PoolRunner) RemoteShell(site, key string) ([]string, []string, error) {
	return p.P.RemoteShell(site, key)
}

type execCmd struct{ *exec.Cmd }

func (c *execCmd) SetStderr(w io.Writer) { c.Cmd.Stderr = w }
func (c *execCmd) Kill() {
	if c.Cmd.Process != nil {
		c.Cmd.Process.Kill()
	}
}

// ExecCmd wraps an exec.Cmd as a Cmd (tests that run commands here).
func ExecCmd(c *exec.Cmd) Cmd { return &execCmd{c} }
