//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// WebKitGTK runs the page in helper processes (WebKitWebProcess,
// WebKitNetworkProcess, WebKitGPUProcess), children of the shell, which end
// on their own once the window is gone. reapWebKit waits for them, up to
// wait, then sends TERM and after a second KILL to any left, so the shell
// exits only after they have: an update or a reinstall started right after
// the window closes does not find them still running from the old copy.
func reapWebKit(wait time.Duration) {
	end := time.Now().Add(wait)
	for time.Now().Before(end) {
		if len(webKitChildren(os.Getpid())) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		left := webKitChildren(os.Getpid())
		if len(left) == 0 {
			return
		}
		for _, pid := range left {
			_ = syscall.Kill(pid, sig)
		}
		for i := 0; i < 10 && len(webKitChildren(os.Getpid())) > 0; i++ {
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// webKitChildren are the live processes whose parent is ppid and whose name
// starts with WebKit.
func webKitChildren(ppid int) []int {
	stats, _ := filepath.Glob("/proc/[0-9]*/stat")
	var out []int
	for _, st := range stats {
		pid, comm, state, parent, ok := readStat(st)
		if ok && parent == ppid && state != "Z" && strings.HasPrefix(comm, "WebKit") {
			out = append(out, pid)
		}
	}
	return out
}

// readStat reads /proc/PID/stat: pid (comm) state ppid ...; comm may hold
// spaces and parentheses, so it ends at the last ")".
func readStat(path string) (pid int, comm, state string, ppid int, ok bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	s := string(b)
	open, close := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || close < open {
		return
	}
	pid, err = strconv.Atoi(strings.TrimSpace(s[:open]))
	if err != nil {
		return
	}
	comm = s[open+1 : close]
	f := strings.Fields(s[close+1:])
	if len(f) < 2 {
		return
	}
	state = f[0]
	ppid, err = strconv.Atoi(f[1])
	return pid, comm, state, ppid, err == nil
}
