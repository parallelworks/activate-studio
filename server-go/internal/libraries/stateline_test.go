package libraries_test

import (
	"path/filepath"
	"regexp"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// From indexFormat.test.mjs and omissions.test.mjs: the start-up line
// names an index from before the rename, and says nothing of the others.
func TestStateLine(t *testing.T) {
	dir := t.TempDir()
	reg := libraries.New(libraries.Options{KBRoot: filepath.Join(dir, "kb"), IndexBase: filepath.Join(dir, "ix"),
		GufiIndex: filepath.Join(dir, "ix", "gufi", "kb"), GufiBin: filepath.Join(dir, "no-gufi"),
		Getenv: func(string) string { return "" }})
	l := libraries.Library{ID: "x", Label: "Shelf", IndexRoot: "/nowhere/x", Caps: libraries.Caps{Index: true}}
	with := func(format string, rolled bool) libraries.Library {
		c := l
		c.Format = &libraries.FormatRecord{Format: format}
		if rolled {
			c.Format.RolledUp = &rolled
		}
		return c
	}
	written := regexp.MustCompile(`written by`)
	for _, f := range []string{"current", "earlier"} {
		if s := reg.StateLine(with(f, false)); written.MatchString(s) {
			t.Fatalf("%s: %s", f, s)
		}
	}
	if s := reg.StateLine(with("older", false)); s != "library x (Shelf): index yes, words no, vectors no, written by GUFI 0.7.0 or earlier" {
		t.Fatalf("older: %s", s)
	}
	if s := reg.StateLine(with("older", true)); !regexp.MustCompile(`written by GUFI 0\.7\.0 or earlier, rolled up; searched in part: `).MatchString(s) {
		t.Fatalf("older, rolled up: %s", s)
	}
	at := "2026-11-08T10:00:00.000Z"
	b := libraries.Library{ID: "shelf", Label: "Shelf", IndexRoot: "/nowhere/shelf", Depth: "text",
		Caps: libraries.Caps{Index: true, FullText: true}, Build: &libraries.LocalBuild{State: "ready", BuiltAt: &at}}
	if s := reg.StateLine(b); !regexp.MustCompile(`^library shelf \(Shelf\): index yes, words yes, vectors no, depth text, build ready \(built .+\)$`).MatchString(s) {
		t.Fatalf("built: %s", s)
	}
	none := libraries.Library{ID: "y", Label: "Empty", IndexRoot: "/nowhere/y", Site: "edge"}
	if s := reg.StateLine(none); !regexp.MustCompile(`^library y \(Empty\): index no, words no, vectors no, on edge; left out of search: .+`).MatchString(s) {
		t.Fatalf("no index: %s", s)
	}
}
