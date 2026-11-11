package main

// The window's zoom: View, Zoom In, Zoom Out and Actual Size on macOS, and
// Ctrl+=, Ctrl+- and Ctrl+0 on Linux (shell/shim.js). It is the web view's
// own page zoom, WKWebView.pageZoom on macOS and WebKitGTK's zoom level on
// Linux, as a browser zooms: the page lays out at the new size, and the
// pointer, the page's layout and what is drawn agree. The macOS window of
// v1.190 and v1.191 zoomed with CSS zoom on the page's body instead, and in
// WebKit that left them apart: menus opened away from their buttons, by
// the zoom factor, and a dragged edge ran ahead of the pointer.
//
// The level is kept in run/window-zoom in the data folder, so the next
// launch opens at it. The shim is told the level (setZoom), since its
// title bar row and file drops are in the window's points.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// zoomLevels are the steps, as browsers have them.
var zoomLevels = []float64{0.5, 0.67, 0.75, 0.8, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2, 2.5, 3}

// nextZoom is the level after z for op: "in", "out", "reset", or "" for z
// itself (within the steps' range).
func nextZoom(z float64, op string) float64 {
	if !pageZoomWorks {
		return 1
	}
	if z <= 0 {
		z = 1
	}
	switch op {
	case "reset":
		return 1
	case "in":
		for _, l := range zoomLevels {
			if l > z+0.001 {
				return l
			}
		}
		return zoomLevels[len(zoomLevels)-1]
	case "out":
		for i := len(zoomLevels) - 1; i >= 0; i-- {
			if zoomLevels[i] < z-0.001 {
				return zoomLevels[i]
			}
		}
		return zoomLevels[0]
	}
	return min(max(z, zoomLevels[0]), zoomLevels[len(zoomLevels)-1])
}

func (a *App) zoomFile() string { return filepath.Join(a.cfg.DataDir, "run", "window-zoom") }

// restoreZoom takes the level the last launch ended with. Call before
// wails.Run, so the macOS window opens at it.
func (a *App) restoreZoom() {
	z := 1.0
	if b, err := os.ReadFile(a.zoomFile()); err == nil {
		if v, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64); err == nil {
			z = nextZoom(v, "")
		}
	}
	a.mu.Lock()
	a.zoom = z
	a.mu.Unlock()
	initPageZoom(z)
}

// setZoom changes the window's zoom by op (nextZoom) and answers the level.
// With op "" it answers the level and applies it again, which is how a
// Linux window takes the saved level once its page has loaded.
func (a *App) setZoom(op string) float64 {
	a.mu.Lock()
	old := a.zoom
	if old <= 0 {
		old = 1
	}
	z := nextZoom(old, op)
	a.zoom = z
	a.mu.Unlock()
	if !pageZoomWorks {
		return 1
	}
	if op == "" {
		if z != 1 {
			a.setPageZoom(z)
		}
		return z
	}
	if z != old {
		_ = os.MkdirAll(filepath.Dir(a.zoomFile()), 0o755)
		_ = os.WriteFile(a.zoomFile(), []byte(strconv.FormatFloat(z, 'f', -1, 64)+"\n"), 0o644)
		a.setPageZoom(z)
	}
	return z
}
