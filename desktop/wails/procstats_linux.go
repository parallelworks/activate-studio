package main

// Process measurements for the self test, from /proc.

import (
	"os"
	"strconv"
	"strings"
)

// procInfo: name, parent, process group, state and resident memory.
type procInfo struct {
	pid, ppid, pgid int
	name, state     string
	rssKB           int64
}

func readProc(pid int) (procInfo, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procInfo{}, false
	}
	s := string(b)
	open, close := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || close < open {
		return procInfo{}, false
	}
	f := strings.Fields(s[close+1:])
	if len(f) < 22 {
		return procInfo{}, false
	}
	p := procInfo{pid: pid, name: s[open+1 : close], state: f[0]}
	p.ppid, _ = strconv.Atoi(f[1])
	p.pgid, _ = strconv.Atoi(f[2])
	pages, _ := strconv.ParseInt(f[21], 10, 64)
	p.rssKB = pages * int64(os.Getpagesize()) / 1024
	return p, true
}

func allProcs() []procInfo {
	ents, _ := os.ReadDir("/proc")
	var out []procInfo
	for _, e := range ents {
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			if p, ok := readProc(pid); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

// pidAlive: running, and not a zombie waiting to be reaped.
func pidAlive(pid int) bool {
	p, ok := readProc(pid)
	return ok && p.state != "Z" && p.state != "X"
}

// memoryStats: the shell's own resident memory, and that of the web view's
// processes (its descendants outside the server's process group).
func memoryStats(serverPIDs map[int]bool) map[string]any {
	self := os.Getpid()
	me, _ := readProc(self)
	procs := allProcs()
	children := map[int][]procInfo{}
	for _, p := range procs {
		children[p.ppid] = append(children[p.ppid], p)
	}
	var web []map[string]any
	var webKB int64
	var walk func(pid int)
	walk = func(pid int) {
		for _, c := range children[pid] {
			if serverPIDs[c.pgid] || serverPIDs[c.pid] {
				continue // the Studio's server and what it started
			}
			web = append(web, map[string]any{"pid": c.pid, "name": c.name, "rssKB": c.rssKB})
			webKB += c.rssKB
			walk(c.pid)
		}
	}
	walk(self)
	return map[string]any{
		"shellRSSKB":       me.rssKB,
		"webviewRSSKB":     webKB,
		"webviewProcesses": web,
		"note":             "shellRSSKB is the shell process alone (GTK, WebKitGTK UI side, Go); the web view renders in WebKitWebProcess and WebKitNetworkProcess, counted in webviewRSSKB",
	}
}
