package main

// The self test (STUDIO_SHELL_TEST=1): the checklist run from inside the
// Studio's page in this window, for CI, where nobody can click. The page
// side (shell/selftest.js, appended to the shim) asks which phase it is in
// (TestHello), does that phase's part and reports (TestReport); this side
// moves the server and the window from phase to phase and writes
// results.json in STUDIO_SHELL_TEST_DIR, then closes the window, which stops
// the server, and records whether anything it started is still running.
//
// Phases (plan "full"):
//
//	checks         bridge, streaming, downloads, links, WebGL; screenshot; idle memory
//	after76        the server exited 76 (asked by the test); the shell started it again and reloaded
//	relaunch       the page asks the shell to relaunch (as UpdatedNotice does)
//	afterRelaunch  the server was restarted and the page reloaded
//	move           the page saves another port (Settings, Server) and tells the shell (studioMoved)
//	afterMove      the window followed the server to the new port, with the hash
//	fail           the server exits 3: the start page shows the error (screenshot), then Retry
//	afterRetry     the server answered again; the window closes
//
// Plan "hold" starts the Studio, writes pids.json and waits, so CI can kill
// the shell outright and look for processes left behind.

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const preloadJS = `// The self test's preload for the Studio's server.
import fs from 'node:fs'
import path from 'node:path'
import { spawn } from 'node:child_process'
const dir = process.env.STUDIO_SHELL_TEST_DIR
// A helper the server leaves running, as a build or an indexer it started
// would be: it must end with the server.
const helper = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1e6)'], { stdio: 'ignore', windowsHide: true })
helper.unref()
fs.appendFileSync(path.join(dir, 'pids.log'), JSON.stringify({ server: process.pid, helper: helper.pid, at: Date.now() }) + '\n')
// The shell asks for an exit status by writing it to the trigger file.
const trigger = path.join(dir, 'trigger')
setInterval(() => {
  let code
  try { code = Number(fs.readFileSync(trigger, 'utf8').trim()); fs.rmSync(trigger) } catch { return }
  if (Number.isInteger(code)) process.exit(code)
}, 100).unref()
`

type SelfTest struct {
	app *App
	dir string

	mu          sync.Mutex
	phase       string
	handed      bool
	results     map[string]any
	events      []map[string]any
	notes       map[string][]any
	marks       map[string]int64
	folderCalls int
	streamPort  int
	movePort    int
	finished    bool
	logFile     *os.File
}

func NewSelfTest(a *App) *SelfTest {
	t := &SelfTest{
		app:     a,
		dir:     a.cfg.TestDir,
		results: map[string]any{},
		notes:   map[string][]any{},
		marks:   map[string]int64{},
	}
	_ = os.MkdirAll(t.dir, 0o755)
	_ = os.Remove(filepath.Join(t.dir, "pids.log"))
	_ = os.Remove(filepath.Join(t.dir, "trigger"))
	// The server's preload reads it.
	_ = os.Setenv("STUDIO_SHELL_TEST_DIR", t.dir)
	t.logFile, _ = os.OpenFile(filepath.Join(t.dir, "selftest.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	t.phase = "checks"
	if a.cfg.TestPlan == "hold" {
		t.phase = "hold"
	}
	t.movePort = freePort()
	t.startStreamServer()
	t.mark("process")
	return t
}

func (t *SelfTest) logf(format string, args ...any) {
	line := fmt.Sprintf("%s %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
	if t.logFile != nil {
		_, _ = t.logFile.WriteString(line)
	}
	fmt.Fprint(os.Stderr, "selftest: "+line)
}

func (t *SelfTest) mark(name string) {
	t.mu.Lock()
	t.marks[name] = time.Now().UnixMilli()
	t.mu.Unlock()
}

func (t *SelfTest) note(kind string, v any) {
	t.mu.Lock()
	t.notes[kind] = append(t.notes[kind], v)
	t.mu.Unlock()
	t.logf("note %s %v", kind, v)
}

// nodeArgs: the preload, as a file: URL (node --import takes URLs; a bare
// Windows path with a drive letter is not one).
func (t *SelfTest) nodeArgs() []string {
	file := filepath.Join(t.dir, "preload.mjs")
	_ = os.WriteFile(file, []byte(preloadJS), 0o644)
	p := filepath.ToSlash(file)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return []string{"--import", (&url.URL{Scheme: "file", Path: p}).String()}
}

// startStreamServer: ten server-sent events 200 ms apart, on a port of its
// own, reached through the same layers as the Studio (/__shell/stream-test)
// and also directly (cross-origin, so with CORS) for comparison.
func (t *SelfTest) startStreamServer() {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.logf("stream server: %v", err)
		return
	}
	t.streamPort = l.Addr().(*net.TCPAddr).Port
	t.app.proxy.mu.Lock()
	t.app.proxy.testPort = t.streamPort
	t.app.proxy.mu.Unlock()
	mux := http.NewServeMux()
	// Where the page's probe and its errors are posted (text/plain, so no
	// CORS preflight), from any origin, past the bridge.
	mux.HandleFunc("/log", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		b, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		t.logf("page: %s", strings.TrimSpace(string(b)))
		t.mu.Lock()
		t.notes["pageLog"] = append(t.notes["pageLog"], string(b))
		t.mu.Unlock()
	})
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		_ = rc.Flush()
		for i := 0; i < 10; i++ {
			fmt.Fprintf(w, "data: {\"i\":%d,\"t\":%d}\n\n", i, time.Now().UnixMilli())
			_ = rc.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
	})
	go func() { _ = http.Serve(l, mux) }()
}

// probe posts what the page is doing to the stream server's /log, from
// inside the page and without the bridge.
func (t *SelfTest) probe(label string) {
	js := fmt.Sprintf(`(function () {
  try {
    if (!window.__probeErrors) {
      window.__probeErrors = [];
      window.addEventListener('error', function (e) { window.__probeErrors.push(String(e.message) + ' @' + e.filename + ':' + e.lineno); });
      window.addEventListener('unhandledrejection', function (e) { window.__probeErrors.push('rejection: ' + String(e.reason && (e.reason.stack || e.reason))); });
    }
    var root = document.getElementById('root');
    var b = window.go && window.go.main && window.go.main.Bridge;
    var s = {
      label: %s, href: location.href, readyState: document.readyState, title: document.title,
      hasWailsRuntime: !!(window.runtime), hasGo: !!window.go, bridgeMethods: b ? Object.keys(b) : null,
      hasShim: !!window.__studioShell, shimHow: window.__studioShell && window.__studioShell.how,
      hasTest: !!window.__studioShellTest, hasHandler: !!(window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.studioShell),
      chromeWebview: !!(window.chrome && window.chrome.webview),
      rootChildren: root ? root.children.length : null, text: (document.body ? document.body.innerText : '').slice(0, 160),
      errors: window.__probeErrors.slice(-8)
    };
    fetch('http://127.0.0.1:%d/log', { method: 'POST', body: JSON.stringify(s), mode: 'no-cors' });
  } catch (e) {
    try { fetch('http://127.0.0.1:%d/log', { method: 'POST', body: 'probe failed: ' + e, mode: 'no-cors' }); } catch (_) {}
  }
})()`, jsString(label), t.streamPort, t.streamPort)
	t.app.execJS(js)
}

// start: the overall time limit.
func (t *SelfTest) start() {
	t.mark("startup")
	// What the window shows, early on: a screenshot and a probe of the
	// page, whatever else happens.
	go func() {
		for i, at := range []time.Duration{5, 10, 20, 40} {
			time.Sleep(at*time.Second - []time.Duration{0, 5, 10, 20}[i]*time.Second)
			t.probe(fmt.Sprintf("t%d", at))
			if at == 10 || at == 40 {
				t.screenshot(fmt.Sprintf("t%02d", at))
			}
		}
	}()
	go func() {
		time.Sleep(150 * time.Second)
		t.mu.Lock()
		done := t.finished || t.phase == "hold"
		t.mu.Unlock()
		if !done {
			t.logf("time limit reached in phase %s", t.currentPhase())
			t.set("timeout", true)
			t.probe("timeout")
			time.Sleep(1 * time.Second)
			t.screenshot("timeout")
			t.quit()
		}
	}()
}

func (t *SelfTest) currentPhase() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.phase
}

func (t *SelfTest) set(k string, v any) {
	t.mu.Lock()
	t.results[k] = v
	t.mu.Unlock()
}

func (t *SelfTest) setPhase(p string) {
	t.mu.Lock()
	t.phase, t.handed = p, false
	t.mu.Unlock()
	t.logf("phase -> %s", p)
}

func (t *SelfTest) serverEvent(e ServerEvent) {
	ev := map[string]any{"at": time.Now().UnixMilli(), "kind": e.Kind, "port": e.Port, "reason": e.Reason}
	t.mu.Lock()
	t.events = append(t.events, ev)
	if e.Kind == "ready" {
		if _, ok := t.marks["firstReady"]; !ok {
			t.marks["firstReady"] = time.Now().UnixMilli()
		}
	}
	phase := t.phase
	t.mu.Unlock()
	t.logf("server %s port=%d reason=%s", e.Kind, e.Port, e.Reason)
	if e.Kind == "failed" && phase == "fail" {
		go t.handleFail(e.Message)
	}
}

