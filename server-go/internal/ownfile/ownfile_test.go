package ownfile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsmd"
	"github.com/parallelworks/activate-studio/server-go/internal/ownfile"
)

const body = "## Finding files\n\nUse the index first. <b>It</b> has the manuals."

func studioFile(t *testing.T, before string) string {
	t.Helper()
	s, err := agentsmd.Apply(before, agentsmd.StudioSection, body)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// What the Studio writes alone in the file is its own, and a word of a
// person's anywhere in it makes the file theirs.
func TestOnlyTheStudiosTextIsItsOwn(t *testing.T) {
	own := studioFile(t, "")
	reviewer, err := agentsmd.Apply(own, "reviewer", "Review with care.")
	if err != nil {
		t.Fatal(err)
	}
	again, err := agentsmd.Apply(strings.Replace(own, "index first", "index last", 1), agentsmd.StudioSection, body)
	if err != nil {
		t.Fatal(err)
	}
	was := own[:strings.Index(own, " [")] + own[strings.Index(own, "] -->")+1:]
	end := "\n\n<!-- ACTIVATE Studio instructions studio_tools: end"
	for name, c := range map[string]struct {
		text string
		want bool
	}{
		"as written":                     {own, true},
		"with Windows line ends":         {strings.ReplaceAll(own, "\n", "\r\n"), true},
		"blank lines after":              {own + "\n\n", true},
		"written over an edited one":     {again, true},
		"empty":                          {"", false},
		"a person's":                     {"# My rules\n", false},
		"a person's line before":         {studioFile(t, "Use tabs.\n"), false},
		"a person's line after":          {own + "\nUse tabs.\n", false},
		"a person's word in the section": {strings.Replace(own, "has the manuals", "has my manuals", 1), false},
		"a person's line at its end":     {strings.Replace(own, end, "\nMine."+end, 1), false},
		"instructions applied beside it": {reviewer, false},
		"two sections":                   {own + own, false},
		"cut short":                      {own[:len(own)/2], false},
		"before the marker had a digest": {was, false},
		"another section of this shape":  {strings.ReplaceAll(own, "studio_tools", "reviewer"), false},
	} {
		if got := ownfile.Is([]byte(c.text)); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

func TestAtIsAPlainFileOfThatName(t *testing.T) {
	d := t.TempDir()
	own := []byte(studioFile(t, ""))
	put := func(name string) string {
		p := filepath.Join(d, name)
		if err := os.WriteFile(p, own, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := put("AGENTS.md")
	if !ownfile.At(p) {
		t.Error("the Studio's file is not its own")
	}
	if ownfile.At(put("NOTES.md")) {
		t.Error("a file of another name")
	}
	if ownfile.At(filepath.Join(d, "none", "AGENTS.md")) {
		t.Error("a file that is not there")
	}
	sub := filepath.Join(d, "sub")
	os.Mkdir(sub, 0o755)
	if os.Symlink(p, filepath.Join(sub, "AGENTS.md")) == nil && ownfile.At(filepath.Join(sub, "AGENTS.md")) {
		t.Error("a link")
	}
}
