package main

// The install check (STUDIO_SHELL_TEST=1), for CI, where nobody can click:
// the Studio's page in this window checks itself, phase by phase, and the
// shell moves the server and the window between phases the way a person
// would, then writes results.json in STUDIO_SHELL_TEST_DIR and closes the
// window, which stops the server. The page side is shell/selftest.js,
// appended to the shim.
//
// Phases (plan "install"):
//
//	checks         the Studio drew (its app replaced the loading placeholder), the
//	               bridge answers, an index build finds a search term
//	relaunch       the page asks the shell to relaunch, as UpdatedNotice does after
//	               an update; the shell restarts the server and reloads the page
//	afterRelaunch  the page drew again from the restarted server
//	move           the page saves another port (Settings, Server): the server exits 75
//	               and the page tells the shell, which follows it
//	afterMove      the window is on the Studio at the new port; the window closes
//
// Plan "hold" does the first phase, writes results.json and, if the Studio
// drew, a "ready" file, and waits, so CI can end the shell in other ways (TERM, a second launch,
// SIGKILL) and look for processes left behind.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type SelfTest struct {
	app *App
	dir string

	mu       sync.Mutex
	phase    string
	handed   bool
	results  map[string]any
	events   []map[string]any
	notes    map[string][]any
	finished bool
	logFile  *os.File
	movePort int
}

