package main

import "syscall"

// If the shell itself dies without stopping the server (killed, crashed),
// the kernel sends the server TERM. This covers the server, not its own
// children; those stay in its process group, which the server's TERM
// handler does not end. Pdeathsig fires when the OS thread that started
// the child exits; Go only retires threads that a goroutine locked and
// never unlocked, which the supervisor does not do.
func setParentDeathSignal(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGTERM }
