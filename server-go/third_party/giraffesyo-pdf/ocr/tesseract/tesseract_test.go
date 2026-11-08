package tesseract

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/giraffesyo/pdf"
)

// fixtureImage extracts the single image of one of the scanned-page
// fixtures, placed on its page.
func fixtureImage(t *testing.T, name string) (pdf.Image, []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(filepath.Join("..", "..", "testdata", "images", name)))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := pdf.ExtractWithOptions(context.Background(), bytes.NewReader(data), int64(len(data)), pdf.Options{IncludeImages: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Pages) != 1 || len(doc.Pages[0].Images) != 1 {
		t.Fatalf("fixture %s: pages %d", name, len(doc.Pages))
	}
	return doc.Pages[0].Images[0], data
}

func TestGlyphsFromTSV(t *testing.T) {
	im, _ := fixtureImage(t, "scan-jpeg.pdf")
	// The fixture is 620×170 pixels over 297.6×81.6 points: 0.48 pt/px.
	tsv := "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n" +
		"1\t1\t0\t0\t0\t0\t0\t0\t620\t170\t-1\t\n" +
		"5\t1\t1\t1\t1\t1\t24\t30\t80\t40\t96.5\tThe\n" +
		"5\t1\t1\t1\t1\t2\t120\t30\t100\t40\t40\tquick\n" +
		"5\t1\t1\t1\t1\t3\t240\t30\t0\t40\t90\tbad\n" + // zero width: dropped
		"5\t1\t1\t1\t1\t4\t300\t30\t50\t40\t90\t \n" // blank: dropped
	e := &Engine{MinConfidence: 50}
	glyphs, err := e.glyphs(im, []byte(tsv))
	if err != nil {
		t.Fatal(err)
	}
	if len(glyphs) != 1 || glyphs[0].Text != "The" {
		t.Fatalf("glyphs = %+v", glyphs)
	}
	g := glyphs[0]
	const scale = 72.0 / 150.0
	// Baseline at the box bottom (top 30 + height 40 = 70 px from the
	// top): page y = (170-70)·scale; x = 24·scale.
	if math.Abs(g.X-24*scale) > 1e-6 || math.Abs(g.Y-(170-70)*scale) > 1e-6 {
		t.Errorf("origin = (%v, %v)", g.X, g.Y)
	}
	if math.Abs(g.Advance-80*scale) > 1e-6 || math.Abs(g.Size-40*scale) > 1e-6 {
		t.Errorf("advance %v size %v", g.Advance, g.Size)
	}
	if math.Abs(g.Direction.X-1) > 1e-9 || math.Abs(g.Direction.Y) > 1e-9 {
		t.Errorf("direction = %+v", g.Direction)
	}
	if math.Abs(g.Ascent.Y-40*scale) > 1e-6 || math.Abs(g.Ascent.X) > 1e-9 {
		t.Errorf("ascent = %+v", g.Ascent)
	}

	// Every word kept: three, in order.
	all, err := (&Engine{}).glyphs(im, []byte(tsv))
	if err != nil || len(all) != 2 || all[1].Text != "quick" {
		t.Fatalf("all glyphs = %+v err %v", all, err)
	}
}

func TestSkipsSmallImagesAndEstimatesDPI(t *testing.T) {
	im, _ := fixtureImage(t, "scan-jpeg.pdf")
	e := &Engine{}
	if e.skip(im) {
		t.Error("620×170 image skipped")
	}
	if dpi := e.dpi(im); dpi != 150 {
		t.Errorf("dpi = %d, want 150", dpi)
	}
	small := pdf.Image{Width: 10, Height: 300}
	if !e.skip(small) {
		t.Error("10-pixel-wide image not skipped")
	}
	if (&Engine{MinImageSize: -1}).skip(small) {
		t.Error("MinImageSize -1 should disable the check")
	}
	if dpi := e.dpi(small); dpi != 0 {
		t.Errorf("unplaced image dpi = %d, want 0", dpi)
	}
}

func TestMissingExecutable(t *testing.T) {
	im, _ := fixtureImage(t, "scan-jpeg.pdf")
	e := &Engine{Command: filepath.Join(t.TempDir(), "no-such-tesseract")}
	_, err := e.run(context.Background(), im)
	if !errors.Is(err, exec.ErrNotFound) && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
}

func TestOCRFixtures(t *testing.T) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("tesseract not installed")
	}
	for _, name := range []string{"scan-jpeg.pdf", "scan-g4.pdf", "scan-jbig2.pdf"} {
		t.Run(name, func(t *testing.T) {
			_, data := fixtureImage(t, name)
			engine := &Engine{Languages: []string{"eng"}}
			doc, err := pdf.ExtractWithOptions(context.Background(), bytes.NewReader(data), int64(len(data)), pdf.Options{OCR: engine})
			if err != nil {
				t.Fatal(err)
			}
			if len(doc.Warnings) != 0 {
				t.Fatalf("warnings = %v", doc.Warnings)
			}
			page := doc.Pages[0]
			text := page.Text()
			if !strings.Contains(text, "quick brown fox") || !strings.Contains(text, "lazy dogs") {
				t.Errorf("text = %q", text)
			}
			if page.OCRGlyphs != len(page.Glyphs) || page.OCRGlyphs < 8 {
				t.Errorf("OCRGlyphs = %d of %d", page.OCRGlyphs, len(page.Glyphs))
			}
			// Two lines, the second below the first.
			lines := strings.Split(text, "\n")
			if len(lines) != 2 {
				t.Errorf("lines = %q", lines)
			}
			first, last := page.Glyphs[0], page.Glyphs[len(page.Glyphs)-1]
			if !(first.Y > last.Y) {
				t.Errorf("first word at y=%v should sit above last word at y=%v", first.Y, last.Y)
			}
			if b := page.Images; b != nil {
				t.Errorf("images retained: %d", len(b))
			}
		})
	}
}