// hello: the page's question on each load. A phase is handed out once; a
// page that loads again in the same phase is told to wait.
func (t *SelfTest) hello(info map[string]any) map[string]any {
	t.mu.Lock()
	phase := t.phase
	if t.handed {
		phase = "idle"
	}
	t.handed = true
	t.mu.Unlock()
	t.logf("hello %v -> %s", info["href"], phase)
	stream := "/__shell/stream-test"
	if t.app.cfg.Mode == ModeDirect {
		stream = fmt.Sprintf("http://127.0.0.1:%d/stream", t.streamPort)
	}
	return map[string]any{
		"phase":           phase,
		"streamURL":       stream,
		"directStreamURL": fmt.Sprintf("http://127.0.0.1:%d/stream", t.streamPort),
		"externalURL":     "https://example.com/",
		"nativeDownload":  fmt.Sprintf("studio-shell-native-%s-%d.csv", t.app.cfg.Mode, os.Getpid()),
		"bridgeDownload":  fmt.Sprintf("studio-shell-bridge-%s-%d.csv", t.app.cfg.Mode, os.Getpid()),
		"movePort":        t.movePort,
	}
}

func (t *SelfTest) report(phase string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["serverPort"] = t.app.sup.Port()
	t.mu.Lock()
	t.results[phase] = data
	t.mu.Unlock()
	t.logf("report %s", phase)
	go t.advance(phase)
}

func (t *SelfTest) advance(phase string) {
	switch phase {
	case "checks":
		time.Sleep(500 * time.Millisecond)
		t.screenshot("studio")
		t.checkDownloads()
		// Idle: nothing loading, nothing streaming.
		time.Sleep(3 * time.Second)
		t.set("idleMemory", memoryStats(t.serverPIDs()))
		t.setPhase("after76")
		t.trigger(76)
	case "after76":
		t.setPhase("relaunch")
		t.app.execJS("location.reload()")
	case "relaunch":
		// The page asks for the relaunch itself, after this report.
		t.setPhase("afterRelaunch")
	case "afterRelaunch":
		t.setPhase("move")
		t.app.execJS("location.reload()")
	case "move":
		t.setPhase("afterMove")
	case "afterMove":
		t.setPhase("fail")
		t.trigger(3)
	case "afterRetry":
		t.setPhase("done")
		t.quit()
	case "hold":
		t.writePIDs()
	}
}

func (t *SelfTest) handleFail(message string) {
	time.Sleep(3 * time.Second)
	t.set("fail", map[string]any{"message": message, "state": t.app.State()})
	t.screenshot("error")
	t.setPhase("afterRetry")
	t.logf("retry")
	t.app.retry()
}

// trigger asks the server to exit with code (the preload reads the file).
func (t *SelfTest) trigger(code int) {
	t.logf("asking the server to exit %d", code)
	_ = os.WriteFile(filepath.Join(t.dir, "trigger"), []byte(fmt.Sprint(code)), 0o644)
}

func (t *SelfTest) chooseFolderCall(start, message, prompt string) (first bool) {
	t.mu.Lock()
	t.folderCalls++
	n := t.folderCalls
	t.mu.Unlock()
	t.note("chooseFolder", map[string]any{"call": n, "start": start, "message": message, "prompt": prompt})
	return n == 1
}

// closeDialogSoon: the real folder dialog is open; photograph it and close
// it as a person pressing Escape would.
func (t *SelfTest) closeDialogSoon() {
	time.Sleep(2500 * time.Millisecond)
	t.screenshot("dialog")
	if len(t.app.cfg.DialogClose) == 0 {
		t.note("dialogClose", "no close command")
		return
	}
	// Another program activating the window while the dialog is open (the
	// Windows check): the shell must live through it.
	if len(t.app.cfg.DialogActivate) > 0 {
		cmd := exec.Command(t.app.cfg.DialogActivate[0], t.app.cfg.DialogActivate[1:]...)
		cmd.Env = append(os.Environ(), fmt.Sprintf("STUDIO_SHELL_PID=%d", os.Getpid()))
		out, err := cmd.CombinedOutput()
		t.note("dialogActivate", map[string]any{"err": fmt.Sprint(err), "out": strings.TrimSpace(string(out))})
		time.Sleep(1 * time.Second)
		t.screenshot("dialog-after-activate")
	}
	out, err := exec.Command(t.app.cfg.DialogClose[0], t.app.cfg.DialogClose[1:]...).CombinedOutput()
	t.note("dialogClose", map[string]any{"err": fmt.Sprint(err), "out": strings.TrimSpace(string(out))})
}

