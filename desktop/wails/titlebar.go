package main

// The window's title bar takes the color the page names in its theme-color
// meta tag, as the macOS window's does (deploy/mac/shell/StudioShell.swift):
// the classification banner's when one is shown, otherwise the page's ground
// in its current theme. The shim (shell/shim.js) reports the tag and each
// change to it. The color is kept in the data folder, so the next launch
// paints the bar before the page arrives.

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var barColorForm = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func barColorFile(dataDir string) string { return filepath.Join(dataDir, "run", "bar-color") }

// barInk is the title and controls' color on a bar of the given #rrggbb: the
// one with the higher contrast, by the WCAG luminance the macOS window uses.
func barInk(color string) string {
	channel := func(at int) float64 {
		v, _ := strconv.ParseUint(color[at:at+2], 16, 8)
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	l := 0.2126*channel(1) + 0.7152*channel(3) + 0.0722*channel(5)
	if 1.05/(l+0.05) >= (l+0.05)/0.05 {
		return "#ffffff"
	}
	return "#1a1a1a"
}

// barCSS styles the window's bar (useOwnTitleBar), and no dialog's. Without
// a compositor the theme frames the window in a band that is its resize
// edge, in the theme's own color; the band takes the bar's.
func barCSS(color string) string {
	ink := barInk(color)
	return fmt.Sprintf(`
.studio-bar, .studio-bar:backdrop {
  background: %[1]s; color: %[2]s; border: none; box-shadow: none;
  min-height: 30px; padding: 0 4px;
}
.studio-bar:backdrop { color: alpha(%[2]s, 0.6); }
.studio-bar button.titlebutton, .studio-bar button.titlebutton:backdrop {
  background: none; color: inherit; border: none; box-shadow: none; -gtk-icon-shadow: none;
  min-height: 22px; min-width: 22px; padding: 0; margin: 0 1px;
}
.studio-bar button.titlebutton:hover { background: alpha(%[2]s, 0.12); }
.studio-bar button.titlebutton:active { background: alpha(%[2]s, 0.22); }
.studio-window.solid-csd, .studio-window.solid-csd:backdrop {
  border-color: alpha(%[2]s, 0.15); box-shadow: inset 0 0 0 4px %[1]s;
}
`, color, ink)
}

// setBarColor paints the bar. Anything but #rrggbb is refused, since the
// value comes from the page and goes into a style sheet.
func (a *App) setBarColor(color string) error {
	color = strings.ToLower(color)
	if !barColorForm.MatchString(color) {
		return fmt.Errorf("not a color: %q", color)
	}
	a.mu.Lock()
	same := a.barColor == color
	a.barColor = color
	a.mu.Unlock()
	if same {
		return nil
	}
	paintBar(color)
	file := barColorFile(a.cfg.DataDir)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return os.WriteFile(file, []byte(color+"\n"), 0o644)
}

// restoreBarColor paints the bar the last launch ended with.
func (a *App) restoreBarColor() {
	if b, err := os.ReadFile(barColorFile(a.cfg.DataDir)); err == nil {
		_ = a.setBarColor(strings.TrimSpace(string(b)))
	}
}
