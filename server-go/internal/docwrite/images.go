package docwrite

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // decoders for image.Decode
	"image/jpeg"
	"image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// ImageSource reads an image a document refers to (the destination of a
// Markdown image) and returns its bytes. A nil source embeds no images.
type ImageSource func(ref string) ([]byte, error)

// picture is an image ready to embed: JPEG kept as it came, anything
// else as an 8-bit PNG, which both the PDF and the Word writer take.
type picture struct {
	data []byte
	kind string // "JPG" or "PNG"
	w, h int    // pixels
}

const (
	maxImageBytes  = 25 << 20
	maxImagePixels = 50_000_000
	maxImages      = 100
)

func (p *picture) ext() string {
	if p.kind == "JPG" {
		return "jpeg"
	}
	return "png"
}

// size is the picture's size in points at 96 pixels an inch (a screen's
// pixel), scaled down to fit maxW by maxH.
func (p *picture) size(maxW, maxH float64) (float64, float64) {
	w, h := float64(p.w)*72/96, float64(p.h)*72/96
	if w > maxW {
		h, w = h*maxW/w, maxW
	}
	if h > maxH {
		w, h = w*maxH/h, maxH
	}
	return w, h
}

func loadPicture(data []byte) (*picture, error) {
	if len(data) > maxImageBytes {
		return nil, fmt.Errorf("over %d MB", maxImageBytes>>20)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("not a PNG, JPEG, GIF, WebP, BMP or TIFF image")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxImagePixels {
		return nil, fmt.Errorf("%dx%d pixels is too large", cfg.Width, cfg.Height)
	}
	if format == "jpeg" && cfg.ColorModel != color.CMYKModel {
		// A JPEG embeds as it came; a CMYK one is redrawn as RGB below.
		return &picture{data: data, kind: "JPG", w: cfg.Width, h: cfg.Height}, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("the image could not be read: " + err.Error())
	}
	b := img.Bounds()
	rgba := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
	var buf bytes.Buffer
	if format == "jpeg" {
		if err := jpeg.Encode(&buf, rgba, &jpeg.Options{Quality: 92}); err != nil {
			return nil, err
		}
		return &picture{data: buf.Bytes(), kind: "JPG", w: b.Dx(), h: b.Dy()}, nil
	}
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, rgba); err != nil {
		return nil, err
	}
	return &picture{data: buf.Bytes(), kind: "PNG", w: b.Dx(), h: b.Dy()}, nil
}

// pictures loads every image the blocks refer to. What could not be
// embedded is listed with why; the writers show its alt text in its place.
func pictures(blocks []Block, src ImageSource) (map[string]*picture, []string) {
	refs := Images(blocks)
	if len(refs) == 0 {
		return nil, nil
	}
	out := map[string]*picture{}
	var missing []string
	for i, ref := range refs {
		if src == nil {
			missing = append(missing, ref+" (images are not embedded here)")
			continue
		}
		if i >= maxImages {
			missing = append(missing, ref+fmt.Sprintf(" (over %d images)", maxImages))
			continue
		}
		data, err := src(ref)
		if err != nil {
			missing = append(missing, ref+" ("+err.Error()+")")
			continue
		}
		p, err := loadPicture(data)
		if err != nil {
			missing = append(missing, ref+" ("+err.Error()+")")
			continue
		}
		out[ref] = p
	}
	return out, missing
}
