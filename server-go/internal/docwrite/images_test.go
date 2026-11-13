package docwrite

import (
	"archive/zip"
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
	"testing"
)

func testPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 200, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func testJPEG(w, h int) []byte {
	var b bytes.Buffer
	jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, w, h)), nil)
	return b.Bytes()
}

const imageDoc = "# Report\n\nBefore ![chart](figs/chart.png) after.\n\n![photo](photo.jpg)\n\n![gone](nope.png)\n\n| a | b |\n| - | - |\n| ![cell](figs/chart.png) | 2 |\n\n- ![in a list](figs/chart.png)\n"

func testSource(ref string) ([]byte, error) {
	switch ref {
	case "figs/chart.png":
		return testPNG(1600, 400), nil
	case "photo.jpg":
		return testJPEG(300, 200), nil
	}
	return nil, errors.New("not found in the library")
}

func TestParseImages(t *testing.T) {
	bs := Parse(imageDoc)
	var kinds []string
	for _, b := range bs {
		kinds = append(kinds, b.Kind)
	}
	if got := strings.Join(kinds, " "); got != "h p img p img img table list" {
		t.Fatalf("kinds = %s", got)
	}
	if bs[2].Src != "figs/chart.png" || bs[2].Text != "chart" || Plain(bs[1].Runs) != "Before " || Plain(bs[3].Runs) != " after." {
		t.Errorf("split: %+v", bs[1:4])
	}
	if cell := Plain(bs[6].Rows[0][0]); cell != "[cell]" {
		t.Errorf("an image in a cell is its alt text: %q", cell)
	}
	if got := strings.Join(Images(bs), ","); got != "figs/chart.png,photo.jpg,nope.png" {
		t.Errorf("images = %s", got)
	}
}

func TestRenderImages(t *testing.T) {
	r, err := Render(".pdf", imageDoc, testSource)
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(r.Data, []byte("/Subtype /Image")); n != 2 {
		t.Errorf("PDF has %d image objects, want 2 (each image once)", n)
	}
	if !strings.Contains(r.Note, "with 2 images") || len(r.Missing) != 1 || !strings.HasPrefix(r.Missing[0], "nope.png (not found") {
		t.Errorf("note %q missing %v", r.Note, r.Missing)
	}

	r, err = Render(".docx", imageDoc, testSource)
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(r.Data), int64(len(r.Data)))
	if err != nil {
		t.Fatal(err)
	}
	parts := map[string]string{}
	for _, f := range z.File {
		if f.Method != zip.Deflate {
			t.Errorf("%s is not deflated; LibreOffice refuses such a document", f.Name)
		}
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		parts[f.Name] = string(b)
	}
	if _, ok := parts["word/media/image1.png"]; !ok {
		t.Errorf("no image1.png in %v", keys(parts))
	}
	if _, ok := parts["word/media/image2.jpeg"]; !ok {
		t.Errorf("no image2.jpeg in %v", keys(parts))
	}
	doc := parts["word/document.xml"]
	if strings.Count(doc, "<w:drawing>") != 3 || !strings.Contains(parts["word/_rels/document.xml.rels"], `Target="media/image2.jpeg"`) {
		t.Errorf("drawings %d, rels %s", strings.Count(doc, "<w:drawing>"), parts["word/_rels/document.xml.rels"])
	}
	// The wide chart is scaled to the 6.5 inch text width.
	if !strings.Contains(doc, `<wp:extent cx="5943600" cy="1485900"/>`) {
		t.Errorf("chart extent not scaled to the text width")
	}
	if !strings.Contains(doc, "[gone]") {
		t.Errorf("a missing image keeps its alt text")
	}

	// No source: nothing is embedded and each image is reported.
	r, _ = Render(".pdf", imageDoc, nil)
	if bytes.Contains(r.Data, []byte("/Subtype /Image")) || len(r.Missing) != 3 {
		t.Errorf("nil source: missing %v", r.Missing)
	}
}

func TestLoadPicture(t *testing.T) {
	if _, err := loadPicture([]byte("not an image")); err == nil {
		t.Error("text is not an image")
	}
	p, err := loadPicture(testJPEG(10, 10))
	if err != nil || p.kind != "JPG" {
		t.Errorf("jpeg %v %v", p, err)
	}
	w, h := (&picture{w: 960, h: 960}).size(468, 648)
	if w != 468 || h != 468 {
		t.Errorf("size %v x %v", w, h)
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