func (t *SelfTest) screenshot(name string) {
	if len(t.app.cfg.Screenshot) == 0 {
		return
	}
	file := filepath.Join(t.dir, name+".png")
	args := append(append([]string{}, t.app.cfg.Screenshot[1:]...), file)
	start := time.Now()
	out, err := exec.Command(t.app.cfg.Screenshot[0], args...).CombinedOutput()
	t.note("screenshot", map[string]any{"name": name, "file": file, "err": fmt.Sprint(err), "ms": time.Since(start).Milliseconds(), "out": strings.TrimSpace(string(out))})
}

// checkDownloads: where the two test files landed. "native" is the web
// view's own handling of <a download> (the shim stood aside); "bridge" is
// the shim's.
func (t *SelfTest) checkDownloads() {
	dir := downloadsDir()
	out := map[string]any{"downloadsDir": dir}
	for _, kind := range []string{"native", "bridge"} {
		name := fmt.Sprintf("studio-shell-%s-%s-%d.csv", kind, t.app.cfg.Mode, os.Getpid())
		p := filepath.Join(dir, name)
		st, err := os.Stat(p)
		if err != nil {
			// WebKitGTK saves a native download in the home folder.
			if home, herr := os.UserHomeDir(); herr == nil {
				if st2, err2 := os.Stat(filepath.Join(home, name)); err2 == nil {
					p, st, err = filepath.Join(home, name), st2, nil
				}
			}
		}
		entry := map[string]any{"path": p, "found": err == nil}
		if err == nil {
			entry["bytes"] = st.Size()
			b, _ := os.ReadFile(p)
			entry["content"] = string(b)
		}
		out[kind] = entry
	}
	t.set("downloads", out)
}

// serverPIDs: every server the supervisor started, for leaving them out of
// the web view's memory.
func (t *SelfTest) serverPIDs() map[int]bool {
	out := map[int]bool{}
	for _, r := range t.pidRecords() {
		out[r.Server] = true
		out[r.Helper] = true
	}
	_, pids, _ := t.app.sup.Stats()
	for _, p := range pids {
		out[p] = true
	}
	return out
}

type pidRecord struct {
	Server int   `json:"server"`
	Helper int   `json:"helper"`
	At     int64 `json:"at"`
}

func (t *SelfTest) pidRecords() []pidRecord {
	b, _ := os.ReadFile(filepath.Join(t.dir, "pids.log"))
	var out []pidRecord
	for _, line := range strings.Split(string(b), "\n") {
		var r pidRecord
		if json.Unmarshal([]byte(line), &r) == nil && r.Server != 0 {
			out = append(out, r)
		}
	}
	return out
}

// writePIDs (plan "hold"): the shell's and the server's PIDs, for CI to
// kill the shell and check.
func (t *SelfTest) writePIDs() {
	time.Sleep(1 * time.Second)
	b, _ := json.MarshalIndent(map[string]any{"shell": os.Getpid(), "servers": t.pidRecords()}, "", "  ")
	_ = os.WriteFile(filepath.Join(t.dir, "pids.json"), b, 0o644)
	t.logf("pids.json written")
}

func (t *SelfTest) quit() {
	t.logf("closing the window")
	if t.app.ctx != nil {
		wruntime.Quit(t.app.ctx)
	}
}

// finish runs in OnShutdown after the server was stopped: check that
// nothing it started is running, then write the results.
func (t *SelfTest) finish() {
	t.mu.Lock()
	if t.finished {
		t.mu.Unlock()
		return
	}
	t.finished = true
	t.mu.Unlock()
	time.Sleep(500 * time.Millisecond)
	var alive []int
	recs := t.pidRecords()
	for _, r := range recs {
		for _, p := range []int{r.Server, r.Helper} {
			if p != 0 && pidAlive(p) {
				alive = append(alive, p)
			}
		}
	}
	starts, pids, exits := t.app.sup.Stats()
	t.mu.Lock()
	t.results["stop"] = map[string]any{"processes": recs, "aliveAfterStop": alive}
	t.results["supervisor"] = map[string]any{"starts": starts, "pids": pids, "exits": exits}
	t.results["events"] = t.events
	t.results["notes"] = t.notes
	t.results["marks"] = t.marks
	t.results["mode"] = t.app.cfg.Mode
	t.results["os"] = runtime.GOOS
	t.results["arch"] = runtime.GOARCH
	t.results["launchT0"] = t.app.cfg.LaunchT0
	t.results["summary"] = t.summary()
	b, err := json.MarshalIndent(t.results, "", "  ")
	t.mu.Unlock()
	if err != nil {
		t.logf("results: %v", err)
		return
	}
	_ = os.WriteFile(filepath.Join(t.dir, "results.json"), b, 0o644)
	t.logf("results.json written")
}

