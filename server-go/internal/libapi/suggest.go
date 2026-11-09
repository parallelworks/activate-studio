package libapi

import (
	"bufio"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// Suggested folders: the places on this computer people most often want
// searched, offered as libraries in one click where a library is added and
// in setup. Documents, Desktop and Downloads everywhere (on Linux where the
// XDG user directories put them), and on macOS Apple Mail's folder. A
// folder is offered when it exists and is neither the main folder nor the
// source of a library already. Only a single-user install offers them.

// Suggestion is one folder offered as a library.
type Suggestion struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Path  string `json:"path"`
	// Note says what to know before adding it; "" when nothing.
	Note string `json:"note"`
	// Access is "ok", or "full-disk-access" for a folder macOS lets the
	// app read only with Full Disk Access, which it does not have yet.
	Access string `json:"access"`
}

// candidate is a folder that may be offered.
type candidate struct {
	id, label, path, note string
	// probe reads the folder to learn whether it may be read. Only for
	// folders whose reading raises no macOS prompt (Mail's answers
	// "not permitted" until Full Disk Access is on); reading Documents,
	// Desktop or Downloads would ask the person at once.
	probe bool
}

const iCloudNote = "If iCloud Drive keeps your Desktop and Documents, files kept only in iCloud are downloaded as they are read."

// suggestionCandidates are the folders for this system and home.
func suggestionCandidates(goos, home string, xdg map[string]string) []candidate {
	dir := func(key, fallback string) string {
		if p := xdg[key]; p != "" {
			return p
		}
		return filepath.Join(home, fallback)
	}
	if goos == "darwin" {
		return []candidate{
			{id: "documents", label: "Documents", path: filepath.Join(home, "Documents"), note: iCloudNote},
			{id: "desktop", label: "Desktop", path: filepath.Join(home, "Desktop"), note: iCloudNote},
			{id: "downloads", label: "Downloads", path: filepath.Join(home, "Downloads")},
			{id: "mail", label: "Apple Mail", path: filepath.Join(home, "Library", "Mail"), probe: true,
				note: "Your Apple Mail messages and their attachments, as Mail keeps them on this Mac."},
		}
	}
	return []candidate{
		{id: "documents", label: "Documents", path: dir("XDG_DOCUMENTS_DIR", "Documents")},
		{id: "desktop", label: "Desktop", path: dir("XDG_DESKTOP_DIR", "Desktop")},
		{id: "downloads", label: "Downloads", path: dir("XDG_DOWNLOAD_DIR", "Downloads")},
	}
}

// xdgUserDirs reads ~/.config/user-dirs.dirs (XDG_CONFIG_HOME's), the
// file xdg-user-dirs writes: lines such as XDG_DOCUMENTS_DIR="$HOME/Docs".
// A directory set to $HOME itself means none.
func xdgUserDirs(home, configHome string) map[string]string {
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	out := map[string]string{}
	f, err := os.Open(filepath.Join(configHome, "user-dirs.dirs"))
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || !strings.HasPrefix(k, "XDG_") {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		switch {
		case v == "$HOME" || v == "$HOME/":
			continue
		case strings.HasPrefix(v, "$HOME/"):
			v = filepath.Join(home, v[len("$HOME/"):])
		case !filepath.IsAbs(v):
			continue
		}
		out[k] = filepath.Clean(v)
	}
	return out
}

// suggest is the offer: candidates that exist, less the main folder and
// libraries' sources (taken has their cleaned paths).
func suggest(cands []candidate, taken map[string]bool, readDir func(string) error) []Suggestion {
	out := []Suggestion{}
	for _, c := range cands {
		p := filepath.Clean(c.path)
		if taken[p] {
			continue
		}
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			continue
		}
		access := "ok"
		if c.probe {
			if err := readDir(p); err != nil {
				if !errors.Is(err, fs.ErrPermission) {
					continue
				}
				access = "full-disk-access"
			}
		}
		out = append(out, Suggestion{ID: c.id, Label: c.label, Path: p, Note: c.note, Access: access})
	}
	return out
}

func readOne(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Readdirnames(1); err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// takenPaths are the main folder and the sources of the libraries here.
func (s *Service) takenPaths() map[string]bool {
	taken := map[string]bool{}
	if s.a.Cfg.KBRoot != "" {
		taken[filepath.Clean(s.a.Cfg.KBRoot)] = true
	}
	for _, l := range s.Reg.List(s.kbLabel(), libraries.Deployment) {
		if l.SourceRoot != "" {
			taken[filepath.Clean(l.SourceRoot)] = true
		}
	}
	return taken
}

func (s *Service) suggestions(w http.ResponseWriter, r *http.Request) error {
	if s.a.Cfg.AuthEnabled() {
		return apierr.New(403, "suggested folders are for a single-user install; this Studio signs users in")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		apierr.JSON(w, 200, map[string]any{"suggestions": []Suggestion{}})
		return nil
	}
	cands := suggestionCandidates(runtime.GOOS, home, xdgUserDirs(home, os.Getenv("XDG_CONFIG_HOME")))
	apierr.JSON(w, 200, map[string]any{"suggestions": suggest(cands, s.takenPaths(), readOne)})
	return nil
}

// openFullDiskAccess opens System Settings at Privacy & Security, Full
// Disk Access, where the person turns it on for the app. macOS only.
func (s *Service) openFullDiskAccess(w http.ResponseWriter, r *http.Request) error {
	if s.a.Cfg.AuthEnabled() {
		return apierr.New(403, "this is for a single-user install; this Studio signs users in")
	}
	if runtime.GOOS != "darwin" {
		return apierr.New(404, "Full Disk Access is a macOS setting")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "/usr/bin/open", "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles").Run(); err != nil {
		return apierr.New(500, "could not open System Settings: %s", err.Error())
	}
	apierr.JSON(w, 200, map[string]bool{"opened": true})
	return nil
}
