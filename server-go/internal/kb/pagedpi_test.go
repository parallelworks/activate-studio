package kb

import (
	"context"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/docwrite"
)

func TestPageDPIIsTheNextFinerOne(t *testing.T) {
	for want, got := range map[float64]int{1: 130, 130: 130, 131: 200, 250: 300, 400: 400, 2000: 400} {
		if d := PageDPI(want); d != got {
			t.Errorf("PageDPI(%v) = %d, want %d", want, d, got)
		}
	}
}

// A page zoomed in is drawn finer, and each resolution keeps its own image.
func TestAPageIsDrawnAtTheResolutionAskedFor(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("no poppler here")
	}
	base := t.TempDir()
	p := &Previews{Roots: Roots{KBRoot: base, IndexBase: filepath.Join(base, "state"), IndexDir: filepath.Join(base, "state")}}
	pdf := filepath.Join(base, "a.pdf")
	r, err := docwrite.Render(".pdf", "# Page\n\nText.", nil)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(pdf, r.Data, 0o644)
	width := func(dpi int) (int, string) {
		path, err := p.PagePNG(context.Background(), pdf, "a.pdf", 1, dpi)
		if err != nil {
			t.Fatal(err)
		}
		f, _ := os.Open(path)
		defer f.Close()
		c, err := png.DecodeConfig(f)
		if err != nil {
			t.Fatal(err)
		}
		return c.Width, path
	}
	w130, p130 := width(130)
	w300, p300 := width(300)
	w999, p999 := width(999)
	if p130 == p300 || p300 == p999 || filepath.Dir(p300) == filepath.Dir(p130) {
		t.Errorf("one image for two resolutions: %s %s %s", p130, p300, p999)
	}
	// A letter page is 8.5 inches wide.
	if w130 != 1105 || w300 != 2550 || w999 != 3400 {
		t.Errorf("widths %d %d %d", w130, w300, w999)
	}
	if again, _ := width(130); again != w130 {
		t.Errorf("130 dpi again: %d", again)
	}
}
