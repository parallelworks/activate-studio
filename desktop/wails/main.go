// ACTIVATE Studio desktop shell for Linux (and Windows, which shares the
// code): a native window around the Studio's server, as the macOS app's
// Swift shell is (deploy/mac/shell/StudioShell.swift).
//
// It starts the Studio's server on a loopback port, waits for
// GET /api/version, shows the Studio, starts the server again when it exits
// with 75 (a setting changed) or 76 (an update replaced the payload), shows
// an error with Try Again when it exits otherwise, and stops it, with every
// process it started, when the window closes, on TERM, and when the shell
// itself is killed. The window opens a reverse proxy the shell serves on
// 127.0.0.1 (proxy.go), which puts the shim first in each page; the shim
// (shell/shim.js) gives the page the macOS window's bridge,
// window.webkit.messageHandlers.studioShell.postMessage({action, ...}), so
// web/src/nativeShell.ts works unchanged: the folder chooser, following the
// server to another port, relaunch after an update, downloads into
// ~/Downloads and links that leave the Studio opening in the browser.
//
// One shell runs per data folder. A second launch asks the first to bring
// its window forward and exits.
//
// Environment:
//
//	STUDIO_TREE            the payload tree (studio-run.sh, node/, app/); default: the
//	                       folder above the one this executable is in, when it has
//	                       studio-run.sh. The launcher sets it to the logical path of
//	                       the install's "current" link, which survives an update.
//	STUDIO_DATA_DIR        per-user data (studio.env, index, logs, run/); default
//	                       ~/.local/share/activate-studio-data
//	STUDIO_DEFAULT_KB      the documents folder when studio.env names none;
//	                       default ~/Documents/ACTIVATE Studio
//	STUDIO_APP_DIR         without a tree: the Studio checkout (server/dist/main.js, web/dist)
//	STUDIO_NODE            without a tree: the node binary; default: node on PATH
//	STUDIO_SHELL_TEST=1    the install check (selftest.go)
//	STUDIO_GO_SERVER=1     run the Go front server (studio-server, Node behind it) in
//	                       place of Node, from app/bin in the tree or server-go/bin in a
//	                       checkout, or STUDIO_GO_SERVER_BIN; the Go semantic map
//	                       beside it is used for STUDIO_SEMANTIC_MAP_BIN. On by default
//	                       for a Go release (the tree's manifest.json says "server":
//	                       "go"), off otherwise; STUDIO_GO_SERVER=0 turns it off where
//	                       the tree still has a Node.
//
// Build (Linux): go build -tags desktop,production,webkit2_41 for WebKitGTK
// 4.1, without webkit2_41 for 4.0 (see desktop/linux/build_shell.sh).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/logger"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

const windowTitle = "ACTIVATE Studio"

// version is set at build time (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	// Linux: the server's guard (pdeathsig_linux.go) is this executable.
	if maybeRunGuard() {
		return
	}
	// Started again by a window that is closing (reopen.go): let it end first.
	if v := os.Getenv("STUDIO_WAIT_PID"); v != "" {
		os.Unsetenv("STUDIO_WAIT_PID")
		waitForPID(v)
	}
	// macOS: the launcher's flags (--install, --stop, --uninstall, --help).
	if code, ok := maybePassThrough(); ok {
		os.Exit(code)
	}
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-version") {
		fmt.Println("ACTIVATE Studio shell", version, webkitAPI)
		return
	}
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	app := NewApp(cfg)

	// One shell per data folder: a second one shows the first's window.
	inst, other, err := acquireInstance(cfg.DataDir, app.show)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if other {
		fmt.Fprintln(os.Stderr, "ACTIVATE Studio is already running; its window was brought forward.")
		return
	}
	app.inst = inst
	defer inst.Release()
	// This window is the current copy's program, so an update that asked
	// for the app to be reopened has had it (macOS: launcher.sh does the
	// same). Without this, Settings kept offering Reopen now.
	clearReopenMark(cfg.DataDir)

	// Before Run: the window's allowed origins name this port.
	if err := app.listenLocal(); err != nil {
		fmt.Fprintln(os.Stderr, "The shell could not listen on 127.0.0.1:", err)
		os.Exit(1)
	}

	useOwnTitleBar(windowTitle)
	app.restoreBarColor()
	app.restoreCompactTitle()
	app.restoreZoom()
	watchMacWindow()

	err = wails.Run(&options.App{
		Title:     windowTitle,
		Width:     1400,
		Height:    900,
		MinWidth:  720,
		MinHeight: 480,
		AssetServer: &assetserver.Options{
			Handler: app.proxy.AssetHandler(),
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind:       []interface{}{app.bridge},
		// The Studio page on the proxy's port may call the bound methods.
		// Wails v2 refuses calls from any origin but its own unless listed
		// here; the shell holds this port from before the window exists
		// until it exits.
		BindingsAllowedOrigins: fmt.Sprintf("http://127.0.0.1:%d", app.localPort),
		// Copy and paste in the page's context menu, where shim.js leaves it
		// (over text and fields).
		EnableDefaultContextMenu: true,
		// WebKitGTK gives the page nothing of a drop from a file manager, so
		// the shell takes the drop too (droppedfiles.go). The web view keeps
		// its own, for the page's drag highlights.
		DragAndDrop:        &options.DragAndDrop{EnableFileDrop: runtime.GOOS == "linux"},
		LogLevel:           logger.INFO,
		LogLevelProduction: logger.WARNING,
		Linux: &linux.Options{
			ProgramName:      "activate-studio",
			WebviewGpuPolicy: linux.WebviewGpuPolicyOnDemand,
		},
		// macOS: the Swift window's menus and About panel (launcher_darwin.go).
		Menu: appMenu(app),
		Mac:  macOptions(app.compactTitle),
		Windows: &windows.Options{
			// The web view's own data (local storage, cache) with the
			// Studio's, not beside the executable.
			WebviewUserDataPath: filepath.Join(cfg.DataDir, "webview"),
		},
	})
	// The window is gone; its page's helper processes end before the shell
	// does (webkitreap_linux.go).
	reapWebKit(5 * time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		app.sup.Stop()
		inst.Release()
		os.Exit(1)
	}
}

