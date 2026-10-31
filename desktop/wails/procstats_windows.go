package main

// Process measurements for the self test, from the Toolhelp snapshot and
// psapi.

import (
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetProcessMemoryInfo = windows.NewLazySystemDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

type processMemoryCountersEx struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
	PrivateUsage               uintptr
}

// workingSet: working set and private bytes of a process, in KB.
func workingSet(pid uint32) (ws, private int64, ok bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, 0, false
	}
	defer windows.CloseHandle(h)
	var c processMemoryCountersEx
	c.CB = uint32(unsafe.Sizeof(c))
	r, _, _ := procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&c)), uintptr(c.CB))
	if r == 0 {
		return 0, 0, false
	}
	return int64(c.WorkingSetSize / 1024), int64(c.PrivateUsage / 1024), true
}

type winProc struct {
	pid, ppid uint32
	name      string
}

func allProcs() []winProc {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var out []winProc
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		out = append(out, winProc{pid: e.ProcessID, ppid: e.ParentProcessID, name: windows.UTF16ToString(e.ExeFile[:])})
	}
	return out
}

func pidAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

// memoryStats: the shell's own working set, and that of the WebView2
// processes it started (msedgewebview2.exe and their children), leaving
// out the Studio's server.
func memoryStats(serverPIDs map[int]bool) map[string]any {
	self := uint32(os.Getpid())
	ws, priv, _ := workingSet(self)
	procs := allProcs()
	children := map[uint32][]winProc{}
	for _, p := range procs {
		children[p.ppid] = append(children[p.ppid], p)
	}
	var web []map[string]any
	var webKB int64
	var walk func(pid uint32)
	walk = func(pid uint32) {
		for _, c := range children[pid] {
			if c.pid == pid || serverPIDs[int(c.pid)] || strings.EqualFold(c.name, "node.exe") || strings.EqualFold(c.name, "conhost.exe") {
				continue
			}
			w, p, _ := workingSet(c.pid)
			web = append(web, map[string]any{"pid": c.pid, "name": c.name, "workingSetKB": w, "privateKB": p})
			webKB += w
			walk(c.pid)
		}
	}
	walk(self)
	return map[string]any{
		"shellRSSKB":       ws,
		"shellPrivateKB":   priv,
		"webviewRSSKB":     webKB,
		"webviewProcesses": web,
		"note":             "shellRSSKB is the shell process's working set alone; WebView2 renders in msedgewebview2.exe processes, counted in webviewRSSKB (working sets, which double-count shared pages)",
	}
}
