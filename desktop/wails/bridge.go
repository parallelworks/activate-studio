package main

// What the page can ask of the shell. Bound to Wails, so the shell's own
// start page and, in assetserver mode, the Studio page reach it as
// window.go.main.Bridge.<Method>(...) returning a Promise; in direct and
// localproxy modes the shim reaches the same methods by speaking Wails'
// message protocol itself, since Wails loads its runtime only into pages on
// its own origin. The shim (shell/shim.js)
// turns window.webkit.messageHandlers.studioShell.postMessage({action, ...})
// into these calls, which is the contract web/src/nativeShell.ts expects
// from the macOS window.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type Bridge struct{ app *App }

// ShellState is what the start page shows: starting, ready (with the
// address to open) or failed (with the server's last lines).
func (b *Bridge) ShellState() ShellState { return b.app.State() }

// Retry starts the server again after it failed.
func (b *Bridge) Retry() { b.app.retry() }

// OpenLogs opens the folder with the server's log in the file manager.
// (Wails' BrowserOpenURL takes web addresses only.)
func (b *Bridge) OpenLogs() { openFolder(filepath.Join(b.app.cfg.DataDir, "logs")) }

// ChooseFolder opens the system folder chooser at start. It returns the
// folder chosen, or "" when the person cancels (the shim turns "" into
// null, as the macOS window answers). In the self test it returns the test
// folder without opening a dialog, which nobody could click in CI.
func (b *Bridge) ChooseFolder(start, message, prompt string) (string, error) {
	if t := b.app.test; t != nil {
		// The first request answers with the test folder; a later one
		// opens the real dialog, which the self test photographs and closes.
		if t.chooseFolderCall(start, message, prompt) && b.app.cfg.TestFolder != "" {
			return b.app.cfg.TestFolder, nil
		}
		go t.closeDialogSoon()
	}
	if strings.HasPrefix(start, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			start = filepath.Join(home, strings.TrimPrefix(start, "~"))
		}
	}
	title := message
	if title == "" {
		title = "Choose a folder."
	}
	// Wails v2 has no field for the button's label (prompt); GTK and
	// Windows show their own "Select Folder".
	defer holdFocus(b.app.cfg.FocusGuard)()
	return wruntime.OpenDirectoryDialog(b.app.ctx, wruntime.OpenDialogOptions{
		DefaultDirectory:     start,
		Title:                title,
		CanCreateDirectories: true,
	})
}

// StudioMoved: the server saved another port and is restarting on it.
// The window follows to that port, opening hash there, once it answers.
func (b *Bridge) StudioMoved(port int, hash string) (bool, error) {
	if port < 1024 || port > 65535 {
		return false, errors.New("not a port")
	}
	if hash != "" && !strings.HasPrefix(hash, "#") {
		hash = ""
	}
	b.app.followTo(port, hash)
	return true, nil
}

// Relaunch: on macOS the app quits and opens again so an updated copy
// runs. Here the server is a child of the shell, so the shell restarts it
// and reloads the page; a shell that replaced its own binary would also
// start a new copy of itself and exit (not done in the spike).
func (b *Bridge) Relaunch() (bool, error) {
	b.app.relaunch()
	return true, nil
}

// OpenExternal opens a page outside the Studio in the default browser.
func (b *Bridge) OpenExternal(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "mailto") {
		return errors.New("not a web address")
	}
	if b.app.test != nil {
		b.app.test.note("openExternal", raw)
		if !b.app.cfg.TestOpenReal {
			return nil
		}
	}
	wruntime.BrowserOpenURL(b.app.ctx, u.String())
	return nil
}

// SaveDownload writes a file the page made (a blob: or data: link with a
// download attribute, as Export CSV makes) to the Downloads folder, under a
// name not taken yet, and returns its path.
func (b *Bridge) SaveDownload(name, dataBase64 string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(dataBase64)
	if err != nil {
		return "", err
	}
	path, err := saveInDownloads(name, data)
	if err == nil && b.app.test != nil {
		b.app.test.note("saveDownload", path)
	}
	return path, err
}

// SaveURL downloads a path on the Studio's server (a same-origin link
// with a download attribute) to the Downloads folder.
func (b *Bridge) SaveURL(path, name string) (string, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return "", errors.New("not a path on the Studio")
	}
	res, err := (&http.Client{Timeout: 5 * time.Minute}).Get(fmt.Sprintf("http://127.0.0.1:%d%s", b.app.sup.Port(), path))
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the Studio answered %s", res.Status)
	}
	if name == "" {
		name = filepath.Base(path)
	}
	dest, err := uniqueDownloadPath(name)
	if err != nil {
		return "", err
	}
	f, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.ReadFrom(res.Body); err != nil {
		return "", err
	}
	return dest, nil
}

// TestHello, TestReport and TestLog are the self test's (selftest.go);
// outside test mode they refuse.
func (b *Bridge) TestHello(info map[string]any) (map[string]any, error) {
	if b.app.test == nil {
		return nil, errors.New("not in test mode")
	}
	return b.app.test.hello(info), nil
}

func (b *Bridge) TestReport(phase string, data map[string]any) error {
	if b.app.test == nil {
		return errors.New("not in test mode")
	}
	b.app.test.report(phase, data)
	return nil
}

func (b *Bridge) TestLog(msg string) {
	if b.app.test != nil {
		b.app.test.logf("page: %s", msg)
	}
}

func saveInDownloads(name string, data []byte) (string, error) {
	dest, err := uniqueDownloadPath(name)
	if err != nil {
		return "", err
	}
	return dest, os.WriteFile(dest, data, 0o644)
}

// uniqueDownloadPath is name in the Downloads folder, numbered as the
// macOS window numbers it ("report 2.csv") when the name is taken.
func uniqueDownloadPath(name string) (string, error) {
	dir := downloadsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || name == "." || name == "/" {
		name = "download"
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	dest := filepath.Join(dir, name)
	for n := 2; ; n++ {
		if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
			return dest, nil
		}
		dest = filepath.Join(dir, fmt.Sprintf("%s %d%s", base, n, ext))
	}
}
