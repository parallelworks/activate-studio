//go:build darwin || linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNextZoom(t *testing.T) {
	cases := []struct {
		z    float64
		op   string
		want float64
	}{
		{1, "in", 1.1},
		{1.1, "in", 1.25},
		{1, "out", 0.9},
		{0.5, "out", 0.5},
		{3, "in", 3},
		{1.75, "reset", 1},
		// A level between steps (saved by hand, or an older step size) moves to the next step each way.
		{1.2, "in", 1.25},
		{1.2, "out", 1.1},
		{1.2, "", 1.2},
		{9, "", 3},
		{0, "", 1},
	}
	for _, c := range cases {
		if got := nextZoom(c.z, c.op); got != c.want {
			t.Errorf("nextZoom(%v, %q) = %v, want %v", c.z, c.op, got, c.want)
		}
	}
}

// The level is kept for the next launch, and Zoom("") reports it.
func TestZoomKept(t *testing.T) {
	dir := t.TempDir()
	a := &App{cfg: Config{DataDir: dir}}
	br := &Bridge{app: a}
	a.restoreZoom()
	if z := br.Zoom(""); z != 1 {
		t.Fatalf("fresh zoom %v, want 1", z)
	}
	br.Zoom("in")
	if z := br.Zoom("in"); z != 1.25 {
		t.Fatalf("two steps in: %v, want 1.25", z)
	}
	b, err := os.ReadFile(filepath.Join(dir, "run", "window-zoom"))
	if err != nil || strings.TrimSpace(string(b)) != "1.25" {
		t.Fatalf("window-zoom holds %q (%v), want 1.25", b, err)
	}
	next := &App{cfg: Config{DataDir: dir}}
	nbr := &Bridge{app: next}
	next.restoreZoom()
	if z := nbr.Zoom(""); z != 1.25 {
		t.Fatalf("next launch zoom %v, want 1.25", z)
	}
	if z := nbr.Zoom("reset"); z != 1 {
		t.Fatalf("reset: %v, want 1", z)
	}
	// The shim learns whether to take the keys.
	if !strings.Contains(next.shimScript(), `"zoomKeys":`) {
		t.Fatal("the shim's config does not say zoomKeys")
	}
}
