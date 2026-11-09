package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

// If the shell itself dies without stopping the server (SIGKILL, a crash),
// the kernel sends TERM to its direct child. That child is the guard: the
// shell's own executable run as "--guard node script", which starts the
// server in the same process group and, on TERM (from the shell's stop or
// from the shell's death), ends the server and then every process left in
// the group, the server's own children included. Without the guard the
// server alone received the signal and its helpers stayed running.
//
// Pdeathsig fires when the OS thread that started the child exits; Go only
// retires threads that a goroutine locked and never unlocked, which the
// supervisor does not do.
func setParentDeathSignal(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGTERM }

// guardCommand makes cmd run through the guard.
//
// The guard is this running program (/proc/self/exe), not the file at its
// path: an update or a rollback replaces that file under the running shell
// with another version's, which may not know --guard.
func guardCommand(cmd *exec.Cmd) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	self := "/proc/self/exe"
	if _, err := os.Stat(self); err != nil {
		self = exe
	}
	cmd.Args = append([]string{exe, "--guard", cmd.Path}, cmd.Args[1:]...)
	cmd.Path = self
}

// maybeRunGuard runs the guard when the process was started as one.
func maybeRunGuard() bool {
	if len(os.Args) < 3 || os.Args[1] != "--guard" {
		return false
	}
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	child := exec.Command(os.Args[2], os.Args[3:]...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "guard:", err)
		os.Exit(127)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	select {
	case <-sig:
		_ = child.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
		// The group: the guard is its leader, so this ends the guard too.
		_ = syscall.Kill(0, syscall.SIGKILL)
		os.Exit(1)
	case <-done:
		code := child.ProcessState.ExitCode()
		if code < 0 {
			code = 1
		}
		os.Exit(code)
	}
	return true
}