func NewSelfTest(a *App) *SelfTest {
	t := &SelfTest{
		app:      a,
		dir:      a.cfg.TestDir,
		results:  map[string]any{},
		notes:    map[string][]any{},
		phase:    "checks",
		movePort: freePort(),
	}
	_ = os.MkdirAll(t.dir, 0o755)
	_ = os.Remove(filepath.Join(t.dir, "ready"))
	t.logFile, _ = os.OpenFile(filepath.Join(t.dir, "selftest.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	return t
}

func (t *SelfTest) logf(format string, args ...any) {
	line := fmt.Sprintf("%s %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
	if t.logFile != nil {
		_, _ = t.logFile.WriteString(line)
	}
	fmt.Fprint(os.Stderr, "selftest: "+line)
}

func (t *SelfTest) note(kind string, v any) {
	t.mu.Lock()
	t.notes[kind] = append(t.notes[kind], v)
	t.mu.Unlock()
	t.logf("note %s %v", kind, v)
}

// start sets the overall time limit.
func (t *SelfTest) start() {
	go func() {
		time.Sleep(400 * time.Second)
		t.mu.Lock()
		done := t.finished || t.phase == "hold"
		t.mu.Unlock()
		if !done {
			t.logf("time limit reached in phase %s", t.currentPhase())
			t.set("timeout", true)
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
	t.mu.Unlock()
	t.logf("server %s port=%d reason=%s", e.Kind, e.Port, e.Reason)
}

// hello is the page's question on each load. A phase is handed out once; a
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
	words, file, _ := strings.Cut(t.app.cfg.TestSearch, ":")
	return map[string]any{
		"phase":      phase,
		"movePort":   t.movePort,
		"searchWord": words,
		"searchFile": file,
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
		if t.app.cfg.TestPlan == "hold" {
			t.setPhase("hold")
			// macOS: the window's own state, once the page has had a moment
			// to name its bar color.
			time.Sleep(2 * time.Second)
			if r := windowReport(); r != nil {
				t.mu.Lock()
				t.results["window"] = r
				t.mu.Unlock()
			}
			t.write()
			// Ready only when the Studio drew.
			if get(t.results, "checks", "appMounted", "mounted") == true {
				_ = os.WriteFile(filepath.Join(t.dir, "ready"), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644)
				go t.watchReopen()
			} else {
				t.logf("the Studio did not draw; not ready")
			}
			return
		}
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
		t.setPhase("done")
		t.quit()
	}
}

// watchReopen (plan hold): a file named reopen in the test folder asks for
// the relaunch the page asks for after an update that changed the window's
// program, a file named eval.js is run in the page, and a file named
// window-report asks for the window's state in window-report.json.
func (t *SelfTest) watchReopen() {
	f := filepath.Join(t.dir, "reopen")
	js := filepath.Join(t.dir, "eval.js")
	for {
		if _, err := os.Stat(f); err == nil {
			_ = os.Remove(f)
			t.logf("relaunch asked for")
			t.app.relaunch()
			return
		}
		// A script for the page, from a check that drives it (the window
		// screenshots, deploy/mac/window_shots.sh); evaluated is written
		// once it has been handed to the page.
		if b, err := os.ReadFile(js); err == nil {
			_ = os.Remove(js)
			t.logf("eval %d bytes", len(b))
			t.app.execJS(string(b))
			_ = os.WriteFile(filepath.Join(t.dir, "evaluated"), []byte(time.Now().Format(time.RFC3339Nano)+"\n"), 0o644)
		}
		// A fresh report of the window's own state (its controls move
		// with the banner), written to window-report.json.
		if _, err := os.Stat(filepath.Join(t.dir, "window-report")); err == nil {
			_ = os.Remove(filepath.Join(t.dir, "window-report"))
			if r := windowReport(); r != nil {
				b, _ := json.MarshalIndent(r, "", " ")
				_ = os.WriteFile(filepath.Join(t.dir, "window-report.json"), b, 0o644)
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// quit closes the window, as a person would; OnShutdown stops the server.
func (t *SelfTest) quit() {
	t.logf("closing the window")
	t.write()
	if t.app.ctx != nil {
		wruntime.Quit(t.app.ctx)
	}
}

// finish runs at shutdown: the last write of the results.
func (t *SelfTest) finish() {
	t.mu.Lock()
	t.finished = true
	t.mu.Unlock()
	t.write()
}

func (t *SelfTest) write() {
	t.mu.Lock()
	out := map[string]any{
		"shell":   map[string]any{"version": version, "webkit": webkitAPI},
		"plan":    t.app.cfg.TestPlan,
		"phase":   t.phase,
		"results": t.results,
		"events":  t.events,
		"notes":   t.notes,
	}
	t.mu.Unlock()
	out["summary"] = t.summary()
	b, _ := json.MarshalIndent(out, "", " ")
	_ = os.WriteFile(filepath.Join(t.dir, "results.json"), b, 0o644)
}

func get(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func same(a, b any) bool { return a != nil && b != nil && fmt.Sprint(a) == fmt.Sprint(b) }

// summary: each item true or false, and ok when all are.
func (t *SelfTest) summary() map[string]any {
	t.mu.Lock()
	r := t.results
	t.mu.Unlock()
	out := map[string]any{}
	out["appMounted"] = get(r, "checks", "appMounted", "mounted") == true
	out["bridge"] = get(r, "checks", "bridge", "present") == true &&
		same(get(r, "checks", "bridge", "chooseFolder"), t.app.cfg.TestFolder) &&
		get(r, "checks", "bridge", "unknownRejects") != nil
	out["version"] = get(r, "checks", "version", "status") == float64(200)
	out["search"] = get(r, "checks", "search", "found") == true
	all := out["appMounted"] == true && out["bridge"] == true && out["version"] == true && out["search"] == true
	if t.app.cfg.TestPlan != "hold" {
		out["relaunch"] = get(r, "relaunchAnswer", "answer") == true &&
			get(r, "afterRelaunch", "appMounted", "mounted") == true && get(r, "afterRelaunch", "version", "status") == float64(200)
		out["moved"] = get(r, "afterMove", "appMounted", "mounted") == true &&
			same(get(r, "afterMove", "serverPort"), get(r, "move", "portChange", "port")) &&
			get(r, "afterMove", "hash") == "#view=settings"
		all = all && out["relaunch"] == true && out["moved"] == true
	}
	out["timeout"] = r["timeout"] == true
	out["ok"] = all && r["timeout"] != true
	return out
}
