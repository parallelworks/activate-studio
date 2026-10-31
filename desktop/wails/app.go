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
	"strings"
	"sync"
	"syscall"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	// The Studio is proxied through Wails' AssetServer: the page is on
	// Wails' origin, with its runtime and bindings.
	ModeAssetServer = "assetserver"
	// The window opens http://127.0.0.1:PORT, the Studio's server itself;
	// the shim is run in the page after it loads.
	ModeDirect = "direct"
	// The window opens a reverse proxy the shell serves on a 127.0.0.1
	// port of its own, which puts the shim first in each page; the bridge
	// is Wails' message channel, allowed for that origin only.
	ModeLocalProxy = "localproxy"
)

//go:embed shell/shim.js
var shimJS string

//go:embed shell/selftest.js
var selftestJS string

type Config struct {
	Mode         string
	AppDir       string // holds server/dist/main.js and web/dist
	Node         string
	DataDir      string
	KBRoot       string
	StartOrigin  string // Wails' asset origin on this platform
	Test         bool
	TestPlan     string // full, or hold (start, write pids.json, wait)
	TestDir      string
	TestFolder   string   // chooseFolder's answer in the self test
	Screenshot   []string // the self test's screenshot command; the file name is appended
	DialogClose  []string // closes the real folder dialog in the self test
	TestOpenReal bool     // the self test opens outside links for real
	LaunchT0     int64    // epoch ms the process was launched, for the cold start
}

type ShellState struct {
	Phase     string `json:"phase"` // starting, ready, failed
	Message   string `json:"message"`
	StudioURL string `json:"studioURL"`
	Mode      string `json:"mode"`
}

type App struct {
	ctx    context.Context
	cfg    Config
	sup    *Supervisor
	proxy  *Proxy
	bridge *Bridge
	test   *SelfTest

	localPort int
	local     *http.Server

	mu    sync.Mutex
	state ShellState
	moved *struct {
		port int
		hash string
	}
}

func NewApp(cfg Config) *App {
	a := &App{cfg: cfg}
	a.bridge = &Bridge{app: a}
	a.proxy = NewProxy(a)
	a.state = ShellState{Phase: "starting", Message: "Starting ACTIVATE Studio…", Mode: cfg.Mode}
	a.sup = &Supervisor{
		Node:    cfg.Node,
		Script:  joinPath(cfg.AppDir, "server", "dist", "main.js"),
		AppDir:  cfg.AppDir,
		DataDir: cfg.DataDir,
		KBRoot:  cfg.KBRoot,
		Events:  a.onServerEvent,
	}
	if cfg.Test {
		a.test = NewSelfTest(a)
		a.sup.Extra = a.test.nodeArgs()
	}
	return a
}

// startup runs once the window exists (Wails OnStartup).
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if a.test != nil {
		a.test.mark("startup")
	}
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

// domReady runs after every page load in the window (Wails OnDomReady
// fires on WebKitGTK's load-finished and WebView2's NavigationCompleted,
// whatever the page's origin). In direct mode this is the only place the
// shim can go into a page served from http://127.0.0.1: Wails v2 has no
// script-at-document-start hook, so the shim arrives after the page's own
// scripts ran.
func (a *App) domReady(ctx context.Context) {
	if a.cfg.Mode != ModeDirect {
		return
	}
	a.execJS(fmt.Sprintf("if (location.origin === %s) { %s }", jsString(a.studioOrigin()), a.shimScript()))
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
	s.Mode = a.cfg.Mode
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
		default:
			// After a restart (exit 75 or 76, or relaunch) the same page
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

// studioOrigin is the origin the Studio's pages are shown from in this mode.
func (a *App) studioOrigin() string {
	switch a.cfg.Mode {
	case ModeDirect:
		return fmt.Sprintf("http://127.0.0.1:%d", a.sup.Port())
	case ModeLocalProxy:
		return fmt.Sprintf("http://127.0.0.1:%d", a.localPort)
	}
	return a.cfg.StartOrigin
}

func (a *App) studioURL() string { return a.studioOrigin() + "/" }

func (a *App) showStudio(hash string) {
	a.execJS(fmt.Sprintf("location.replace(%s)", jsString(a.studioURL()+hash)))
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

// shimScript is the shim with this mode's settings: which transport
// reaches the shell, and the self test when it runs.
func (a *App) shimScript() string {
	transport := map[string]string{ModeAssetServer: "wails", ModeDirect: "ipc", ModeLocalProxy: "ipc"}[a.cfg.Mode]
	cfg := map[string]any{
		"mode":      a.cfg.Mode,
		"transport": transport,
		"test":      a.test != nil,
	}
	b, _ := json.Marshal(cfg)
	s := strings.Replace(shimJS, "__STUDIO_SHELL_CONFIG__", string(b), 1)
	if a.test != nil {
		s += "\n" + selftestJS
	}
	return s
}

// listenLocal starts the localproxy listener on a free 127.0.0.1 port.
func (a *App) listenLocal() error {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	a.localPort = l.Addr().(*net.TCPAddr).Port
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
