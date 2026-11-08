package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// What a launcher that keeps the Studio running offers it (lifecycle.ts): a
// restart, and a settings file it reads before each start. The macOS app
// sets STUDIO_RESTARTABLE=1 and STUDIO_CONFIG_FILE; a deployment without a
// launcher sets neither, and a change that needs a restart says so instead.

// RestartExitCode asks the launcher to start the server again.
const RestartExitCode = 75

// RelaunchExitCode asks a run loop the desktop app started itself to run
// the app's own run loop again, from the copy an update put in place
// (deploy/mac/studio-run.sh, the Linux payload's studio-run.sh). The
// updates track calls RequestRestart(RelaunchExitCode) after an install.
const RelaunchExitCode = 76

// RunsWithoutAgent is lifecycle.ts's runsWithoutAgent: the macOS app
// started the server's run loop itself, without a LaunchAgent.
func RunsWithoutAgent() bool { return os.Getenv("STUDIO_SUPERVISOR") == "detached" }

// CanRestart is lifecycle.ts's canRestart.
func CanRestart() bool { return os.Getenv("STUDIO_RESTARTABLE") == "1" }

// SetStopper replaces how a restart stops the process. main.go installs
// the graceful stop; tests install their own to see the code.
func (a *App) SetStopper(fn func(code int)) {
	a.mu.Lock()
	a.stopper = fn
	a.mu.Unlock()
}

// RequestRestart stops the server soon, after the current response has
// gone out, with code (RestartExitCode, or RelaunchExitCode after an
// update replaced the app), as requestRestart does: false, and nothing
// happens, where the launcher cannot restart it.
func (a *App) RequestRestart(code int) bool {
	a.mu.Lock()
	stop := a.stopper
	a.mu.Unlock()
	if !CanRestart() || stop == nil {
		return false
	}
	time.AfterFunc(300*time.Millisecond, func() { stop(code) })
	return true
}

// LauncherConfigFile is the launcher's settings file (KEY=value lines), or
// "" without a launcher.
func LauncherConfigFile() string { return os.Getenv("STUDIO_CONFIG_FILE") }

var launcherLine = regexp.MustCompile(`^([A-Z][A-Z0-9_]*)=(.*)$`)
var launcherKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// ReadLauncherSettings is the settings the launcher passes in at the next start.
func ReadLauncherSettings() map[string]string {
	out := map[string]string{}
	file := LauncherConfigFile()
	if file == "" {
		return out
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		if m := launcherLine.FindStringSubmatch(jsTrim(line)); m != nil {
			out[m[1]] = unquote(m[2])
		}
	}
	return out
}

// launcherOrder is the keys of the file in the order they are written, so
// a save keeps the file's own order as Object.entries does.
func launcherOrder() []string {
	var keys []string
	seen := map[string]bool{}
	if b, err := os.ReadFile(LauncherConfigFile()); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if m := launcherLine.FindStringSubmatch(jsTrim(line)); m != nil && !seen[m[1]] {
				seen[m[1]] = true
				keys = append(keys, m[1])
			}
		}
	}
	return keys
}

// SaveLauncherSetting sets one setting for the next start, keeping the
// others (saveLauncherSetting): written to a temporary file, mode 0600,
// renamed over the old one.
func SaveLauncherSetting(key, value string) error {
	file := LauncherConfigFile()
	if file == "" {
		return errors.New("no launcher settings file (STUDIO_CONFIG_FILE)")
	}
	if !launcherKey.MatchString(key) {
		return fmt.Errorf("bad setting name %s", key)
	}
	if strings.ContainsAny(value, "\n\r") {
		return errors.New("a setting is one line")
	}
	settings := ReadLauncherSettings()
	order := launcherOrder()
	if _, ok := settings[key]; !ok {
		order = append(order, key)
	}
	settings[key] = value
	// Object.entries puts integer-like keys first; none of these is one.
	if len(order) != len(settings) {
		order = order[:0]
		for k := range settings {
			order = append(order, k)
		}
		sort.Strings(order)
	}
	var b strings.Builder
	for i, k := range order {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(k + "=" + quote(settings[k]))
	}
	b.WriteByte('\n')
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", file, os.Getpid())
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Values are single-quoted for a POSIX shell to source; a single quote
// inside is closed, escaped and reopened.
func quote(v string) string { return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'" }

func unquote(v string) string {
	if !strings.HasPrefix(v, "'") {
		return v
	}
	if len(v) < 2 {
		return ""
	}
	return strings.ReplaceAll(v[1:len(v)-1], `'\''`, "'")
}

// jsTrim is String.prototype.trim: JavaScript's white space and line
// terminators.
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\v', '\f', '\r', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
			return true
		}
		return r >= 0x2000 && r <= 0x200a
	})
}
