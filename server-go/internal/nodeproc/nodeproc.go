// Package nodeproc runs the Node server behind the Go front: on a loopback
// port, with the Go server's environment, until the Go server stops.
package nodeproc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Proc is a running Node server.
type Proc struct {
	URL  *url.URL
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

// Start runs `nodeBin main` with PORT set to a free loopback port and HOST
// to 127.0.0.1, so only the Go front is reachable from outside, and waits
// until Node is ready: a line on the STUDIO_READY_FD pipe, which Node writes
// once it listens, or else its /healthz answering, polled from 5 ms up to
// every 100 ms. env sets variables for Node over the Go server's own; a
// value of "" removes the variable.
func Start(ctx context.Context, log *slog.Logger, nodeBin, main string, wait time.Duration, env map[string]string) (*Proc, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(nodeBin, main)
	cmd.Env = append(Environ(os.Environ(), env), "PORT="+strconv.Itoa(port), "HOST=127.0.0.1", "STUDIO_BEHIND_GO=1")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	ready, started, err := readySignal(cmd)
	if err != nil {
		return nil, fmt.Errorf("readiness pipe: %w", err)
	}
	err = cmd.Start()
	started()
	if err != nil {
		return nil, fmt.Errorf("starting node: %w", err)
	}
	p := &Proc{URL: &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(port)}, cmd: cmd, done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	log.Info("node starting", "pid", cmd.Process.Pid, "url", p.URL.String())
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(wait)
	delay := 5 * time.Millisecond
	for {
		if res, err := client.Get(p.URL.String() + "/healthz"); err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				log.Info("node ready", "pid", cmd.Process.Pid, "by", "healthz")
				return p, nil
			}
		}
		if time.Now().After(deadline) {
			p.Stop(5 * time.Second)
			return nil, fmt.Errorf("node did not answer within %s", wait)
		}
		t := time.NewTimer(delay)
		select {
		case <-ready:
			t.Stop()
			log.Info("node ready", "pid", cmd.Process.Pid, "by", "signal")
			return p, nil
		case <-p.done:
			t.Stop()
			return nil, fmt.Errorf("node exited before answering: %v", p.err)
		case <-ctx.Done():
			t.Stop()
			p.Stop(5 * time.Second)
			return nil, ctx.Err()
		case <-t.C:
		}
		delay = min(2*delay, 100*time.Millisecond)
	}
}

// Done is closed when Node exits.
func (p *Proc) Done() <-chan struct{} { return p.done }

// ExitCode is Node's exit code once Done is closed (76 asks for a relaunch,
// server/src/lifecycle.ts), or -1.
func (p *Proc) ExitCode() int {
	var ee *exec.ExitError
	if p.err == nil {
		return 0
	}
	if errors.As(p.err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// Stop asks Node to stop (it lets open streams finish, up to 20 seconds)
// and kills it after grace.
func (p *Proc) Stop(grace time.Duration) {
	select {
	case <-p.done:
		return
	default:
	}
	terminate(p.cmd.Process)
	select {
	case <-p.done:
	case <-time.After(grace):
		p.cmd.Process.Kill()
		<-p.done
	}
}

// Environ is base with env's variables set over it; "" removes one.
func Environ(base []string, env map[string]string) []string {
	out := make([]string, 0, len(base)+len(env))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if _, over := env[k]; over {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range env {
		if v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
