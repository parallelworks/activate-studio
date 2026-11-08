package main

// The window's side: what it shows (the start page, the Studio, the error),
// how it follows the server through restarts and port changes, and the
// shim each Studio page gets.

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed shell/shim.js
var shimJS string

//go:embed shell/selftest.js
var selftestJS string

type Config struct {
	Tree      string // the payload tree (Linux app); empty when run from a checkout
	RunScript string // Tree/studio-run.sh
	AppDir    string // holds server/dist/main.js and web/dist
	Node      string
	// GoServer and SemanticMap: the Go front server and map binary, with
	// STUDIO_GO_SERVER=1 (goServerPaths); empty otherwise.
	GoServer    string
	SemanticMap string
	DataDir     string
	KBRoot      string // the default documents folder, when studio.env names none
	// Wails' asset origin on this platform, where the start page is shown.
	StartOrigin string

	// The self test (selftest.go).
	Test       bool
	TestPlan   string // install, or hold
	TestDir    string
	TestFolder string // chooseFolder's answer
	TestSearch string // "words:file name": a search the page runs and must find
	// FocusGuard: hold the window's focus handling while a dialog is open
	// (Windows; see focusguard_windows.go).
	FocusGuard bool

	// macOS: the app bundle and its launcher (Contents/Resources/launcher.sh),
	// which starts the background server; empty elsewhere.
	Bundle   string
	Launcher string
}

type ShellState struct {
	Phase     string `json:"phase"` // starting, ready, failed
	Message   string `json:"message"`
	StudioURL string `json:"studioURL"`
}

type App struct {
	ctx    context.Context
	cfg    Config
	sup    serverRunner
	proxy  *Proxy
	bridge *Bridge
	test   *SelfTest

	inst      *instance
	localPort int
	local     *http.Server

	mu       sync.Mutex
	state    ShellState
	barColor string // the title bar's, #rrggbb (titlebar.go)
	moved    *struct {
		port int
		hash string
	}
}

func NewApp(cfg Config) *App {
	a := &App{cfg: cfg}
	a.bridge = &Bridge{app: a}
	a.proxy = NewProxy(a)
	a.state = ShellState{Phase: "starting", Message: "Starting ACTIVATE Studio…"}
	if cfg.Launcher != "" {
		a.sup = newLauncherServer(cfg, a.onServerEvent)
		if cfg.Test {
			a.test = NewSelfTest(a)
		}
		return a
	}
	a.sup = &Supervisor{
		RunScript:   cfg.RunScript,
		Node:        cfg.Node,
		Script:      filepath.Join(cfg.AppDir, "server", "dist", "main.js"),
		GoServer:    cfg.GoServer,
		SemanticMap: cfg.SemanticMap,
		AppDir:      cfg.AppDir,
		DataDir:     cfg.DataDir,
		KBRoot:      cfg.KBRoot,
		Events:      a.onServerEvent,
	}
	if cfg.Test {
		a.test = NewSelfTest(a)
	}
	return a
}

// startup runs once the window exists (Wails OnStartup).
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	wruntime.OnFileDrop(ctx, a.onFileDrop)
	// TERM, INT or HUP (a logout, kill, Ctrl-C) stop the server first.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sig
		a.sup.Stop()
		wruntime.Quit(a.ctx)
	}()
	if a.test != nil {
		a.test.start()
	}
	go a.sup.Run()
}

// show brings the window forward (a second launch asked for it).
func (a *App) show() {
	if a.ctx == nil {
		return
	}
	wruntime.WindowUnminimise(a.ctx)
	wruntime.WindowShow(a.ctx)
}

// shutdown runs after the window closed (Wails OnShutdown): the server and
// everything it started stop before the shell exits.
func (a *App) shutdown(ctx context.Context) {
	a.sup.Stop()
	if a.local != nil {
		_ = a.local.Close()
	}
	if a.test != nil {
		a.test.finish()
	}
}

func (a *App) State() ShellState {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.state
	s.StudioURL = a.studioURL()
	return s
}

func (a *App) setState(s ShellState) {
	a.mu.Lock()
	a.state = s
	a.mu.Unlock()
}

func (a *App) onServerEvent(e ServerEvent) {
	if a.test != nil {
		a.test.serverEvent(e)
	}
	switch e.Kind {
	case "starting":
		a.proxy.SetServer(e.Port, false)
		a.setState(ShellState{Phase: "starting", Message: "Starting ACTIVATE Studio…"})
	case "ready":
		a.proxy.SetServer(e.Port, true)
		a.setState(ShellState{Phase: "ready"})
		a.mu.Lock()
		moved := a.moved
		if moved != nil && moved.port == e.Port {
			a.moved = nil
		}
		a.mu.Unlock()
		switch {
		case moved != nil && moved.port == e.Port:
			a.showStudio(moved.hash)
		case e.Reason == "launch" || e.Reason == "retry":
			// The start page is showing; it sees "ready" and opens the
			// Studio itself. Opening it from here too would load the page
			// twice.
		case e.Reason == "exit-75":
			// A setting changed. The page that changed it waits for the
			// server and carries on, as it does in the macOS window; a
			// reload would send setup back to its first step.
		default:
			// After a restart (exit 76, or relaunch) the same page
			// loads again from the server now answering.
			a.reloadStudio()
		}
	case "failed":
		a.proxy.SetServer(e.Port, false)
		a.setState(ShellState{Phase: "failed", Message: e.Message})
		a.showStart()
	}
}

