// ACTIVATE Studio desktop shell for Linux and Windows, on Wails v2: a
// spike that puts the Studio in a native window the way the macOS app
// does (deploy/mac/shell/StudioShell.swift), with the server as a child of
// the shell.
//
// It starts the Studio's server (node server/dist/main.js) on a free
// 127.0.0.1 port, waits for GET /api/version, shows the Studio, restarts
// the server when it exits with 75 or 76, shows an error with Try Again
// when it exits otherwise, and stops it, with every process it started,
// when the window closes. The page finds the macOS window's bridge,
// window.webkit.messageHandlers.studioShell.postMessage({action, ...}),
// put there by a shim (shell/shim.js), so web/src/nativeShell.ts works
// unchanged.
//
// Environment:
//
//	STUDIO_APP_DIR         the Studio (server/dist/main.js, web/dist); default: searched
//	                       upward from the executable and the working directory
//	STUDIO_NODE            the node binary; default: node on PATH
//	STUDIO_SHELL_MODE      assetserver (default), direct or localproxy; see app.go
//	STUDIO_SHELL_DATA_DIR  per-user data (studio.env, index, logs, the web view's data)
//	STUDIO_KB_ROOT         the documents folder; default ~/Documents/ACTIVATE Studio
//	STUDIO_SHELL_TEST=1    the self test (selftest.go): checks run inside the page,
//	                       results in STUDIO_SHELL_TEST_DIR, then the shell exits;
//	                       STUDIO_SHELL_TEST_PLAN=hold starts the Studio, writes
//	                       pids.json and waits (for the hard-kill orphan check)
//
// Build: wails build -tags webkit2_41 (Linux with WebKitGTK 4.1), or
// go build -tags desktop,production,webkit2_41 -ldflags "-s -w".
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/logger"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

func main() {
	// Linux: the server's guard (pdeathsig_linux.go) is this executable.
	if maybeRunGuard() {
		return
	}
	t0 := time.Now()
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if cfg.LaunchT0 == 0 {
		cfg.LaunchT0 = t0.UnixMilli()
	}
	app := NewApp(cfg)
	if cfg.Mode == ModeLocalProxy {
		// Before Run: the window's allowed origins name this port.
		if err := app.listenLocal(); err != nil {
			fmt.Fprintln(os.Stderr, "The shell could not listen on 127.0.0.1:", err)
			os.Exit(1)
		}
	}

	err = wails.Run(&options.App{
		Title:     "ACTIVATE Studio",
		Width:     1400,
		Height:    900,
		MinWidth:  720,
		MinHeight: 480,
		AssetServer: &assetserver.Options{
			Handler: app.proxy.AssetHandler(),
		},
		OnStartup:  app.startup,
		OnDomReady: app.domReady,
		OnShutdown: app.shutdown,
		Bind:       []interface{}{app.bridge},
		// Direct and localproxy modes: the Studio page on http://127.0.0.1
		// may call the bound methods. Wails v2 refuses calls from any origin
		// but its own unless listed here.
		BindingsAllowedOrigins: app.bindingsOrigins(),
		// Copy, paste and Inspect in the page's context menu.
		EnableDefaultContextMenu: true,
		LogLevel:                 logger.INFO,
		LogLevelProduction:       logger.WARNING,
		Linux: &linux.Options{
			ProgramName:      "activate-studio",
			WebviewGpuPolicy: linux.WebviewGpuPolicyOnDemand,
		},
		Windows: &windows.Options{
			// The web view's own data (local storage, cache) with the
			// Studio's, not beside the executable.
			WebviewUserDataPath: filepath.Join(cfg.DataDir, "webview"),
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		app.sup.Stop()
		os.Exit(1)
	}
}

// bindingsOrigins: in direct mode any 127.0.0.1 port, since the server
// can move to another port while the window is open; in localproxy mode
// the shell's own listener only.
func (a *App) bindingsOrigins() string {
	switch a.cfg.Mode {
	case ModeDirect:
		return "http://127.0.0.1:*"
	case ModeLocalProxy:
		return fmt.Sprintf("http://127.0.0.1:%d", a.localPort)
	}
	return ""
}

func loadConfig() (Config, error) {
	cfg := Config{
		Mode:       envOr("STUDIO_SHELL_MODE", ModeAssetServer),
		Node:       envOr("STUDIO_NODE", "node"),
		DataDir:    envOr("STUDIO_SHELL_DATA_DIR", defaultDataDir()),
		Test:       os.Getenv("STUDIO_SHELL_TEST") == "1",
		TestPlan:   envOr("STUDIO_SHELL_TEST_PLAN", "full"),
		TestDir:    os.Getenv("STUDIO_SHELL_TEST_DIR"),
		TestFolder: os.Getenv("STUDIO_SHELL_TEST_FOLDER"),
	}
	switch cfg.Mode {
	case ModeAssetServer, ModeDirect, ModeLocalProxy:
	default:
		return cfg, fmt.Errorf("STUDIO_SHELL_MODE is %q; use assetserver, direct or localproxy", cfg.Mode)
	}
	// Wails' asset origin: WebKitGTK and WKWebView use a wails:// scheme,
	// WebView2 intercepts http://wails.localhost.
	cfg.StartOrigin = "wails://wails"
	if runtime.GOOS == "windows" {
		cfg.StartOrigin = "http://wails.localhost"
	}
	if s := os.Getenv("STUDIO_SHELL_SCREENSHOT"); s != "" {
		cfg.Screenshot = strings.Fields(s)
	}
	if s := os.Getenv("STUDIO_SHELL_TEST_DIALOG_CLOSE"); s != "" {
		cfg.DialogClose = strings.Fields(s)
	}
	cfg.TestOpenReal = os.Getenv("STUDIO_SHELL_TEST_OPEN_REAL") == "1"
	if s := os.Getenv("STUDIO_SHELL_LAUNCH_T0"); s != "" {
		cfg.LaunchT0, _ = strconv.ParseInt(s, 10, 64)
	}
	if cfg.Test && cfg.TestDir == "" {
		cfg.TestDir = filepath.Join(cfg.DataDir, "selftest")
	}
	home, _ := os.UserHomeDir()
	cfg.KBRoot = envOr("STUDIO_KB_ROOT", filepath.Join(home, "Documents", "ACTIVATE Studio"))

	cfg.AppDir = os.Getenv("STUDIO_APP_DIR")
	if cfg.AppDir == "" {
		cfg.AppDir = findAppDir()
	}
	if cfg.AppDir == "" {
		return cfg, fmt.Errorf("the Studio was not found: set STUDIO_APP_DIR to the folder holding server/dist/main.js")
	}
	if p, err := exec.LookPath(cfg.Node); err == nil {
		cfg.Node = p
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// findAppDir looks for server/dist/main.js beside and above the
// executable, then the working directory.
func findAppDir() string {
	var starts []string
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	for _, dir := range starts {
		for d := dir; ; d = filepath.Dir(d) {
			if _, err := os.Stat(filepath.Join(d, "server", "dist", "main.js")); err == nil {
				return d
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	return ""
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func joinPath(parts ...string) string { return filepath.Join(parts...) }
