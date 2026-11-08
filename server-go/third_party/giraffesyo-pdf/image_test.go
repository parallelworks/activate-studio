package pdf

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/giraffesyo/pdf/pdftest"
)

// imageDoc builds a one-page document whose resources hold the given
// image XObject (object 5) and whose content is content. extra objects
// follow as 6, 7, …
func imageDoc(content, imageObj string, extra ...string) []byte {
	objs := []string{
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> >> /XObject << /Im1 5 0 R >> >>"),
		pdftest.Stream("", content),
		imageObj,
	}
	return pdftest.Build(1, append(objs, extra...)...)
}

func grayImageObj(w, h int, data string, extra string) string {
	return pdftest.Stream(fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8 %s", w, h, extra), data)
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func pointNear(a, b Point) bool { return near(a.X, b.X) && near(a.Y, b.Y) }

func TestImageXObjectPlacement(t *testing.T) {
	// A 4×2 grey image painted 200 wide and 100 tall at (50, 600), then
	// again rotated 90° counter-clockwise about (300, 300).
	data := imageDoc(
		"q 200 0 0 100 50 600 cm /Im1 Do Q q 0 100 -50 0 300 300 cm /Im1 Do Q",
		grayImageObj(4, 2, "\x00\x40\x80\xff\x10\x20\x30\x40", ""),
	)
	doc, err := extractOptions(t, data, Options{IncludeImages: true})
	if err != nil {
		t.Fatal(err)
	}
	page := doc.Pages[0]
	if len(page.Images) != 2 {
		t.Fatalf("images = %d, want 2: %+v", len(page.Images), page.Images)
	}
	im := page.Images[0]
	if im.Width != 4 || im.Height != 2 || im.BitsPerComponent != 8 || im.Components != 1 ||
		im.ColorSpace != "DeviceGray" || im.Filter != "" || im.Inline || im.ImageMask {
		t.Errorf("image = %+v", im)
	}
	if string(im.Data) != "\x00\x40\x80\xff\x10\x20\x30\x40" {
		t.Errorf("data = %q", im.Data)
	}
	if b := im.Bounds(); !near(b.MinX, 50) || !near(b.MinY, 600) || !near(b.MaxX, 250) || !near(b.MaxY, 700) {
		t.Errorf("bounds = %+v", b)
	}
	if q := im.Quad(); !pointNear(q[0], Point{X: 50, Y: 600}) || !pointNear(q[1], Point{X: 250, Y: 600}) ||
		!pointNear(q[2], Point{X: 250, Y: 700}) || !pointNear(q[3], Point{X: 50, Y: 700}) {
		t.Errorf("quad = %+v", q)
	}
	// Pixel (0,0) is the top-left sample; (4,2) the bottom-right.
	if p := im.ToPage(0, 0); !pointNear(p, Point{X: 50, Y: 700}) {
		t.Errorf("ToPage(0,0) = %+v", p)
	}
	if p := im.ToPage(4, 2); !pointNear(p, Point{X: 250, Y: 600}) {
		t.Errorf("ToPage(4,2) = %+v", p)
	}
	if p := im.ToPage(2, 1); !pointNear(p, Point{X: 150, Y: 650}) {
		t.Errorf("ToPage(2,1) = %+v", p)
	}

	rotated := page.Images[1]
	// The unit square's x axis maps to page +y (100 long), its y axis to
	// page -x (50 long): the image stands on its right edge.
	if q := rotated.Quad(); !pointNear(q[0], Point{X: 300, Y: 300}) || !pointNear(q[1], Point{X: 300, Y: 400}) ||
		!pointNear(q[2], Point{X: 250, Y: 400}) || !pointNear(q[3], Point{X: 250, Y: 300}) {
		t.Errorf("rotated quad = %+v", q)
	}
	if &rotated.Data[0] != &im.Data[0] {
		t.Error("the same XObject painted twice should share its data")
	}

	// Without IncludeImages nothing is reported.
	doc, err = extractOptions(t, data, Options{})
	if err != nil || doc.Pages[0].Images != nil {
		t.Fatalf("images without IncludeImages = %+v, err %v", doc.Pages[0].Images, err)
	}
}

func TestImageInsideFormXObject(t *testing.T) {
	objs := []string{
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /XObject << /Fx 5 0 R >> >>"),
		pdftest.Stream("", "q 1 0 0 1 100 100 cm /Fx Do Q"),
		pdftest.Stream("/Type /XObject /Subtype /Form /BBox [0 0 100 100] /Matrix [2 0 0 2 0 0] /Resources << /XObject << /Im1 6 0 R >> >>",
			"q 10 0 0 10 5 5 cm /Im1 Do Q"),
		grayImageObj(1, 1, "\x80", ""),
	}
	data := pdftest.Build(1, objs...)
	doc, err := extractOptions(t, data, Options{IncludeImages: true})
	if err != nil {
		t.Fatal(err)
	}
	images := doc.Pages[0].Images
	if len(images) != 1 {
		t.Fatalf("images = %+v", images)
	}
	// form matrix doubles, page CTM translates: 10 units at (5,5) → 20 at (110,110).
	if b := images[0].Bounds(); !near(b.MinX, 110) || !near(b.MinY, 110) || !near(b.MaxX, 130) || !near(b.MaxY, 130) {
		t.Errorf("bounds = %+v", b)
	}
}

func TestInlineImages(t *testing.T) {
	// Unfiltered data whose bytes spell " EI " mid-stream: the exact
	// length from the dictionary must be trusted over the marker scan.
	raw := "ab EI cd"
	if len(raw) != 8 {
		t.Fatal("fixture")
	}
	content := "q 80 0 0 20 10 10 cm BI /W 4 /H 2 /BPC 8 /CS /G ID " + raw + " EI Q\n" +
		// Abbreviated filter and parms, an image mask with a Decode array.
		"q 8 0 0 8 100 100 cm BI /W 8 /H 1 /IM true /D [1 0] /F /AHx ID a5> EI Q\n" +
		// A named colour space from the resources, Indexed inline.
		"q 4 0 0 1 200 200 cm BI /W 2 /H 1 /BPC 8 /CS /CSx ID \x00\x01 EI Q\n" +
		"q 4 0 0 1 300 300 cm BI /W 2 /H 1 /BPC 1 /CS [/I /RGB 1 <ff000000ff00>] ID \x40 EI Q\n" +
		"BT /F1 12 Tf 72 700 Td (text) Tj ET"
	objs := []string{
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> >> /ColorSpace << /CSx [/ICCBased 5 0 R] >> >>"),
		pdftest.Stream("", content),
		pdftest.Stream("/N 3", "icc"),
	}
	data := pdftest.Build(1, objs...)
	doc, err := extractOptions(t, data, Options{IncludeImages: true})
	if err != nil {
		t.Fatal(err)
	}
	page := doc.Pages[0]
	if page.Text() != "text" {
		t.Errorf("text after inline images = %q (warnings %v)", page.Text(), page.Warnings)
	}
	if len(page.Images) != 4 {
		t.Fatalf("images = %d: %+v (warnings %v)", len(page.Images), page.Images, page.Warnings)
	}
	first := page.Images[0]
	if !first.Inline || first.Width != 4 || first.Height != 2 || string(first.Data) != raw || first.ColorSpace != "DeviceGray" {
		t.Errorf("first = %+v", first)
	}
	if b := first.Bounds(); !near(b.MinX, 10) || !near(b.MaxX, 90) || !near(b.MaxY, 30) {
		t.Errorf("first bounds = %+v", b)
	}
	mask := page.Images[1]
	if !mask.ImageMask || mask.BitsPerComponent != 1 || string(mask.Data) != "\xa5" || mask.Filter != "" {
		t.Errorf("mask = %+v", mask)
	}
	img, err := mask.Decode()
	if err != nil {
		t.Fatal(err)
	}
	// Decode [1 0] paints the 1 bits: 0xa5 = 1010 0101 → black at 0,2,5,7.
	for x, want := range []uint8{0, 255, 0, 255, 255, 0, 255, 0} {
		if got := img.(*image.Gray).GrayAt(x, 0).Y; got != want {
			t.Errorf("mask pixel %d = %d, want %d", x, got, want)
		}
	}
	named := page.Images[2]
	if named.ColorSpace != "ICCBased" || named.Components != 3 {
		t.Errorf("named colour space image = %+v", named)
	}
	indexed := page.Images[3]
	if indexed.ColorSpace != "Indexed" || indexed.Components != 1 {
		t.Errorf("indexed = %+v", indexed)
	}
	img, err = indexed.Decode()
	if err != nil {
		t.Fatal(err)
	}
	pal, ok := img.(*image.Paletted)
	if !ok || len(pal.Palette) != 2 {
		t.Fatalf("indexed decode = %T %+v", img, img)
	}
	// 0x40 = 01…: pixel 0 index 0 (red), pixel 1 index 1 (green).
	r, g, _, _ := pal.At(0, 0).RGBA()
	r2, g2, _, _ := pal.At(1, 0).RGBA()
	if r != 0xFFFF || g != 0 || r2 != 0 || g2 != 0xFFFF {
		t.Errorf("indexed pixels = %v %v", pal.At(0, 0), pal.At(1, 0))
	}
}

func TestInlineImageMarkerScan(t *testing.T) {
	// DCT data cannot be measured from the dictionary; the EI inside the
	// binary run is followed by non-printable bytes and must be skipped,
	// the real one is followed by content.
	binary := "\xff\xd8 EI \x01\x02\x03"
	content := "q BI /W 1 /H 1 /BPC 8 /CS /G /F /DCT ID " + binary + " EI Q BT /F1 12 Tf 10 10 Td (after) Tj ET"
	data := simpleDoc(content)
	doc, err := extractOptions(t, data, Options{IncludeImages: true})
	if err != nil {
		t.Fatal(err)
	}
	page := doc.Pages[0]
	if page.Text() != "after" || len(page.Images) != 1 || string(page.Images[0].Data) != binary || page.Images[0].Filter != "DCTDecode" {
		t.Fatalf("text %q images %+v warnings %v", page.Text(), page.Images, page.Warnings)
	}
}

func TestImageFilterChainAndJPEG(t *testing.T) {
	src := image.NewGray(image.Rect(0, 0, 6, 4))
	for i := range src.Pix {
		src.Pix[i] = uint8(i * 10)
	}
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, src, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	var flated bytes.Buffer
	zw := zlib.NewWriter(&flated)
	_, _ = zw.Write(jpg.Bytes())
	_ = zw.Close()

	data := imageDoc("q 60 0 0 40 0 0 cm /Im1 Do Q",
		pdftest.Stream("/Type /XObject /Subtype /Image /Width 6 /Height 4 /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter [/FlateDecode /DCTDecode]", flated.String()))
	doc, err := extractOptions(t, data, Options{IncludeImages: true})
	if err != nil {
		t.Fatal(err)
	}
	images := doc.Pages[0].Images
	if len(images) != 1 || images[0].Filter != "DCTDecode" || !bytes.Equal(images[0].Data, jpg.Bytes()) {
		t.Fatalf("images = %+v warnings %v", images, doc.Warnings)
	}
	img, err := images[0].Decode()
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 6 || img.Bounds().Dy() != 4 {
		t.Errorf("decoded bounds = %v", img.Bounds())
	}
	if got := img.(*image.Gray).GrayAt(5, 3).Y; math.Abs(float64(got)-230) > 8 {
		t.Errorf("pixel = %d, want ≈230", got)
	}
}

func TestImageLoadErrors(t *testing.T) {
	t.Run("stream failure is a warning and the image is dropped", func(t *testing.T) {
		data := imageDoc("q 10 0 0 10 0 0 cm /Im1 Do Q /Im1 Do",
			pdftest.Stream("/Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /FlateDecode", "not flate"))
		doc, err := extractOptions(t, data, Options{IncludeImages: true})
		if err != nil {
			t.Fatal(err)
		}
		if n := len(doc.Pages[0].Images); n != 0 {
			t.Errorf("images = %d", n)
		}
		// The failure is reported once although the image is painted twice.
		if n := len(doc.Warnings); n != 1 || doc.Warnings[0].Code != WarningStream {
			t.Errorf("warnings = %v", doc.Warnings)
		}
		_, err = extractOptions(t, data, Options{IncludeImages: true, Strict: true})
		var strict *StrictError
		if !errors.As(err, &strict) || strict.Warning.Code != WarningStream {
			t.Errorf("strict err = %v", err)
		}
		// Not asked for: never read, never reported.
		doc, err = extractOptions(t, data, Options{})
		if err != nil || len(doc.Warnings) != 0 {
			t.Errorf("without IncludeImages: warnings %v err %v", doc.Warnings, err)
		}
	})

	t.Run("invalid dimensions", func(t *testing.T) {
		data := imageDoc("/Im1 Do", grayImageObj(0, 5, "", ""))
		doc, err := extractOptions(t, data, Options{IncludeImages: true})
		if err != nil || len(doc.Pages[0].Images) != 0 || !hasWarning(doc.Warnings, WarningStream) {
			t.Errorf("images %+v warnings %v err %v", doc.Pages[0].Images, doc.Warnings, err)
		}
	})

	t.Run("per-page image limit", func(t *testing.T) {
		data := imageDoc(strings.Repeat("/Im1 Do ", 5), grayImageObj(1, 1, "\x00", ""))
		doc, err := extractOptions(t, data, Options{IncludeImages: true, Limits: Limits{MaxImagesPerPage: 3}})
		if err != nil || len(doc.Pages[0].Images) != 3 || !hasWarning(doc.Warnings, WarningWorkLimit) {
			t.Errorf("images %d warnings %v err %v", len(doc.Pages[0].Images), doc.Warnings, err)
		}
	})

	t.Run("stream limit", func(t *testing.T) {
		data := imageDoc("/Im1 Do", grayImageObj(4, 4, strings.Repeat("x", 16), ""))
		doc, err := extractOptions(t, data, Options{IncludeImages: true, Limits: Limits{MaxStreamBytes: 8}})
		if err != nil || len(doc.Pages[0].Images) != 0 || !hasWarning(doc.Warnings, WarningStreamLimit) {
			t.Errorf("images %d warnings %v err %v", len(doc.Pages[0].Images), doc.Warnings, err)
		}
	})

	t.Run("per-page image byte budget", func(t *testing.T) {
		// Three distinct 16-byte images; a 40-byte budget admits two.
		objs := []string{
			pdftest.Catalog(2),
			pdftest.Pages(3),
			pdftest.Page(2, 4, "<< /XObject << /A 5 0 R /B 6 0 R /C 7 0 R >> >>"),
			pdftest.Stream("", "/A Do /B Do /C Do /A Do"),
			grayImageObj(4, 4, strings.Repeat("a", 16), ""),
			grayImageObj(4, 4, strings.Repeat("b", 16), ""),
			grayImageObj(4, 4, strings.Repeat("c", 16), ""),
		}
		doc, err := extractOptions(t, pdftest.Build(1, objs...), Options{IncludeImages: true, Limits: Limits{MaxImageBytesPerPage: 40}})
		if err != nil {
			t.Fatal(err)
		}
		// A and B load; C is over budget; the repeated A shares its data.
		if n := len(doc.Pages[0].Images); n != 3 || !hasWarning(doc.Warnings, WarningStreamLimit) {
			t.Errorf("images %d warnings %v", n, doc.Warnings)
		}
	})

	t.Run("inline filter chain limit", func(t *testing.T) {
		data := simpleDoc("BI /W 1 /H 1 /BPC 8 /CS /G /F [/AHx /AHx /AHx /AHx /AHx /AHx /AHx /AHx /AHx] ID 00> EI")
		doc, err := extractOptions(t, data, Options{IncludeImages: true})
		if err != nil || len(doc.Pages[0].Images) != 0 || !hasWarning(doc.Warnings, WarningStream) {
			t.Errorf("images %d warnings %v err %v", len(doc.Pages[0].Images), doc.Warnings, err)
		}
	})
}

func TestImageDecodeSamples(t *testing.T) {
	decodeOne := func(t *testing.T, dict, data string) image.Image {
		t.Helper()
		doc, err := extractOptions(t, imageDoc("/Im1 Do", pdftest.Stream("/Type /XObject /Subtype /Image "+dict, data)), Options{IncludeImages: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Pages[0].Images) != 1 {
			t.Fatalf("images %+v warnings %v", doc.Pages[0].Images, doc.Warnings)
		}
		img, err := doc.Pages[0].Images[0].Decode()
		if err != nil {
			t.Fatal(err)
		}
		return img
	}
	decodeErr := func(t *testing.T, dict, data string) error {
		t.Helper()
		doc, err := extractOptions(t, imageDoc("/Im1 Do", pdftest.Stream("/Type /XObject /Subtype /Image "+dict, data)), Options{IncludeImages: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Pages[0].Images) != 1 {
			t.Fatalf("images %+v warnings %v", doc.Pages[0].Images, doc.Warnings)
		}
		_, err = doc.Pages[0].Images[0].Decode()
		return err
	}

	t.Run("gray 8", func(t *testing.T) {
		img := decodeOne(t, "/Width 2 /Height 2 /ColorSpace /DeviceGray /BitsPerComponent 8", "\x00\x80\xff\x40")
		g := img.(*image.Gray)
		if g.GrayAt(1, 0).Y != 0x80 || g.GrayAt(1, 1).Y != 0x40 {
			t.Errorf("pixels = %v", g.Pix)
		}
	})
	t.Run("gray 1 inverted", func(t *testing.T) {
		img := decodeOne(t, "/Width 3 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 1 /Decode [1 0]", "\xa0")
		g := img.(*image.Gray)
		if g.GrayAt(0, 0).Y != 0 || g.GrayAt(1, 0).Y != 255 || g.GrayAt(2, 0).Y != 0 {
			t.Errorf("pixels = %v", g.Pix)
		}
	})
	t.Run("gray 4", func(t *testing.T) {
		img := decodeOne(t, "/Width 2 /Height 1 /ColorSpace [/CalGray << >>] /BitsPerComponent 4", "\xf0")
		g := img.(*image.Gray)
		if g.GrayAt(0, 0).Y != 255 || g.GrayAt(1, 0).Y != 0 {
			t.Errorf("pixels = %v", g.Pix)
		}
	})
	t.Run("gray 16", func(t *testing.T) {
		img := decodeOne(t, "/Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 16", "\x12\x34")
		if g := img.(*image.Gray16); g.Gray16At(0, 0).Y != 0x1234 {
			t.Errorf("pixel = %v", g.Gray16At(0, 0))
		}
	})
	t.Run("rgb 8", func(t *testing.T) {
		img := decodeOne(t, "/Width 1 /Height 1 /ColorSpace /DeviceRGB /BitsPerComponent 8", "\x10\x20\x30")
		if c := img.(*image.RGBA).RGBAAt(0, 0); c != (color.RGBA{R: 0x10, G: 0x20, B: 0x30, A: 0xFF}) {
			t.Errorf("pixel = %v", c)
		}
	})
	t.Run("cmyk 8 via ICC", func(t *testing.T) {
		img := decodeOne(t, "/Width 1 /Height 1 /ColorSpace [/ICCBased 6 0 R] /BitsPerComponent 8", "\x01\x02\x03\x04")
		_ = img
	})
	t.Run("indexed 2 bits over gray", func(t *testing.T) {
		img := decodeOne(t, "/Width 4 /Height 1 /ColorSpace [/Indexed /DeviceGray 3 <00 55 aa ff>] /BitsPerComponent 2", "\x1b")
		pal := img.(*image.Paletted)
		// 0x1b = 00 01 10 11
		for x, want := range []uint8{0x00, 0x55, 0xaa, 0xff} {
			if got := color.GrayModel.Convert(pal.At(x, 0)).(color.Gray).Y; got != want {
				t.Errorf("pixel %d = %d, want %d", x, got, want)
			}
		}
	})
	t.Run("separation is inverted tint", func(t *testing.T) {
		img := decodeOne(t, "/Width 1 /Height 1 /ColorSpace [/Separation /Spot /DeviceGray 6 0 R] /BitsPerComponent 8", "\xff")
		if g := img.(*image.Gray).GrayAt(0, 0).Y; g != 0 {
			t.Errorf("full tint = %d, want black", g)
		}
	})
	t.Run("image mask", func(t *testing.T) {
		img := decodeOne(t, "/Width 2 /Height 1 /ImageMask true", "\x80")
		g := img.(*image.Gray)
		if g.GrayAt(0, 0).Y != 255 || g.GrayAt(1, 0).Y != 0 {
			t.Errorf("mask = %v", g.Pix)
		}
	})
	t.Run("unsupported", func(t *testing.T) {
		for name, dict := range map[string]string{
			"lab": "/Width 1 /Height 1 /ColorSpace [/Lab << >>] /BitsPerComponent 8",
			"jpx": "/Width 1 /Height 1 /Filter /JPXDecode",
		} {
			if err := decodeErr(t, dict, "\x00\x00\x00"); !errors.Is(err, errors.ErrUnsupported) {
				t.Errorf("%s: err = %v", name, err)
			}
		}
	})
	t.Run("pixel limit", func(t *testing.T) {
		doc, err := extractOptions(t, imageDoc("/Im1 Do", grayImageObj(100, 100, strings.Repeat("\x00", 10000), "")),
			Options{IncludeImages: true, Limits: Limits{MaxImagePixels: 5000}})
		if err != nil || len(doc.Pages[0].Images) != 1 {
			t.Fatalf("images %+v err %v", doc.Pages[0].Images, err)
		}
		if _, err := doc.Pages[0].Images[0].Decode(); !errors.Is(err, ErrImageTooLarge) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestImageDecodeICCBased(t *testing.T) {
	objs := []string{
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /XObject << /Im1 5 0 R >> >>"),
		pdftest.Stream("", "/Im1 Do"),
		pdftest.Stream("/Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace [/ICCBased 6 0 R] /BitsPerComponent 8", "\x01\x02\x03\x04"),
		pdftest.Stream("/N 4", "icc"),
	}
	doc, err := extractOptions(t, pdftest.Build(1, objs...), Options{IncludeImages: true})
	if err != nil || len(doc.Pages[0].Images) != 1 {
		t.Fatalf("images %+v err %v", doc.Pages[0].Images, err)
	}
	im := doc.Pages[0].Images[0]
	if im.ColorSpace != "ICCBased" || im.Components != 4 {
		t.Fatalf("image = %+v", im)
	}
	img, err := im.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if c := img.(*image.CMYK).CMYKAt(0, 0); c != (color.CMYK{C: 1, M: 2, Y: 3, K: 4}) {
		t.Errorf("pixel = %v", c)
	}
}

func TestScannedFixtures(t *testing.T) {
	// Real scanning-pipeline encodings of the same rendered text; each
	// page is one image and nothing else. Provenance: testdata/images/gen.py.
	for _, tc := range []struct {
		file   string
		filter string
	}{
		{"scan-jpeg.pdf", "DCTDecode"},
		{"scan-g4.pdf", "CCITTFaxDecode"},
		{"scan-jbig2.pdf", "JBIG2Decode"},
		{"scan-jbig2-generic.pdf", "JBIG2Decode"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "images", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			doc, err := extractOptions(t, data, Options{IncludeImages: true})
			if err != nil {
				t.Fatal(err)
			}
			page := doc.Pages[0]
			if len(page.Glyphs) != 0 || len(page.Images) != 1 || len(page.Warnings) != 0 {
				t.Fatalf("glyphs %d images %+v warnings %v", len(page.Glyphs), page.Images, page.Warnings)
			}
			im := page.Images[0]
			if im.Filter != tc.filter || im.Width != 620 || im.Height != 170 {
				t.Fatalf("image = %+v", im)
			}
			if b := im.Bounds(); b.MinX != 0 || b.MinY != 0 || math.Abs(b.MaxX-620.0*72/150) > 0.5 {
				t.Errorf("bounds = %+v", b)
			}
			img, err := im.Decode()
			if err != nil {
				t.Fatal(err)
			}
			if img.Bounds().Dx() != 620 || img.Bounds().Dy() != 170 {
				t.Fatalf("decoded bounds = %v", img.Bounds())
			}
			// Dark text on a light page: the background is light, the
			// ink covers a few percent of the pixels, and the ink sits
			// where the text was rendered (left/top quarter of the page).
			dark, total, darkTopLeft := 0, 0, 0
			for y := range 170 {
				for x := range 620 {
					total++
					if color.GrayModel.Convert(img.At(x, y)).(color.Gray).Y < 128 {
						dark++
						if x < 310 && y < 60 {
							darkTopLeft++
						}
					}
				}
			}
			ratio := float64(dark) / float64(total)
			if ratio < 0.02 || ratio > 0.2 {
				t.Errorf("dark pixel ratio = %.3f, want text-like coverage", ratio)
			}
			if darkTopLeft == 0 {
				t.Error("no ink where the first line of text was rendered")
			}
		})
	}
}

// ocrCounter records OCR calls and returns one positioned glyph per image
// on the page (or one for the page when it has none).
type ocrCounter struct {
	calls atomic.Int32
	err   error
}

func (o *ocrCounter) ExtractPage(_ context.Context, req OCRRequest) ([]Glyph, error) {
	o.calls.Add(1)
	var glyphs []Glyph
	for i, im := range req.Page.Images {
		origin := im.ToPage(0, float64(im.Height))
		glyphs = append(glyphs, Glyph{Text: fmt.Sprintf("ocr%d", i), X: origin.X, Y: origin.Y, Advance: 30, Size: 10})
	}
	if len(glyphs) == 0 {
		glyphs = []Glyph{{Text: "scanned", X: 10, Y: 20, Advance: 40, Size: 12}}
	}
	return glyphs, o.err
}

func TestOCRPolicies(t *testing.T) {
	mixed := imageDoc("q 100 0 0 50 100 600 cm /Im1 Do Q BT /F1 12 Tf 72 700 Td (typeset) Tj ET",
		grayImageObj(1, 1, "\x00", ""))
	textOnly := simpleDoc("BT /F1 12 Tf 72 700 Td (typeset) Tj ET")
	imageOnly := imageDoc("q 100 0 0 50 100 600 cm /Im1 Do Q", grayImageObj(1, 1, "\x00", ""))
	vectorOnly := simpleDoc("0 0 m 100 100 l S")

	for _, tc := range []struct {
		name     string
		policy   OCRPolicy
		doc      []byte
		called   bool
		text     string
		ocrCount int
	}{
		{"textless skips text pages", OCRTextlessPages, textOnly, false, "typeset", 0},
		{"textless skips mixed pages", OCRTextlessPages, mixed, false, "typeset", 0},
		{"textless takes image pages", OCRTextlessPages, imageOnly, true, "ocr0", 1},
		{"textless takes vector pages", OCRTextlessPages, vectorOnly, true, "scanned", 1},
		{"image pages skips text pages", OCRImagePages, textOnly, false, "typeset", 0},
		{"image pages takes mixed pages", OCRImagePages, mixed, true, "typeset\nocr0", 1},
		{"image pages skips vector pages", OCRImagePages, vectorOnly, false, "", 0},
		{"all pages", OCRAllPages, textOnly, true, "typeset\nscanned", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ocr := &ocrCounter{}
			doc, err := extractOptions(t, tc.doc, Options{OCR: ocr, OCRPolicy: tc.policy})
			if err != nil {
				t.Fatal(err)
			}
			if called := ocr.calls.Load() == 1; called != tc.called {
				t.Errorf("called = %v, want %v", called, tc.called)
			}
			page := doc.Pages[0]
			if got := page.Text(); got != tc.text {
				t.Errorf("text = %q, want %q", got, tc.text)
			}
			if page.OCRGlyphs != tc.ocrCount {
				t.Errorf("OCRGlyphs = %d, want %d", page.OCRGlyphs, tc.ocrCount)
			}
			if page.Images != nil {
				t.Errorf("images retained without IncludeImages: %+v", page.Images)
			}
		})
	}

	t.Run("images are positioned for the implementation", func(t *testing.T) {
		doc, err := extractOptions(t, imageOnly, Options{OCR: &ocrCounter{}, IncludeImages: true})
		if err != nil {
			t.Fatal(err)
		}
		page := doc.Pages[0]
		if len(page.Images) != 1 || len(page.Glyphs) != 1 || page.OCRGlyphs != 1 {
			t.Fatalf("page = %+v", page)
		}
		if g := page.Glyphs[0]; !near(g.X, 100) || !near(g.Y, 600) {
			t.Errorf("OCR glyph origin = (%v, %v), want the image's bottom-left (100, 600)", g.X, g.Y)
		}
	})

	t.Run("partial glyphs with an error are kept and warned", func(t *testing.T) {
		ocr := &ocrCounter{err: errors.New("engine timed out")}
		doc, err := extractOptions(t, imageOnly, Options{OCR: ocr})
		if err != nil {
			t.Fatal(err)
		}
		if doc.Pages[0].Text() != "ocr0" || !hasWarning(doc.Warnings, WarningOCR) {
			t.Errorf("text %q warnings %v", doc.Pages[0].Text(), doc.Warnings)
		}
	})

	t.Run("images of unselected pages are never read", func(t *testing.T) {
		broken := imageDoc("q 10 0 0 10 0 0 cm /Im1 Do Q BT /F1 12 Tf 72 700 Td (typeset) Tj ET",
			pdftest.Stream("/Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /FlateDecode", "not flate"))
		doc, err := extractOptions(t, broken, Options{OCR: &ocrCounter{}})
		if err != nil || len(doc.Warnings) != 0 {
			t.Errorf("warnings %v err %v", doc.Warnings, err)
		}
	})

	t.Run("invalid policy", func(t *testing.T) {
		if _, err := extractOptions(t, textOnly, Options{OCRPolicy: OCRPolicy(9)}); err == nil {
			t.Error("accepted")
		}
	})
}

func TestOCRRunsConcurrently(t *testing.T) {
	const pages = 24
	objs := []string{pdftest.Catalog(2)}
	kids := make([]int, 0, pages)
	for i := range pages {
		// objects: page = 3+2i, contents = 4+2i
		kids = append(kids, 3+2*i)
	}
	objs = append(objs, pdftest.Pages(kids...))
	for i := range pages {
		objs = append(objs,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R >>", 4+2*i),
			pdftest.Stream("", fmt.Sprintf("%% page %d", i+1)),
		)
	}
	data := pdftest.Build(1, objs...)

	// Each call holds until a second call has started, so concurrent
	// workers are observed whatever their timing; a sequential run waits
	// out the deadline and reports a peak of one.
	var started, inFlight, peak atomic.Int32
	ocr := OCRFunc(func(_ context.Context, req OCRRequest) ([]Glyph, error) {
		started.Add(1)
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for deadline := time.Now().Add(2 * time.Second); started.Load() < 2 && time.Now().Before(deadline); {
			runtime.Gosched()
		}
		n = max(n, inFlight.Load())
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		return []Glyph{{Text: fmt.Sprintf("p%d", req.PageNumber), X: 10, Y: 20, Advance: 20, Size: 10}}, nil
	})
	doc, err := extractOptions(t, data, Options{OCR: ocr, Concurrency: 4})
	if err != nil {
		t.Fatal(err)
	}
	for i, page := range doc.Pages {
		if page.Text() != fmt.Sprintf("p%d", i+1) {
			t.Errorf("page %d text = %q", i+1, page.Text())
		}
	}
	if peak.Load() < 2 {
		t.Errorf("OCR peak concurrency = %d, want > 1 with Concurrency 4", peak.Load())
	}
}

func TestExtractPagesYieldsImages(t *testing.T) {
	data := imageDoc("q 10 0 0 10 0 0 cm /Im1 Do Q", grayImageObj(1, 1, "\x00", ""))
	seen := 0
	_, err := ExtractPages(context.Background(), bytes.NewReader(data), int64(len(data)), Options{IncludeImages: true}, func(p Page) error {
		seen += len(p.Images)
		return nil
	})
	if err != nil || seen != 1 {
		t.Fatalf("seen %d err %v", seen, err)
	}
}

func TestCorpusImages(t *testing.T) {
	// The real-document corpus: image-bearing files report their images
	// with sane geometry, and collecting images never changes the text.
	for _, path := range corpusFiles(t, false) {
		name := strings.TrimSuffix(filepath.Base(path), ".pdf")
		t.Run(name, func(t *testing.T) {
			data := readCorpusFile(t, path)
			plain, err := Extract(t.Context(), bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			withImages, err := extractOptions(t, data, Options{IncludeImages: true})
			if err != nil {
				t.Fatal(err)
			}
			if plain.Text() != withImages.Text() {
				t.Error("text differs when images are collected")
			}
			images := 0
			for _, page := range withImages.Pages {
				for _, im := range page.Images {
					images++
					if im.Width <= 0 || im.Height <= 0 || len(im.Data) == 0 {
						t.Errorf("page %d: image %+v", page.Number, im)
					}
					if b := im.Bounds(); b.MaxX <= b.MinX || b.MaxY <= b.MinY {
						t.Errorf("page %d: degenerate bounds %+v", page.Number, b)
					}
				}
			}
			t.Logf("%s: %d images", name, images)
		})
	}
}

func FuzzExtractImages(f *testing.F) {
	f.Add(imageDoc("q 200 0 0 100 50 600 cm /Im1 Do Q", grayImageObj(4, 2, "\x00\x40\x80\xff\x10\x20\x30\x40", "")))
	f.Add(simpleDoc("q 80 0 0 20 10 10 cm BI /W 4 /H 2 /BPC 8 /CS /G ID ab EI cd EI Q BI /W 8 /H 1 /IM true /F /AHx ID a5> EI"))
	f.Add(imageDoc("/Im1 Do", pdftest.Stream("/Type /XObject /Subtype /Image /Width 4 /Height 1 /ColorSpace [/Indexed /DeviceRGB 1 <ff000000ff00>] /BitsPerComponent 1", "\x40")))
	for _, name := range []string{"scan-jpeg.pdf", "scan-g4.pdf", "scan-jbig2.pdf", "scan-jbig2-generic.pdf"} {
		if data, err := os.ReadFile(filepath.Clean(filepath.Join("testdata", "images", name))); err == nil {
			f.Add(data)
		}
	}
	f.Fuzz(func(_ *testing.T, data []byte) {
		// Collecting and decoding images must never panic or hang, and
		// must stay within the pixel limit however large an image claims
		// to be.
		doc, err := ExtractWithOptions(context.Background(), bytes.NewReader(data), int64(len(data)), Options{
			IncludeImages: true,
			Limits:        Limits{MaxImagePixels: 1 << 16, MaxStreamBytes: 1 << 20},
		})
		if err != nil {
			return
		}
		for _, page := range doc.Pages {
			for _, im := range page.Images {
				img, err := im.Decode()
				if err == nil && img.Bounds().Dx()*img.Bounds().Dy() > 1<<16 {
					panic("decoded image exceeds the pixel limit")
				}
			}
		}
	})
}

// Page.ImageCount is what separates a scanned page from one whose text
// became vector outlines. Both yield no glyphs, and only one of them is
// worth handing to an OCR engine, so the count is reported whether or
// not the data behind it was read.
func TestPageImageCount(t *testing.T) {
	twice := imageDoc(strings.Repeat("/Im1 Do ", 2), grayImageObj(1, 1, "\x00", ""))

	t.Run("without image data", func(t *testing.T) {
		doc, err := extractOptions(t, twice, Options{})
		if err != nil {
			t.Fatalf("extract: %v", err)
		}
		page := doc.Pages[0]
		if page.ImageCount != 2 {
			t.Errorf("ImageCount = %d, want 2", page.ImageCount)
		}
		// Counting must not start reading what nobody asked for.
		if len(page.Images) != 0 {
			t.Errorf("Images = %d, want none without IncludeImages", len(page.Images))
		}
	})

	t.Run("with image data", func(t *testing.T) {
		doc, err := extractOptions(t, twice, Options{IncludeImages: true})
		if err != nil {
			t.Fatalf("extract: %v", err)
		}
		if page := doc.Pages[0]; page.ImageCount != 2 || len(page.Images) != 2 {
			t.Errorf("ImageCount = %d, Images = %d, want 2 and 2", page.ImageCount, len(page.Images))
		}
	})

	t.Run("page painting none", func(t *testing.T) {
		doc, err := extractOptions(t, imageDoc("", grayImageObj(1, 1, "\x00", "")), Options{})
		if err != nil {
			t.Fatalf("extract: %v", err)
		}
		// The image is in the page's resources but never painted.
		if got := doc.Pages[0].ImageCount; got != 0 {
			t.Errorf("ImageCount = %d, want 0", got)
		}
	})

	// A count that depended on IncludeImages would be a count a caller
	// could not compare across extractions.
	t.Run("bounded the same either way", func(t *testing.T) {
		data := imageDoc(strings.Repeat("/Im1 Do ", 5), grayImageObj(1, 1, "\x00", ""))
		for _, include := range []bool{false, true} {
			doc, err := extractOptions(t, data, Options{IncludeImages: include, Limits: Limits{MaxImagesPerPage: 3}})
			if err != nil {
				t.Fatalf("IncludeImages=%v: %v", include, err)
			}
			if got := doc.Pages[0].ImageCount; got != 3 {
				t.Errorf("IncludeImages=%v: ImageCount = %d, want 3", include, got)
			}
			if !hasWarning(doc.Warnings, WarningWorkLimit) {
				t.Errorf("IncludeImages=%v: warnings = %v, want the work limit reported", include, doc.Warnings)
			}
		}
	})
}
