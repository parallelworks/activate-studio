//go:build darwin

package main

// The macOS app's window in Go, in place of the Swift shell
// (deploy/mac/shell/StudioShell.swift; the feature list is in
// docs/MACOS.md, "The window"). As there, the server is not a child of the
// window: the app's launcher (Contents/Resources/launcher.sh) starts it as a
// LaunchAgent, or where none can be loaded as a run loop of its own
// (studio-server detach), and prints the address; the window follows it
// and leaves it running when it closes. The launcher is also what hands
// over from an older copy: a server still running another copy's build is
// restarted from this one before the window shows it.
//
// What the window adds to the shared code: the bundle's own paths and data
// folder, the launcher's command line flags passed through (--install,
// --stop, --uninstall, --help), the app menu (About, Check for Updates, Run
// Setup, Stop Background Server, Uninstall, Hide, Quit), Edit, View and
// Window menus, the About panel, and the reopen after an update, which
// opens the bundle again once this process has ended.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const bundleID = "com.parallelworks.activate-studio"

// The launcher's flags the app passes on, as `ACTIVATE Studio --stop` does
// from a terminal.
var passThrough = map[string]bool{"--install": true, "--stop": true, "--uninstall": true, "--help": true, "-h": true}

// bundlePaths: the .app this executable is in (Contents/MacOS/<name>) and
// its launcher, or "" when it is not in a bundle with one.
func bundlePaths() (bundle, launcher string) {
	exe, err := os.Executable()
	if err != nil {
		return "", ""
	}
	contents := filepath.Dir(filepath.Dir(exe))
	launcher = filepath.Join(contents, "Resources", "launcher.sh")
	if filepath.Base(contents) != "Contents" {
		return "", ""
	}
	if _, err := os.Stat(launcher); err != nil {
		return "", ""
	}
	return filepath.Dir(contents), launcher
}

// maybePassThrough runs the launcher with the command line's flag, and
// reports whether it did (the process then exits with its status).
func maybePassThrough() (int, bool) {
	if len(os.Args) < 2 || !passThrough[os.Args[1]] {
		return 0, false
	}
	_, launcher := bundlePaths()
	if launcher == "" {
		fmt.Fprintln(os.Stderr, "this program is not inside ACTIVATE Studio.app")
		return 2, true
	}
	cmd := exec.Command("/bin/bash", append([]string{launcher}, os.Args[1:]...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if x, ok := err.(*exec.ExitError); ok {
			return x.ExitCode(), true
		}
		fmt.Fprintln(os.Stderr, err)
		return 1, true
	}
	return 0, true
}

// platformConfig fills in the bundle's paths and the macOS data folder.
func platformConfig(cfg *Config) {
	cfg.Bundle, cfg.Launcher = bundlePaths()
	if cfg.Bundle != "" {
		cfg.AppDir = filepath.Join(cfg.Bundle, "Contents", "Resources", "app")
	}
}

var urlInOutput = regexp.MustCompile(`http://127\.0\.0\.1:(\d+)/`)

// launcherServer runs the launcher, takes the port it prints, and follows
// the server there: away (an update's or a setting's restart, under launchd
// or the run loop) shows the start page until it answers again.
type launcherServer struct {
	cfg    Config
	events func(ServerEvent)

	mu      sync.Mutex
	port    int
	stopped bool
	wake    chan string // Retry and Restart
	done    chan struct{}
}

func newLauncherServer(cfg Config, events func(ServerEvent)) serverRunner {
	return &launcherServer{cfg: cfg, events: events, wake: make(chan string, 1), done: make(chan struct{})}
}

func (l *launcherServer) Port() int { l.mu.Lock(); defer l.mu.Unlock(); return l.port }

func (l *launcherServer) Run() {
	reason := "launch"
	for {
		l.events(ServerEvent{Kind: "starting", Port: l.Port(), Reason: reason})
		port, out, err := l.launch()
		if l.isStopped() {
			return
		}
		if err != nil {
			l.events(ServerEvent{Kind: "failed", Port: port, Reason: reason, Message: out})
			select {
			case reason = <-l.wake:
				continue
			case <-l.done:
				return
			}
		}
		l.mu.Lock()
		l.port = port
		l.mu.Unlock()
		l.events(ServerEvent{Kind: "ready", Port: port, Reason: reason})
		next, ok := l.follow(port)
		if !ok {
			return
		}
		reason = next
	}
}

// launch runs launcher.sh as the Swift shell did (STUDIO_NO_BROWSER=1:
// it prints the address in place of opening a browser) and returns the
// port, or its output when it fails.
func (l *launcherServer) launch() (int, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/bash", l.cfg.Launcher)
	cmd.Env = append(os.Environ(), "STUDIO_NO_BROWSER=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	out := strings.TrimSpace(buf.String())
	if m := urlInOutput.FindStringSubmatch(out); m != nil && err == nil {
		p, _ := strconv.Atoi(m[1])
		return p, out, nil
	}
	if err == nil {
		err = fmt.Errorf("the launcher printed no address")
	}
	if out == "" {
		out = err.Error()
	}
	return 0, out, err
}

// follow polls the server every 1.5 s, as the Swift window's reload retry
// did. A server away for long, or on another port (studio.env), runs the
// launcher again, which starts it if it is not running.
func (l *launcherServer) follow(port int) (string, bool) {
	away := time.Time{}
	tick := time.NewTicker(1500 * time.Millisecond)
	defer tick.Stop()
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		select {
		case <-l.done:
			return "", false
		case r := <-l.wake:
			return r, true
		case <-tick.C:
		}
		res, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/version", port))
		if err == nil {
			res.Body.Close()
		}
		if err == nil && res.StatusCode < 500 {
			if !away.IsZero() {
				away = time.Time{}
				l.events(ServerEvent{Kind: "ready", Port: port, Reason: "back"})
			}
			continue
		}
		if away.IsZero() {
			away = time.Now()
			l.events(ServerEvent{Kind: "starting", Port: port, Reason: "away"})
		}
		if time.Since(away) > 20*time.Second || settingsPort(l.cfg.DataDir) != port {
			return "relaunch", true
		}
	}
}

// settingsPort is the PORT studio.env names (4180 when none).
func settingsPort(dataDir string) int {
	if p, err := strconv.Atoi(readSettings(filepath.Join(dataDir, "studio.env"))["PORT"]); err == nil && p > 0 {
		return p
	}
	return 4180
}

func (l *launcherServer) isStopped() bool { l.mu.Lock(); defer l.mu.Unlock(); return l.stopped }

func (l *launcherServer) poke(reason string) {
	select {
	case l.wake <- reason:
	default:
	}
}

func (l *launcherServer) Restart(reason string) { l.poke(reason) }
func (l *launcherServer) Retry()                { l.poke("retry") }

// Stop stops following; the background server keeps running, as it did
// when the Swift window closed (Stop Background Server stops it).
func (l *launcherServer) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.stopped {
		l.stopped = true
		close(l.done)
	}
}