func (a *App) retry() {
	a.setState(ShellState{Phase: "starting", Message: "Starting ACTIVATE Studio…"})
	a.sup.Retry()
}

func (a *App) relaunch() {
	// After the reply has reached the page.
	go func() {
		time.Sleep(300 * time.Millisecond)
		// An update that changed this window's own program, or one that
		// replaces the whole app (relaunchAsked): open the new one
		// (reopen.go). Otherwise only the server starts again.
		if a.cfg.Bundle != "" {
			// macOS: the bundle is opened again once this process has
			// ended, which starts the copy the update put in place.
			if a.reopenBundle() {
				wruntime.Quit(a.ctx)
				return
			}
		} else if (a.shellChanged() || a.relaunchAsked()) && a.reopen(a.inst) {
			a.sup.Stop()
			wruntime.Quit(a.ctx)
			return
		}
		a.sup.Restart("relaunch")
	}()
}

// followTo: the server is restarting on port; open hash there once it
// answers (onServerEvent), or now if it already does.
func (a *App) followTo(port int, hash string) {
	a.mu.Lock()
	a.moved = &struct {
		port int
		hash string
	}{port, hash}
	ready := a.state.Phase == "ready" && a.sup.Port() == port
	if ready {
		a.moved = nil
	}
	a.mu.Unlock()
	if ready {
		a.showStudio(hash)
	}
}

// studioOrigin is the origin the Studio's pages are shown from: the
// shell's own listener (proxy.go).
func (a *App) studioOrigin() string { return fmt.Sprintf("http://127.0.0.1:%d", a.localPort) }

func (a *App) studioURL() string { return a.studioOrigin() + "/" }

// showStudio opens the Studio at hash. When the window already shows that
// page (assetserver and localproxy: the origin does not change when the
// server moves) only the hash would change, so the page is reloaded.
func (a *App) showStudio(hash string) {
	a.execJS(fmt.Sprintf(`(function (u) {
  var t = new URL(u);
  var same = t.origin === location.origin && t.pathname === location.pathname && t.search === location.search;
  location.replace(u);
  if (same) location.reload();
})(%s)`, jsString(a.studioURL()+hash)))
}

// reloadStudio loads the Studio page again, keeping its place (the hash),
// from the server's current address.
func (a *App) reloadStudio() {
	a.execJS(fmt.Sprintf(`(function (u) {
  if (window.__studioShell && location.href.indexOf(u) === 0) location.reload();
  else location.replace(u + (window.__studioShell ? location.hash : ''));
})(%s)`, jsString(a.studioURL())))
}

func (a *App) showStart() {
	a.execJS(fmt.Sprintf("location.replace(%s)", jsString(a.cfg.StartOrigin+"/__shell/")))
}

func (a *App) execJS(js string) {
	if a.ctx != nil {
		wruntime.WindowExecJS(a.ctx, js)
	}
}

// shimScript is the shim, with the self test's page side appended when it
// runs.
func (a *App) shimScript() string {
	cfg := map[string]any{"test": a.test != nil}
	b, _ := json.Marshal(cfg)
	s := strings.Replace(shimJS, "__STUDIO_SHELL_CONFIG__", string(b), 1)
	if a.test != nil {
		s += "\n" + selftestJS
	}
	return s
}

// listenLocal starts the listener the window opens, on 127.0.0.1. It takes
// the port it used last (kept in the data folder) when that is free, so the
// page's origin, and with it what the page saved in the browser's storage,
// is the same from one launch to the next, and a free port otherwise.
func (a *App) listenLocal() error {
	file := filepath.Join(a.cfg.DataDir, "run", "window-port")
	var l net.Listener
	var err error
	if p := readPort(file); p != 0 {
		l, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	}
	if l == nil {
		if l, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return err
		}
	}
	a.localPort = l.Addr().(*net.TCPAddr).Port
	writePort(file, a.localPort)
	a.proxy.mu.Lock()
	a.proxy.localHost = fmt.Sprintf("127.0.0.1:%d", a.localPort)
	a.proxy.mu.Unlock()
	a.local = &http.Server{Handler: a.proxy.LocalHandler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = a.local.Serve(l) }()
	return nil
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
