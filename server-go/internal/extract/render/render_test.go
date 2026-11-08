package render

import (
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makePDF writes a one-page PDF of the given size with content, extra
// page entries (such as "/Rotate 90") and resources, and further objects
// numbered from 5.
func makePDF(t testing.TB, w, h float64, pageExtra, resources, content string, objs ...string) string {
	t.Helper()
	all := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %g %g] /Contents 4 0 R /Resources << %s >> %s >>", w, h, resources, pageExtra),
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content)+1, content),
	}
	return writePDF(t, append(all, objs...))
}

func writePDF(t testing.TB, all []string) string {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offs := make([]int, len(all))
	for i, o := range all {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(all)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(all)+1, x)
	p := filepath.Join(t.TempDir(), "t.pdf")
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func renderOne(t *testing.T, path string, dpi float64) *image.Gray {
	t.Helper()
	d, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.PageCount() != 1 {
		t.Fatalf("pages %d", d.PageCount())
	}
	img, err := d.Page(context.Background(), 1, dpi)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// at is the grey at page point (x, y), y up, for an unrotated page h
// points high drawn at 72 dpi.
func at(img *image.Gray, h, x, y float64) uint8 {
	return img.GrayAt(int(x), int(h-y)).Y
}

func expect(t *testing.T, img *image.Gray, h float64, dark, light [][2]float64) {
	t.Helper()
	for _, p := range dark {
		if g := at(img, h, p[0], p[1]); g > 40 {
			t.Errorf("(%g, %g) = %d, want dark", p[0], p[1], g)
		}
	}
	for _, p := range light {
		if g := at(img, h, p[0], p[1]); g < 215 {
			t.Errorf("(%g, %g) = %d, want light", p[0], p[1], g)
		}
	}
}

func TestFill(t *testing.T) {
	img := renderOne(t, makePDF(t, 100, 80, "", "", "0 g 10 10 30 20 re f"), 72)
	if b := img.Bounds(); b.Dx() != 100 || b.Dy() != 80 {
		t.Fatalf("size %v", b)
	}
	expect(t, img, 80, [][2]float64{{20, 20}, {38, 28}}, [][2]float64{{5, 5}, {50, 20}, {20, 35}})
}

func TestGreyAndAlpha(t *testing.T) {
	img := renderOne(t, makePDF(t, 100, 80, "", "/ExtGState << /H << /ca 0.5 >> >>",
		"0.5 g 0 0 50 80 re f 0 g /H gs 50 0 50 80 re f"), 72)
	for _, x := range []float64{25, 75} {
		if g := at(img, 80, x, 40); g < 118 || g > 138 {
			t.Errorf("x %g: grey %d, want about 128", x, g)
		}
	}
}

func TestEvenOddHole(t *testing.T) {
	sq := "10 10 m 70 10 l 70 70 l 10 70 l h 30 30 m 50 30 l 50 50 l 30 50 l h"
	img := renderOne(t, makePDF(t, 80, 80, "", "", sq+" f*"), 72)
	expect(t, img, 80, [][2]float64{{20, 20}}, [][2]float64{{40, 40}})
	// Nonzero fills the hole: both squares turn the same way.
	img = renderOne(t, makePDF(t, 80, 80, "", "", sq+" f"), 72)
	expect(t, img, 80, [][2]float64{{20, 20}, {40, 40}}, nil)
}

func TestCurve(t *testing.T) {
	// A circle of radius 30 about (50, 50) from four curves.
	k := 30 * 0.5523
	c := fmt.Sprintf("80 50 m 80 %[1]g %[1]g 80 50 80 c %[2]g 80 20 %[1]g 20 50 c 20 %[2]g %[2]g 20 50 20 c %[1]g 20 80 %[2]g 80 50 c f",
		50+k, 50-k)
	img := renderOne(t, makePDF(t, 100, 100, "", "", c), 72)
	expect(t, img, 100, [][2]float64{{50, 50}, {75, 50}, {50, 25}}, [][2]float64{{25, 25}, {75, 75}, {85, 50}})
}

func TestStroke(t *testing.T) {
	img := renderOne(t, makePDF(t, 100, 100, "", "", "4 w 10 50 m 90 50 l S 1 w 50 10 m 50 30 l S"), 72)
	expect(t, img, 100, [][2]float64{{50, 50}, {50, 51.5}, {20, 49}}, [][2]float64{{50, 54}, {50, 46}, {95, 50}, {30, 20}})
	// A one-point line shows as at least a grey pixel column.
	if g := at(img, 100, 50, 20); g > 128 {
		t.Errorf("thin line grey %d", g)
	}
	// The width scales with the transform.
	img = renderOne(t, makePDF(t, 100, 100, "", "", "4 0 0 4 0 0 cm 2 w 2.5 12.5 m 22.5 12.5 l S"), 72)
	expect(t, img, 100, [][2]float64{{50, 50}, {50, 53}, {50, 47}}, [][2]float64{{50, 56}, {50, 44}})
}

func TestClip(t *testing.T) {
	img := renderOne(t, makePDF(t, 100, 100, "", "", "q 20 20 30 30 re W n 0 0 100 100 re f Q 60 60 20 20 re f"), 72)
	expect(t, img, 100, [][2]float64{{30, 30}, {70, 70}}, [][2]float64{{10, 10}, {60, 30}, {30, 60}})
	// Clips nest; Q restores the outer one.
	img = renderOne(t, makePDF(t, 100, 100, "", "",
		"q 0 0 50 100 re W n q 0 0 100 50 re W n 0 0 100 100 re f Q 0 60 100 40 re f Q"), 72)
	expect(t, img, 100, [][2]float64{{25, 25}, {25, 75}}, [][2]float64{{75, 25}, {75, 75}})
}

func TestRotation(t *testing.T) {
	for _, tc := range []struct {
		rot  int
		w, h int
		x, y int // where the mark at the page's lower left lands
	}{
		{0, 200, 100, 15, 85},
		{90, 100, 200, 15, 15},
		{180, 200, 100, 185, 15},
		{270, 100, 200, 85, 185},
	} {
		img := renderOne(t, makePDF(t, 200, 100, fmt.Sprintf("/Rotate %d", tc.rot), "", "10 10 10 10 re f"), 72)
		if b := img.Bounds(); b.Dx() != tc.w || b.Dy() != tc.h {
			t.Errorf("rotate %d: size %v", tc.rot, b)
			continue
		}
		if g := img.GrayAt(tc.x, tc.y).Y; g > 40 {
			t.Errorf("rotate %d: (%d, %d) = %d, want the mark", tc.rot, tc.x, tc.y, g)
		}
	}
}

func TestCropBox(t *testing.T) {
	img := renderOne(t, makePDF(t, 200, 200, "/CropBox [50 50 150 150]", "", "60 60 10 10 re f"), 72)
	if b := img.Bounds(); b.Dx() != 100 || b.Dy() != 100 {
		t.Fatalf("size %v", b)
	}
	if g := img.GrayAt(15, 85).Y; g > 40 {
		t.Errorf("mark grey %d", g)
	}
}

func TestResolution(t *testing.T) {
	img := renderOne(t, makePDF(t, 72, 36, "", "", "0 0 36 36 re f"), 300)
	if b := img.Bounds(); b.Dx() != 300 || b.Dy() != 150 {
		t.Fatalf("size %v", b)
	}
	if img.GrayAt(100, 75).Y > 40 || img.GrayAt(200, 75).Y < 215 {
		t.Error("half page not dark")
	}
}

func TestImage(t *testing.T) {
	// A 2 by 2 image, black and white by quadrant, over the page.
	im := "<< /Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceGray /BitsPerComponent 8 /Length 4 >>\nstream\n\x00\xff\xff\x00\nendstream"
	img := renderOne(t, makePDF(t, 100, 100, "", "/XObject << /Im 5 0 R >>", "q 100 0 0 100 0 0 cm /Im Do Q", im), 72)
	// The first row of samples is the top of the image.
	expect(t, img, 100, [][2]float64{{20, 80}, {80, 20}}, [][2]float64{{80, 80}, {20, 20}})
}

func TestStencilMask(t *testing.T) {
	// One-bit mask, 8 by 1: 0 paints, so the left half is filled.
	im := "<< /Type /XObject /Subtype /Image /Width 8 /Height 1 /ImageMask true /BitsPerComponent 1 /Length 1 >>\nstream\n\x0f\nendstream"
	img := renderOne(t, makePDF(t, 80, 10, "", "/XObject << /M 5 0 R >>", "0 g q 80 0 0 10 0 0 cm /M Do Q", im), 72)
	expect(t, img, 10, [][2]float64{{20, 5}}, [][2]float64{{60, 5}})
}

func TestForm(t *testing.T) {
	form := "<< /Type /XObject /Subtype /Form /BBox [0 0 10 10] /Matrix [2 0 0 2 10 10] /Length 21 >>\nstream\n-5 -5 100 100 re f\n\nendstream"
	img := renderOne(t, makePDF(t, 100, 100, "", "/XObject << /F 5 0 R >>", "/F Do", form), 72)
	// The form fills its box: page 10 to 30 after its matrix.
	expect(t, img, 100, [][2]float64{{20, 20}}, [][2]float64{{5, 5}, {40, 40}})
}

func TestFormRecursion(t *testing.T) {
	// A form that draws itself stops at the depth limit.
	form := "<< /Type /XObject /Subtype /Form /BBox [0 0 100 100] /Resources << /XObject << /F 5 0 R >> >> /Length 25 >>\nstream\n0 0 10 10 re f /F Do\n\nendstream"
	img := renderOne(t, makePDF(t, 100, 100, "", "/XObject << /F 5 0 R >>", "/F Do", form), 72)
	expect(t, img, 100, [][2]float64{{5, 5}}, [][2]float64{{50, 50}})
}

func TestColorSpaces(t *testing.T) {
	c := "1 0 0 rg 0 0 10 10 re f 0 0 0 1 k 10 0 10 10 re f /CS0 cs 1 sc 20 0 10 10 re f /CS1 cs 1 sc 30 0 10 10 re f /P cs /Pat scn 40 0 10 10 re f"
	res := "/ColorSpace << /CS0 [/Separation /Spot /DeviceCMYK 5 0 R] /CS1 [/Indexed /DeviceRGB 1 <000000FFFFFF>] /P /Pattern >>"
	img := renderOne(t, makePDF(t, 50, 10, "", res, c, "<< /FunctionType 2 /Domain [0 1] /N 1 >>"), 72)
	want := []struct {
		x        float64
		min, max uint8
	}{{5, 66, 86}, {15, 0, 10}, {25, 0, 10}, {35, 245, 255}, {45, 245, 255}}
	for _, w := range want {
		if g := at(img, 10, w.x, 5); g < w.min || g > w.max {
			t.Errorf("x %g: grey %d, want %d to %d", w.x, g, w.min, w.max)
		}
	}
}

func TestCompressedContent(t *testing.T) {
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	w.Write([]byte("0 0 50 50 re f 60 60 m 90 60 l 90 90 l h f"))
	w.Close()
	cut := z.Bytes()[:z.Len()-12] // damaged: the second shape is lost
	for _, tc := range []struct {
		data []byte
		dark [][2]float64
	}{{z.Bytes(), [][2]float64{{25, 25}, {85, 70}}}, {cut, [][2]float64{{25, 25}}}} {
		p := writePDF(t, []string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R >>",
			fmt.Sprintf("<< /Length %d /Filter /FlateDecode >>\nstream\n%s\nendstream", len(tc.data), tc.data),
		})
		img := renderOne(t, p, 72)
		expect(t, img, 100, tc.dark, [][2]float64{{75, 25}})
	}
}

func TestDeadline(t *testing.T) {
	var c strings.Builder
	for i := 0; i < 200000; i++ {
		c.WriteString("0 0 100 100 re f\n")
	}
	d, err := Open(context.Background(), makePDF(t, 100, 100, "", "", c.String()))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	img, err := d.Page(ctx, 1, 300)
	if err == nil {
		t.Fatal("no error past the deadline")
	}
	if img == nil {
		t.Error("no partial page")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("took %v", el)
	}
}

func TestBadInput(t *testing.T) {
	dir := t.TempDir()
	for i, b := range []string{"", "%PDF-1.4\n", "not a pdf", "%PDF-1.4\n1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj trailer << /Root 1 0 R >>"} {
		p := filepath.Join(dir, fmt.Sprint(i))
		os.WriteFile(p, []byte(b), 0o644)
		if d, err := Open(context.Background(), p); err == nil {
			for n := 1; n <= d.PageCount(); n++ {
				d.Page(context.Background(), n, 72)
			}
			if _, err := d.Page(context.Background(), d.PageCount()+1, 72); err == nil {
				t.Error("page past the end")
			}
			d.Close()
		}
	}
	// Content that is not well formed draws what it can.
	img := renderOne(t, makePDF(t, 100, 100, "", "", "q q q [1 2 (x) <<>> /N] 0 0 50 50 re ) ] >> f Q Q Q Q Q 1 2 3 cm BI /W 1 ID xx EI 50 50 m l c h S"), 72)
	expect(t, img, 100, [][2]float64{{25, 25}}, [][2]float64{{75, 75}})
}

func TestNoPanicOnGarbage(t *testing.T) {
	p := makePDF(t, 100, 100, "", "/XObject << /Im 5 0 R >>", "/Im Do 1e308 1e308 1e308 1e308 1e308 1e308 cm 0 0 1 1 re f",
		"<< /Subtype /Image /Width 100000 /Height 100000 /BitsPerComponent 8 /Length 1 >>\nstream\nx\nendstream")
	renderOne(t, p, 72)
}

func FuzzContent(f *testing.F) {
	for _, s := range []string{
		"0 g 10 10 30 20 re f", "q 20 20 30 30 re W n 0 0 100 100 re f Q", "4 w 10 50 m 90 50 l S",
		"80 50 m 80 66 66 80 50 80 c f*", "BI /W 2 /H 2 /BPC 8 /CS /G ID abcd EI", "/F Do",
	} {
		f.Add(s)
	}
	form := "<< /Type /XObject /Subtype /Form /BBox [0 0 100 100] /Resources << /XObject << /F 5 0 R >> >> /Length 25 >>\nstream\n0 0 10 10 re f /F Do\n\nendstream"
	f.Fuzz(func(t *testing.T, content string) {
		if len(content) > 4096 {
			return
		}
		p := makePDF(t, 50, 50, "", "/XObject << /F 5 0 R >>", content, form)
		d, err := Open(context.Background(), p)
		if err != nil {
			return
		}
		defer d.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		d.Page(ctx, 1, 72)
	})
}

// TestCyclicPageTree: a page tree that lists itself as a kid is walked
// once, where pdfcpu's own walk overflows the stack.
func TestCyclicPageTree(t *testing.T) {
	p := writePDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [2 0 R 5 0 R 3 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R >>",
		"<< /Length 14 >>\nstream\n0 0 50 50 re f\nendstream",
		"<< /Type /Pages /Kids [5 0 R 2 0 R] /Count 1 >>",
	})
	d, err := Open(context.Background(), p)
	if err != nil {
		t.Skip("not opened:", err)
	}
	defer d.Close()
	for n := 1; n <= d.PageCount(); n++ {
		d.Page(context.Background(), n, 72)
	}
}