func loadConfig() (Config, error) {
	cfg := Config{
		DataDir:    envOr("STUDIO_DATA_DIR", defaultDataDir()),
		Test:       os.Getenv("STUDIO_SHELL_TEST") == "1",
		TestPlan:   envOr("STUDIO_SHELL_TEST_PLAN", "install"),
		TestDir:    os.Getenv("STUDIO_SHELL_TEST_DIR"),
		TestFolder: os.Getenv("STUDIO_SHELL_TEST_FOLDER"),
		TestSearch: os.Getenv("STUDIO_SHELL_TEST_SEARCH"),
		FocusGuard: true,
	}
	// Wails' asset origin: WebKitGTK uses a wails:// scheme, WebView2
	// intercepts http://wails.localhost.
	cfg.StartOrigin = "wails://wails"
	if runtime.GOOS == "windows" {
		cfg.StartOrigin = "http://wails.localhost"
	}
	if cfg.Test && cfg.TestDir == "" {
		cfg.TestDir = filepath.Join(cfg.DataDir, "selftest")
	}
	home, _ := os.UserHomeDir()
	cfg.KBRoot = envOr("STUDIO_DEFAULT_KB", filepath.Join(home, "Documents", "ACTIVATE Studio"))

	platformConfig(&cfg)
	if cfg.Launcher != "" {
		// macOS: the launcher runs the server (launcher_darwin.go).
	} else if tree := findTree(); tree != "" {
		cfg.Tree = tree
		cfg.RunScript = filepath.Join(tree, "studio-run.sh")
		cfg.AppDir = filepath.Join(tree, "app")
		cfg.Node = filepath.Join(tree, "node", "bin", "node")
	} else {
		cfg.AppDir = os.Getenv("STUDIO_APP_DIR")
		if cfg.AppDir == "" {
			cfg.AppDir = findAppDir()
		}
		if cfg.AppDir == "" {
			return cfg, fmt.Errorf("the Studio was not found: set STUDIO_TREE to the folder holding studio-run.sh, or STUDIO_APP_DIR to the folder holding server/dist/main.js")
		}
		cfg.Node = envOr("STUDIO_NODE", "node")
		if p, err := exec.LookPath(cfg.Node); err == nil {
			cfg.Node = p
		}
	}
	cfg.GoServer, cfg.SemanticMap = goServerPaths(os.Getenv, cfg.Tree, cfg.AppDir)
	if os.Getenv("STUDIO_GO_SERVER") == "1" && cfg.GoServer == "" {
		fmt.Fprintln(os.Stderr, "STUDIO_GO_SERVER=1, but studio-server was not found; starting Node")
	}
	if cfg.Tree != "" && cfg.GoServer == "" {
		if _, noNode := treeServer(cfg.Tree); noNode {
			return cfg, fmt.Errorf("%s has no Node and no Go server (app/bin/studio-server)", cfg.Tree)
		}
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// findTree is the payload tree: STUDIO_TREE, else the folder above the one
// this executable is in (<tree>/bin/activate-studio-wk41), when it holds
// studio-run.sh. The path is used as given, not resolved: an install's
// "current" link points at a different folder after an update, and the next
// start of the server must read the new one.
func findTree() string {
	if t := os.Getenv("STUDIO_TREE"); t != "" {
		if _, err := os.Stat(filepath.Join(t, "studio-run.sh")); err == nil {
			return t
		}
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	t := filepath.Dir(filepath.Dir(exe))
	if _, err := os.Stat(filepath.Join(t, "studio-run.sh")); err == nil && !strings.Contains(exe, "(deleted)") {
		return t
	}
	return ""
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
