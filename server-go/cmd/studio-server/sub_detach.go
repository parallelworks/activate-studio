//go:build !windows

package main

// `studio-server detach PROGRAM ARGS...` runs PROGRAM in a session of its own
// that ignores hangups, in place of this process: the macOS launcher's way
// to start the run loop so that it outlives the launcher and the window
// (deploy/mac/launcher.sh, start_detached), which the app's Python did
// before the Go version carried none.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

func init() {
	registerSubcommand("detach", subcommand{Summary: "run a program in a new session that ignores hangups (the macOS launcher's run loop)", Run: runDetach})
}

func runDetach(args []string, _ io.Reader, _, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: studio-server detach PROGRAM [ARGS...]")
		return 2
	}
	prog, err := exec.LookPath(args[0])
	if err != nil {
		fmt.Fprintln(stderr, "detach:", err)
		return 127
	}
	// A process group leader cannot start a session; the launcher starts
	// this in the background, so it is not one, as with Python's os.setsid.
	if _, err := syscall.Setsid(); err != nil {
		fmt.Fprintln(stderr, "detach: setsid:", err)
		return 1
	}
	// SIG_IGN survives exec, as signal.signal(SIGHUP, SIG_IGN) did.
	signal.Ignore(syscall.SIGHUP)
	err = syscall.Exec(prog, args, os.Environ())
	fmt.Fprintln(stderr, "detach: exec:", err)
	return 126
}