// reopenBundle opens the bundle again once this process has ended, from a
// process of its own, which is how the Swift window's relaunch worked: the
// copy the update put at the bundle's path is the one that opens.
func (a *App) reopenBundle() bool {
	if a.cfg.Bundle == "" {
		return false
	}
	a.sup.Stop()
	if a.inst != nil {
		a.inst.Release()
	}
	script := `while kill -0 "$1" 2>/dev/null; do sleep 0.2; done; exec /usr/bin/open "$2"`
	cmd := exec.Command("/bin/sh", "-c", script, "sh", strconv.Itoa(os.Getpid()), a.cfg.Bundle)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return false
	}
	go func() { _ = cmd.Wait() }()
	return true
}

// runLauncher runs the launcher with args and returns its output.
func (a *App) runLauncher(args ...string) (string, error) {
	cmd := exec.Command("/bin/bash", append([]string{a.cfg.Launcher}, args...)...)
	cmd.Env = append(os.Environ(), "STUDIO_NO_BROWSER=1")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// appMenu is the menu bar of the Swift window.
func appMenu(a *App) *menu.Menu {
	if a.cfg.Bundle == "" {
		return nil
	}
	m := menu.NewMenu()
	app := m.AddSubmenu(windowTitle)
	app.AddText("About "+windowTitle, nil, func(*menu.CallbackData) { a.about() })
	app.AddSeparator()
	// The page's own Check for Updates (web/src: the ade:check-updates event).
	app.AddText("Check for Updates…", nil, func(*menu.CallbackData) {
		a.execJS(`window.dispatchEvent(new CustomEvent('ade:check-updates'))`)
	})
	app.AddText("Run Setup…", nil, func(*menu.CallbackData) { a.execJS(`location.hash = '#view=settings:setup'`) })
	app.AddSeparator()
	app.AddText("Stop Background Server", nil, func(*menu.CallbackData) { a.stopBackground() })
	app.AddText("Uninstall "+windowTitle+"…", nil, func(*menu.CallbackData) { a.uninstall() })
	app.AddSeparator()
	app.AddText("Hide "+windowTitle, keys.CmdOrCtrl("h"), func(*menu.CallbackData) { wruntime.Hide(a.ctx) })
	app.AddText("Show All", nil, func(*menu.CallbackData) { wruntime.Show(a.ctx) })
	app.AddSeparator()
	app.AddText("Quit "+windowTitle, keys.CmdOrCtrl("q"), func(*menu.CallbackData) { wruntime.Quit(a.ctx) })
	m.Append(menu.EditMenu())
	view := m.AddSubmenu("View")
	view.AddText("Reload", keys.CmdOrCtrl("r"), func(*menu.CallbackData) { a.reloadStudio() })
	view.AddText("Actual Size", keys.CmdOrCtrl("0"), func(*menu.CallbackData) { a.execJS(`document.body.style.zoom = ''`) })
	view.AddText("Zoom In", keys.CmdOrCtrl("="), func(*menu.CallbackData) {
		a.execJS(`document.body.style.zoom = String((parseFloat(document.body.style.zoom) || 1) * 1.1)`)
	})
	view.AddText("Zoom Out", keys.CmdOrCtrl("-"), func(*menu.CallbackData) {
		a.execJS(`document.body.style.zoom = String((parseFloat(document.body.style.zoom) || 1) / 1.1)`)
	})
	view.AddSeparator()
	view.AddText("Enter Full Screen", keys.Combo("f", keys.CmdOrCtrlKey, keys.ControlKey), func(*menu.CallbackData) {
		if wruntime.WindowIsFullscreen(a.ctx) {
			wruntime.WindowUnfullscreen(a.ctx)
		} else {
			wruntime.WindowFullscreen(a.ctx)
		}
	})
	m.Append(menu.WindowMenu())
	return m
}

func (a *App) about() {
	v := version
	if b, err := os.ReadFile(filepath.Join(a.cfg.AppDir, "build-info.json")); err == nil {
		if m := regexp.MustCompile(`"version":\s*"([^"]*)"`).FindSubmatch(b); m != nil {
			v = string(m[1])
		}
	}
	_, _ = wruntime.MessageDialog(a.ctx, wruntime.MessageDialogOptions{
		Type:    wruntime.InfoDialog,
		Title:   windowTitle,
		Message: "Version " + v + "\n\nBuilt and maintained by Parallel Works.\nhttps://parallelworks.com",
	})
}

// stopBackground stops the server and quits, as the Swift menu item did.
func (a *App) stopBackground() {
	ok, _ := wruntime.MessageDialog(a.ctx, wruntime.MessageDialogOptions{
		Type:          wruntime.QuestionDialog,
		Title:         "Stop the background server?",
		Message:       "ACTIVATE Studio stops until you open it again.",
		Buttons:       []string{"Stop", "Cancel"},
		DefaultButton: "Stop",
		CancelButton:  "Cancel",
	})
	if ok != "Stop" {
		return
	}
	a.sup.Stop()
	_, _ = a.runLauncher("--stop")
	wruntime.Quit(a.ctx)
}

// uninstall shows the launcher's own plan (--uninstall --dry-run) and runs
// it, with the data deleted only when asked: the Swift dialog's checkbox
// is the choice of two buttons here.
func (a *App) uninstall() {
	plan, err := a.runLauncher("--uninstall", "--dry-run")
	if err != nil {
		_, _ = wruntime.MessageDialog(a.ctx, wruntime.MessageDialogOptions{Type: wruntime.ErrorDialog, Title: "Uninstall", Message: plan})
		return
	}
	purge, _ := a.runLauncher("--uninstall", "--dry-run", "--purge")
	choice, _ := wruntime.MessageDialog(a.ctx, wruntime.MessageDialogOptions{
		Type:  wruntime.WarningDialog,
		Title: "Uninstall " + windowTitle + "?",
		Message: "This removes:\n" + planText(plan) +
			"\n\nUninstall and Delete Data also removes:\n" + planText(onlyIn(purge, plan)),
		Buttons:       []string{"Uninstall", "Uninstall and Delete Data", "Cancel"},
		DefaultButton: "Cancel",
		CancelButton:  "Cancel",
	})
	args := []string{"--uninstall"}
	switch choice {
	case "Uninstall":
	case "Uninstall and Delete Data":
		args = append(args, "--purge")
	default:
		return
	}
	a.sup.Stop()
	if out, err := a.runLauncher(args...); err != nil {
		_, _ = wruntime.MessageDialog(a.ctx, wruntime.MessageDialogOptions{Type: wruntime.ErrorDialog, Title: "Uninstall", Message: out})
		return
	}
	wruntime.Quit(a.ctx)
}

// onlyIn is the lines of a not in b.
func onlyIn(a, b string) string {
	have := map[string]bool{}
	for _, l := range strings.Split(b, "\n") {
		have[l] = true
	}
	var out []string
	for _, l := range strings.Split(a, "\n") {
		if !have[l] {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// planText is the launcher's "kind<TAB>path" lines as a list.
func planText(plan string) string {
	var b strings.Builder
	sc := bufio.NewScanner(strings.NewReader(plan))
	for sc.Scan() {
		kind, path, ok := strings.Cut(sc.Text(), "\t")
		if !ok || strings.HasPrefix(kind, "keep-") {
			continue
		}
		fmt.Fprintf(&b, "  %s\n", path)
	}
	if b.Len() == 0 {
		return "  (nothing)"
	}
	return strings.TrimRight(b.String(), "\n")
}

// macOptions: the About panel's credit, the web view's user agent and
// its own title bar, which takes the page's color (titlebar.go).
func macOptions() *mac.Options {
	return &mac.Options{
		TitleBar:   mac.TitleBarDefault(),
		Appearance: mac.DefaultAppearance,
		About: &mac.AboutInfo{
			Title:   windowTitle,
			Message: "Built and maintained by Parallel Works",
		},
		// The page reads it to know it is in the Mac app, as from the
		// Swift window (applicationNameForUserAgent).
		Preferences: &mac.Preferences{ApplicationNameForUserAgent: userAgentSuffix, FullscreenEnabled: mac.Enabled},
	}
}

// userAgentSuffix is the Swift window's user agent, which the page reads to
// know it is in the Mac app.
const userAgentSuffix = "ACTIVATEStudioMac"