// summary: a pass or fail per checklist item, from what was recorded.
// Called with t.mu held.
func (t *SelfTest) summary() map[string]any {
	get := func(m any, keys ...string) any {
		for _, k := range keys {
			mm, ok := m.(map[string]any)
			if !ok {
				return nil
			}
			m = mm[k]
		}
		return m
	}
	num := func(v any) float64 {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		case int64:
			return float64(n)
		}
		return -1
	}
	r := t.results
	out := map[string]any{}

	drawn := num(get(r, "checks", "drawnAt"))
	versionOK := num(get(r, "checks", "version", "status")) == 200
	out["start"] = drawn > 0 && versionOK
	if drawn > 0 && t.app.cfg.LaunchT0 > 0 {
		out["coldStartMs"] = int64(drawn) - t.app.cfg.LaunchT0
	}
	if ready, ok := t.marks["firstReady"]; ok && t.app.cfg.LaunchT0 > 0 {
		out["serverReadyMs"] = ready - t.app.cfg.LaunchT0
	}

	_, pids, _ := func() (int, []int, []int) { s, p, e := t.app.sup.Stats(); return s, p, e }()
	alive, _ := get(r, "stop", "aliveAfterStop").([]int)
	after76 := num(get(r, "after76", "version", "status")) == 200
	afterRelaunch := num(get(r, "afterRelaunch", "version", "status")) == 200
	movedOK := num(get(r, "afterMove", "serverPort")) == float64(t.movePort)
	hashOK := get(r, "afterMove", "hash") == "#view=settings"
	failMsg, _ := get(r, "fail", "message").(string)
	afterRetry := num(get(r, "afterRetry", "version", "status")) == 200
	out["supervise"] = map[string]any{
		"restartAfter76":   after76,
		"relaunch":         afterRelaunch,
		"followMove":       movedOK && hashOK,
		"errorOnOtherExit": strings.Contains(failMsg, "exit status 3"),
		"retry":            afterRetry,
		"serverStarts":     len(pids),
		"noOrphans":        len(alive) == 0 && len(t.pidRecords()) > 0,
	}

	out["bridge"] = map[string]any{
		"present":        get(r, "checks", "bridge", "present") == true,
		"returnsPromise": get(r, "checks", "bridge", "returnsPromise") == true,
		"chooseFolder":   get(r, "checks", "bridge", "chooseFolder") == t.app.cfg.TestFolder,
		"realDialog":     fmt.Sprint(get(r, "checks", "bridge", "chooseFolderReal")),
		"relaunch":       get(r, "relaunchAnswer", "answer") == true,
		"studioMoved":    get(r, "move", "studioMoved") == true,
		"how":            get(r, "checks", "bridge", "how"),
		// A bridge call after the real dialog closed (and after any
		// window activation done while it was open) is answered.
		"afterDialog": strings.Contains(fmt.Sprint(get(r, "checks", "bridge", "afterDialog")), "unknown request"),
	}
	out["dialogActivation"] = map[string]any{
		"ran":        len(t.notes["dialogActivate"]) > 0,
		"focusGuard": t.app.cfg.FocusGuard,
	}

	incremental := func(s any) any {
		chunks := num(get(s, "chunks"))
		first, last := num(get(s, "firstChunkMs")), num(get(s, "lastChunkMs"))
		if chunks < 0 {
			return nil
		}
		return chunks >= 5 && last-first >= 1200
	}
	out["streaming"] = map[string]any{
		"fetchThroughModePath": incremental(get(r, "checks", "stream")),
		"fetchDirect":          incremental(get(r, "checks", "streamDirect")),
		"eventSourceEvents":    get(r, "checks", "eventSource", "events"),
		"chatStreamEvents":     get(r, "checks", "chatStream", "events"),
	}
	out["downloads"] = map[string]any{
		"native": get(r, "downloads", "native", "found"),
		"bridge": get(r, "downloads", "bridge", "found"),
	}
	out["links"] = map[string]any{
		"stayed":        get(r, "checks", "links", "stayed"),
		"openedOutside": len(t.notes["openExternal"]),
	}
	out["webgl"] = get(r, "checks", "webgl")
	return out
}
