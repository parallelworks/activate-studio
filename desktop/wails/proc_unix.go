//go:build !windows

package main

// The server runs in a process group of its own, so the shell can end it
// and everything it started with one signal to the group.

import (
	"os/exec"
	"syscall"
	"time"
)

type serverProcess struct {
	cmd  *exec.Cmd
	pgid int
	done chan struct{}
}

func startServerProcess(cmd *exec.Cmd) (*serverProcess, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	setParentDeathSignal(cmd.SysProcAttr)
	guardCommand(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// Setpgid with Pgid 0 makes the child the leader of a new group.
	return &serverProcess{cmd: cmd, pgid: cmd.Process.Pid, done: make(chan struct{})}, nil
}

// exited is called by the supervisor once Wait returns.
func (p *serverProcess) exited() { close(p.done) }

// stop asks the group to end (TERM: the server drains and stops), then
// kills what is left after the timeout.
func (p *serverProcess) stop(timeout time.Duration) {
	_ = syscall.Kill(-p.pgid, syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(timeout):
	}
	p.killTree()
}

// killTree ends every process still in the group, the server's helpers
// included, even after the server itself exited.
func (p *serverProcess) killTree() { _ = syscall.Kill(-p.pgid, syscall.SIGKILL) }

func (p *serverProcess) close() {}
