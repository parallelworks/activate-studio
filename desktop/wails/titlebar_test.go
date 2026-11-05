package main

import (
	"strings"
	"testing"
)

func TestBarInkReadsOnTheBar(t *testing.T) {
	for color, want := range map[string]string{
		"#f3f4f6": "#1a1a1a", // the light theme's ground
		"#0f1216": "#ffffff", // the dark theme's ground
		"#007a33": "#ffffff", // a classification banner's green
		"#ffd100": "#1a1a1a", // and its yellow
	} {
		if got := barInk(color); got != want {
			t.Errorf("barInk(%s) = %s, want %s", color, got, want)
		}
	}
}

func TestSetBarColorRefusesAnythingButAColor(t *testing.T) {
	a := &App{cfg: Config{DataDir: t.TempDir()}}
	for _, bad := range []string{"", "red", "#fff", "#12345g", "#123456; } * { display: none", "rgb(1,2,3)"} {
		if err := a.setBarColor(bad); err == nil {
			t.Errorf("setBarColor(%q) was accepted", bad)
		}
	}
	if err := a.setBarColor("#F3F4F6"); err != nil {
		t.Fatal(err)
	}
	b := &App{cfg: a.cfg}
	b.restoreBarColor()
	if b.barColor != "#f3f4f6" {
		t.Errorf("restored %q", b.barColor)
	}
	if !strings.Contains(barCSS("#f3f4f6"), "background: #f3f4f6") {
		t.Error("the style sheet does not carry the color")
	}
}
