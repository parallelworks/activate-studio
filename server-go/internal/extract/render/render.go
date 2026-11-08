// Package render draws PDF pages as greyscale images for OCR, where
// poppler's pdftoppm is not installed (the macOS app has none). It draws
// what a page that needs OCR holds: filled and stroked paths, which is
// text converted to outlines, and images, which is a scan, in content
// order, with clipping, the page's crop box and rotation. It leaves out
// text shown with fonts (the PDF's text layer already holds it), shadings
// and patterns, and dashes.
//
// Content streams, resources and form XObjects are read with
// github.com/pdfcpu/pdfcpu; images are read and decoded by
// github.com/giraffesyo/pdf, the reader downmark uses (DCT, CCITT fax,
// JBIG2, 1 to 16 bit samples in the device, ICC, indexed and separation
// colour spaces, and stencil masks), and placed where it reports them.
// JPEG 2000 images are not drawn. When pdfcpu cannot read a file, the
// page's images alone are drawn.
//
// A page never panics the caller and stops at its context's deadline.
package render

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"sync"
	"time"

	gpdf "github.com/giraffesyo/pdf"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
)

// Limits on one page.
const (
	// MaxPixels bounds a page's canvas; a larger page is drawn at a lower
	// resolution.
	MaxPixels = 60 << 20
	// maxImagePixels bounds one decoded image.
	maxImagePixels = 80 << 20
	// maxContent bounds a page's decoded content streams, forms included.
	maxContent = 64 << 20
	// maxOps bounds the operators a page runs.
	maxOps = 4 << 20
	// maxFormDepth bounds nested form XObjects.
	maxFormDepth = 16
	// maxPathPoints bounds one path, flattened.
	maxPathPoints = 4 << 20
)

// ErrTooComplex means a page went past one of the limits above, or its
// context ended; what was drawn by then is returned with it.
var ErrTooComplex = errors.New("render: page too complex")

// Doc is an open PDF.
type Doc struct {
	f     *os.File
	size  int64
	pc    *model.Context // nil when pdfcpu cannot read the file
	pages int
}

var configOnce sync.Once

// maxPdfcpuFile is the largest file whose objects pdfcpu reads; larger
// files, and files it cannot read within pdfcpuTimeout, have their
// images drawn and nothing else.
const maxPdfcpuFile = 512 << 20

var pdfcpuTimeout = 60 * time.Second

// Open opens a PDF for rendering. ctx bounds the opening only.
func Open(ctx context.Context, path string) (d *Doc, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	d = &Doc{f: f, size: st.Size()}
	if d.size <= maxPdfcpuFile {
		d.pc = readPdfcpu(ctx, path)
	}
	err = guard(func() error {
		doc, err := gpdf.ExtractWithOptions(ctx, f, d.size, gpdf.Options{Pages: []gpdf.PageRange{{First: 1, Last: 1}}, Concurrency: 1})
		if err == nil {
			d.pages = doc.PageCount
		}
		return err
	})
	if err != nil && d.pc != nil {
		d.pages, err = d.pc.PageCount, nil
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("render: %w", err)
	}
	return d, nil
}

// readPdfcpu reads the file's objects with pdfcpu, or returns nil. pdfcpu
// cannot be stopped part way, so a read past the deadline is left to
// finish on its own file handle and its result dropped.
func readPdfcpu(ctx context.Context, path string) *model.Context {
	configOnce.Do(func() {
		// pdfcpu otherwise writes a configuration folder under the
		// user's config directory, and exits the process if it cannot.
		model.ConfigPath = "disable"
	})
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	done := make(chan *model.Context, 1)
	go func() {
		defer f.Close()
		var pc *model.Context
		guard(func() error {
			conf := model.NewDefaultConfiguration()
			conf.ValidationMode = model.ValidationRelaxed
			c, err := pdfcpu.Read(f, conf)
			if err != nil {
				return err
			}
			if err := c.EnsurePageCount(); err != nil {
				return err
			}
			pc = c
			return nil
		})
		done <- pc
	}()
	t := time.NewTimer(pdfcpuTimeout)
	defer t.Stop()
	select {
	case pc := <-done:
		return pc
	case <-t.C:
	case <-ctx.Done():
	}
	return nil
}

// Close closes the file.
func (d *Doc) Close() error { return d.f.Close() }

// PageCount is the number of pages.
func (d *Doc) PageCount() int { return d.pages }

// guard turns a panic in a PDF library into an error.
func guard(fn func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("render: %v", p)
		}
	}()
	return fn()
}

// Page draws page n (from 1) at dpi dots per inch. On ErrTooComplex or a
// deadline it returns what it drew with the error.
func (d *Doc) Page(ctx context.Context, n int, dpi float64) (img *image.Gray, err error) {
	var r *renderer
	err = guard(func() error {
		var err error
		r, err = d.page(ctx, n, dpi)
		return err
	})
	if r != nil {
		img = r.canvas
	}
	if img == nil && err == nil {
		err = errors.New("render: no page")
	}
	return img, err
}

func (d *Doc) page(ctx context.Context, n int, dpi float64) (*renderer, error) {
	if n < 1 || n > d.pages {
		return nil, fmt.Errorf("render: no page %d", n)
	}
	// The page's images and geometry, by the reader downmark uses.
	var gp *gpdf.Page
	guard(func() error {
		doc, err := gpdf.ExtractWithOptions(ctx, d.f, d.size, gpdf.Options{
			Pages: []gpdf.PageRange{{First: n, Last: n}}, IncludeImages: true, Concurrency: 1,
			Limits: gpdf.Limits{MaxImagePixels: maxImagePixels},
		})
		if err == nil && len(doc.Pages) == 1 {
			gp = &doc.Pages[0]
		}
		return err
	})
	var pageDict types.Dict
	var inh *model.InheritedPageAttrs
	if d.pc != nil {
		guard(func() error {
			pageDict, inh = findPage(d.pc, n)
			return nil
		})
	}
	box, rot, ok := geometry(gp, inh)
	if !ok {
		return nil, errors.New("render: page has no size")
	}
	r := newRenderer(ctx, d.pc, box, rot, dpi)
	if gp != nil {
		r.images = gp.Images
	}
	var err error
	if pageDict != nil && inh != nil {
		err = r.walkPage(pageDict, inh.Resources)
	}
	if r.matched == 0 {
		// pdfcpu read nothing, or matched none of the images it met with
		// the reader's: draw the reader's images in its order.
		for i := range r.images {
			r.drawImage(&r.images[i], nil, r.st.clip)
		}
	}
	return r, err
}

// box is a page rectangle in PDF units.
type box struct{ x0, y0, x1, y1 float64 }

// geometry is the page's visible box (the crop box within the media box,
// as pdftoppm draws it) and rotation.
func geometry(gp *gpdf.Page, inh *model.InheritedPageAttrs) (box, int, bool) {
	var b box
	rot := 0
	switch {
	case gp != nil:
		m := box{gp.MediaBox.MinX, gp.MediaBox.MinY, gp.MediaBox.MaxX, gp.MediaBox.MaxY}
		b = m
		if c := gp.CropBox; c.MaxX > c.MinX && c.MaxY > c.MinY {
			b = box{math.Max(c.MinX, m.x0), math.Max(c.MinY, m.y0), math.Min(c.MaxX, m.x1), math.Min(c.MaxY, m.y1)}
			if b.x1 <= b.x0 || b.y1 <= b.y0 {
				b = m
			}
		}
		rot = gp.Rotation
	case inh != nil && inh.MediaBox != nil:
		mb := inh.MediaBox
		b = box{math.Min(mb.LL.X, mb.UR.X), math.Min(mb.LL.Y, mb.UR.Y), math.Max(mb.LL.X, mb.UR.X), math.Max(mb.LL.Y, mb.UR.Y)}
		if cb := inh.CropBox; cb != nil {
			c := box{math.Max(math.Min(cb.LL.X, cb.UR.X), b.x0), math.Max(math.Min(cb.LL.Y, cb.UR.Y), b.y0),
				math.Min(math.Max(cb.LL.X, cb.UR.X), b.x1), math.Min(math.Max(cb.LL.Y, cb.UR.Y), b.y1)}
			if c.x1 > c.x0 && c.y1 > c.y0 {
				b = c
			}
		}
		rot = inh.Rotate
	default:
		return b, 0, false
	}
	rot = ((rot % 360) + 360) % 360 / 90 * 90
	ok := b.x1-b.x0 >= 1 && b.y1-b.y0 >= 1 && !math.IsInf(b.x1-b.x0, 0) && !math.IsNaN(b.x0+b.y0+b.x1+b.y1)
	return b, rot, ok
}

// mat is a PDF transformation [a b c d e f]: x' = a x + c y + e,
// y' = b x + d y + f.
type mat [6]float64

// mul is m followed by n.
func (m mat) mul(n mat) mat {
	return mat{
		m[0]*n[0] + m[1]*n[2], m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2], m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4], m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

func (m mat) apply(x, y float64) pt {
	return pt{m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]}
}

func (m mat) finite() bool {
	for _, v := range m {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 1e9 {
			return false
		}
	}
	return true
}

// paint is a colour to paint with: grey, opacity, or nothing (a pattern).
type paint struct {
	gray  uint8
	alpha float64
	none  bool
}

type gstate struct {
	ctm           mat
	fill, stroke  paint
	fillCS, strCS *colorSpace
	lineWidth     float64
	clip          *image.Alpha // nil: the whole page
}

type renderer struct {
	ctx     context.Context
	pc      *model.Context
	canvas  *image.Gray
	base    mat // page space to device
	st      gstate
	stack   []gstate
	ops     int
	content int
	err     error

	// The path being built, in device space.
	subs   []polygon
	closed []bool
	cur    pt // current point, user space
	start  pt
	clipOp int // 0, or 1 (W) or 2 (W*) pending for the next painting operator

	images  []gpdf.Image // the reader's images, in content order
	next    int          // the first of them not drawn yet
	matched int
}

func newRenderer(ctx context.Context, pc *model.Context, b box, rot int, dpi float64) *renderer {
	w, h := b.x1-b.x0, b.y1-b.y0
	s := dpi / 72
	if px := w * s * h * s; px > MaxPixels {
		s *= math.Sqrt(MaxPixels / px)
	}
	W, H := max(1, int(math.Ceil(w*s))), max(1, int(math.Ceil(h*s)))
	var base mat
	switch rot {
	case 90:
		W, H = H, W
		base = mat{0, s, s, 0, -b.y0 * s, -b.x0 * s}
	case 180:
		base = mat{-s, 0, 0, s, b.x1 * s, -b.y0 * s}
	case 270:
		W, H = H, W
		base = mat{0, -s, -s, 0, b.y1 * s, b.x1 * s}
	default:
		base = mat{s, 0, 0, -s, -b.x0 * s, b.y1 * s}
	}
	c := image.NewGray(image.Rect(0, 0, W, H))
	for i := range c.Pix {
		c.Pix[i] = 255
	}
	r := &renderer{ctx: ctx, pc: pc, canvas: c, base: base}
	r.st = gstate{ctm: base, fill: paint{alpha: 1}, stroke: paint{alpha: 1}, lineWidth: 1,
		fillCS: deviceGray, strCS: deviceGray}
	return r
}

// stop reports whether the page has to end: a limit or the deadline.
func (r *renderer) stop() bool {
	if r.err != nil {
		return true
	}
	if r.ops > maxOps || r.content > maxContent {
		r.err = ErrTooComplex
	} else if r.ctx.Err() != nil { // every operator: one can be a page-sized fill
		r.err = r.ctx.Err()
	}
	return r.err != nil
}

// walkPage runs the page's content streams.
func (r *renderer) walkPage(page, res types.Dict) error {
	var content []byte
	for _, s := range r.streams(page["Contents"]) {
		content = append(content, s...)
		content = append(content, '\n')
	}
	r.run(content, res, 0)
	return r.err
}

// streams decodes a content stream or an array of them, within the
// page's content budget.
func (r *renderer) streams(o types.Object) [][]byte {
	o, err := r.pc.Dereference(o)
	if err != nil || o == nil {
		return nil
	}
	switch v := o.(type) {
	case types.StreamDict:
		return [][]byte{r.decode(&v)}
	case types.Array:
		var out [][]byte
		for _, e := range v {
			sd, _, err := r.pc.DereferenceStreamDict(e)
			if err == nil && sd != nil {
				out = append(out, r.decode(sd))
			}
		}
		return out
	}
	return nil
}

// run interprets a content stream with resources res.
func (r *renderer) run(content []byte, res types.Dict, depth int) {
	l := &lexer{b: content}
	var ops []operand
	inText := false
	for !r.stop() {
		o, op, done := l.next()
		if done {
			return
		}
		if op == "" {
			if len(ops) < 64 {
				ops = append(ops, o)
			}
			continue
		}
		r.ops++
		num := func(i int) float64 {
			if i < len(ops) && ops[i].kind == 'n' {
				return ops[i].num
			}
			return 0
		}
		nums := func(k int) bool {
			if len(ops) < k {
				return false
			}
			for _, x := range ops[len(ops)-k:] {
				if x.kind != 'n' {
					return false
				}
			}
			ops = ops[len(ops)-k:]
			return true
		}
		switch op {
		case "BT":
			inText = true
		case "ET":
			inText = false
		case "BI":
			l.inlineImage()
			r.inline()
		case "q":
			if len(r.stack) < 1024 {
				r.stack = append(r.stack, r.st)
			}
		case "Q":
			if n := len(r.stack); n > 0 {
				r.st = r.stack[n-1]
				r.stack = r.stack[:n-1]
			}
		case "cm":
			if nums(6) {
				m := mat{num(0), num(1), num(2), num(3), num(4), num(5)}.mul(r.st.ctm)
				if m.finite() {
					r.st.ctm = m
				}
			}
		case "w":
			if nums(1) {
				r.st.lineWidth = math.Abs(num(0))
			}
		case "gs":
			if len(ops) > 0 && ops[len(ops)-1].kind == 'N' {
				r.extGState(res, ops[len(ops)-1].name)
			}
		case "m":
			if nums(2) && !inText {
				r.moveTo(num(0), num(1))
			}
		case "l":
			if nums(2) && !inText {
				r.lineTo(num(0), num(1))
			}
		case "c":
			if nums(6) {
				r.curveTo(num(0), num(1), num(2), num(3), num(4), num(5))
			}
		case "v":
			if nums(4) {
				r.curveTo(r.cur.x, r.cur.y, num(0), num(1), num(2), num(3))
			}
		case "y":
			if nums(4) {
				r.curveTo(num(0), num(1), num(2), num(3), num(2), num(3))
			}
		case "h":
			r.closePath()
		case "re":
			if nums(4) {
				x, y, w, h := num(0), num(1), num(2), num(3)
				r.moveTo(x, y)
				r.lineTo(x+w, y)
				r.lineTo(x+w, y+h)
				r.lineTo(x, y+h)
				r.closePath()
			}
		case "f", "F":
			r.fillPath(false)
		case "f*":
			r.fillPath(true)
		case "S":
			r.strokePath()
		case "s":
			r.closePath()
			r.strokePath()
		case "B", "b":
			if op == "b" {
				r.closePath()
			}
			r.fillPath(false)
			r.strokePath()
		case "B*", "b*":
			if op == "b*" {
				r.closePath()
			}
			r.fillPath(true)
			r.strokePath()
		case "n":
		case "W":
			r.clipOp = 1
		case "W*":
			r.clipOp = 2
		case "g", "G", "rg", "RG", "k", "K":
			r.deviceColor(op, ops)
		case "cs", "CS":
			if len(ops) > 0 && ops[len(ops)-1].kind == 'N' {
				cs := r.colorSpace(res, ops[len(ops)-1].name)
				if op == "cs" {
					r.st.fillCS, r.st.fill = cs, cs.initial(r.st.fill.alpha)
				} else {
					r.st.strCS, r.st.stroke = cs, cs.initial(r.st.stroke.alpha)
				}
			}
		case "sc", "scn":
			r.st.fill = r.st.fillCS.paint(ops, r.st.fill.alpha)
		case "SC", "SCN":
			r.st.stroke = r.st.strCS.paint(ops, r.st.stroke.alpha)
		case "Do":
			if len(ops) > 0 && ops[len(ops)-1].kind == 'N' {
				r.do(res, ops[len(ops)-1].name, depth)
			}
		}
		switch op {
		case "f", "F", "f*", "S", "s", "B", "B*", "b", "b*", "n":
			r.endPath()
		}
		ops = ops[:0]
	}
}

func (r *renderer) dev(x, y float64) pt { return r.st.ctm.apply(x, y) }

func (r *renderer) points() int {
	n := 0
	for _, s := range r.subs {
		n += len(s)
	}
	return n
}

func (r *renderer) moveTo(x, y float64) {
	r.subs = append(r.subs, polygon{r.dev(x, y)})
	r.closed = append(r.closed, false)
	r.cur, r.start = pt{x, y}, pt{x, y}
}

func (r *renderer) lineTo(x, y float64) {
	if len(r.subs) == 0 {
		r.moveTo(x, y)
		return
	}
	if r.points() > maxPathPoints {
		r.err = ErrTooComplex
		return
	}
	i := len(r.subs) - 1
	r.subs[i] = append(r.subs[i], r.dev(x, y))
	r.cur = pt{x, y}
}

func (r *renderer) curveTo(x1, y1, x2, y2, x3, y3 float64) {
	if len(r.subs) == 0 {
		r.moveTo(x1, y1)
	}
	if r.points() > maxPathPoints {
		r.err = ErrTooComplex
		return
	}
	i := len(r.subs) - 1
	p0 := r.dev(r.cur.x, r.cur.y)
	r.subs[i] = cubic(r.subs[i], p0, r.dev(x1, y1), r.dev(x2, y2), r.dev(x3, y3))
	r.cur = pt{x3, y3}
}

func (r *renderer) closePath() {
	if n := len(r.subs); n > 0 {
		r.closed[n-1] = true
		r.cur = r.start
	}
}

// endPath applies a pending clip and starts a new path.
func (r *renderer) endPath() {
	if r.clipOp != 0 {
		m := coverage(r.subs, r.clipOp == 2, r.canvas.Bounds())
		if m == nil {
			m = image.NewAlpha(image.Rectangle{}) // nothing shows through
		}
		r.st.clip = intersect(r.st.clip, m)
		r.clipOp = 0
	}
	r.subs, r.closed = r.subs[:0], r.closed[:0]
}

func (r *renderer) fillPath(evenOdd bool) {
	if r.st.fill.none || len(r.subs) == 0 {
		return
	}
	r.composite(coverage(r.subs, evenOdd, r.bounds()), r.st.fill)
}

func (r *renderer) strokePath() {
	if r.st.stroke.none || len(r.subs) == 0 {
		return
	}
	var polys []polygon
	for i, s := range r.subs {
		polys = append(polys, stroke(s, r.closed[i], r.st.ctm, r.st.lineWidth)...)
	}
	r.composite(coverage(polys, false, r.bounds()), r.st.stroke)
}

// bounds is where painting can show: the canvas, within the clip.
func (r *renderer) bounds() image.Rectangle {
	b := r.canvas.Bounds()
	if r.st.clip != nil {
		b = b.Intersect(r.st.clip.Rect)
	}
	return b
}

// composite paints colour p through mask m and the clip.
func (r *renderer) composite(m *image.Alpha, p paint) {
	if m == nil || p.alpha <= 0 {
		return
	}
	c := r.canvas
	clip := r.st.clip
	g := float64(p.gray)
	for y := m.Rect.Min.Y; y < m.Rect.Max.Y; y++ {
		mo := (y - m.Rect.Min.Y) * m.Stride
		co := y * c.Stride
		for x := m.Rect.Min.X; x < m.Rect.Max.X; x++ {
			a := float64(m.Pix[mo+x-m.Rect.Min.X]) / 255
			if a == 0 {
				continue
			}
			if clip != nil {
				a *= float64(clip.AlphaAt(x, y).A) / 255
			}
			a *= p.alpha
			d := float64(c.Pix[co+x])
			c.Pix[co+x] = uint8(d + (g-d)*a + 0.5)
		}
	}
}

// intersect is the clip of both masks; nil is the whole page.
func intersect(a, b *image.Alpha) *image.Alpha {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	rect := a.Rect.Intersect(b.Rect)
	out := image.NewAlpha(rect)
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			out.Pix[(y-rect.Min.Y)*out.Stride+x-rect.Min.X] = uint8(int(a.AlphaAt(x, y).A) * int(b.AlphaAt(x, y).A) / 255)
		}
	}
	return out
}

func (r *renderer) extGState(res types.Dict, name string) {
	gs := r.dict(r.dict(res, "ExtGState"), name)
	if gs == nil {
		return
	}
	if v, ok := r.number(gs["LW"]); ok {
		r.st.lineWidth = math.Abs(v)
	}
	if v, ok := r.number(gs["ca"]); ok {
		r.st.fill.alpha = math.Max(0, math.Min(1, v))
	}
	if v, ok := r.number(gs["CA"]); ok {
		r.st.stroke.alpha = math.Max(0, math.Min(1, v))
	}
}

func (r *renderer) dict(d types.Dict, key string) types.Dict {
	if d == nil {
		return nil
	}
	v, err := r.pc.DereferenceDict(d[key])
	if err != nil {
		return nil
	}
	return v
}

func (r *renderer) number(o types.Object) (float64, bool) {
	if o == nil {
		return 0, false
	}
	v, err := r.pc.DereferenceNumber(o)
	return v, err == nil
}

// do paints the XObject name: a form, run in place, or an image.
func (r *renderer) do(res types.Dict, name string, depth int) {
	xo := r.dict(res, "XObject")
	if xo == nil {
		return
	}
	sd, _, err := r.pc.DereferenceStreamDict(xo[name])
	if err != nil || sd == nil {
		return
	}
	switch sub := sd.Dict.NameEntry("Subtype"); {
	case sub == nil:
	case *sub == "Form" && depth < maxFormDepth:
		r.form(sd, res, depth)
	case *sub == "Image":
		w, h := sd.Dict.IntEntry("Width"), sd.Dict.IntEntry("Height")
		if w == nil || h == nil {
			return
		}
		if im := r.match(*w, *h, false); im != nil {
			r.drawImage(im, sd, r.st.clip)
		}
	}
}

// inline paints an inline image, the reader's next one.
func (r *renderer) inline() {
	if im := r.match(-1, -1, true); im != nil {
		r.drawImage(im, nil, r.st.clip)
	}
}

// match finds the reader's image for the one the content stream paints
// now: the next of the same size (and kind) within a few, since the
// reader may have left one out.
func (r *renderer) match(w, h int, inline bool) *gpdf.Image {
	for j := r.next; j < len(r.images) && j < r.next+8; j++ {
		im := &r.images[j]
		if im.Inline == inline && (inline || im.Width == w && im.Height == h) {
			r.next = j + 1
			r.matched++
			return im
		}
	}
	return nil
}

func (r *renderer) form(sd *types.StreamDict, res types.Dict, depth int) {
	content := r.decode(sd)
	saved, savedStack := r.st, len(r.stack)
	if m := sd.Dict.ArrayEntry("Matrix"); len(m) == 6 {
		var mm mat
		for i, o := range m {
			mm[i], _ = r.number(o)
		}
		if t := mm.mul(r.st.ctm); t.finite() {
			r.st.ctm = t
		}
	}
	if bb := sd.Dict.ArrayEntry("BBox"); len(bb) == 4 {
		var v [4]float64
		for i, o := range bb {
			v[i], _ = r.number(o)
		}
		subs := []polygon{{r.dev(v[0], v[1]), r.dev(v[2], v[1]), r.dev(v[2], v[3]), r.dev(v[0], v[3])}}
		m := coverage(subs, false, r.canvas.Bounds())
		if m == nil {
			m = image.NewAlpha(image.Rectangle{})
		}
		r.st.clip = intersect(r.st.clip, m)
	}
	fres := r.dict(sd.Dict, "Resources")
	if fres == nil {
		fres = res
	}
	savedSubs := r.subs
	r.subs, r.closed = nil, nil
	r.run(content, fres, depth+1)
	r.subs, r.closed = savedSubs[:0], r.closed[:0]
	r.st = saved
	r.stack = r.stack[:savedStack]
}

// drawImage paints one of the reader's images where it reports it, a
// stencil mask in the fill colour, through the clip.
func (r *renderer) drawImage(im *gpdf.Image, sd *types.StreamDict, clip *image.Alpha) {
	if r.stop() {
		return
	}
	var src image.Image
	if err := guard(func() error {
		var err error
		src, err = im.Decode()
		return err
	}); err != nil || src == nil {
		return
	}
	b := src.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return
	}
	if im.ImageMask {
		if r.st.fill.none {
			return
		}
		src = stencil(src, r.st.fill.gray)
	} else if sd != nil {
		if a := r.softMask(sd, b); a != nil {
			src = withAlpha(src, a)
		}
	}
	q := im.Quad()
	unit := mat{q[1].X - q[0].X, q[1].Y - q[0].Y, q[3].X - q[0].X, q[3].Y - q[0].Y, q[0].X, q[0].Y}
	toUnit := mat{1 / float64(b.Dx()), 0, 0, -1 / float64(b.Dy()), -float64(b.Min.X) / float64(b.Dx()), 1 + float64(b.Min.Y)/float64(b.Dy())}
	m := toUnit.mul(unit).mul(r.base)
	if !m.finite() || math.Abs(m[0]*m[3]-m[1]*m[2]) < 1e-12 {
		return
	}
	opts := &xdraw.Options{}
	if clip != nil {
		opts.DstMask = clip
	}
	xdraw.ApproxBiLinear.Transform(r.canvas, f64.Aff3{m[0], m[2], m[4], m[1], m[3], m[5]}, src, b, xdraw.Over, opts)
}

// stencil is a stencil mask, decoded as black on white, as the fill
// colour where it paints and transparent elsewhere.
func stencil(src image.Image, gray uint8) image.Image {
	b := src.Bounds()
	out := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			g := color.GrayModel.Convert(src.At(x, y)).(color.Gray).Y
			i := (y-b.Min.Y)*out.Stride + 4*(x-b.Min.X)
			out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = gray, gray, gray, 255-g
		}
	}
	return out
}

// softMask decodes an image's /SMask when it is 8-bit grey samples: the
// image's opacity, scaled to its size.
func (r *renderer) softMask(sd *types.StreamDict, b image.Rectangle) *image.Gray {
	sm, _, err := r.pc.DereferenceStreamDict(sd.Dict["SMask"])
	if err != nil || sm == nil {
		return nil
	}
	w, h, bpc := sm.Dict.IntEntry("Width"), sm.Dict.IntEntry("Height"), sm.Dict.IntEntry("BitsPerComponent")
	if w == nil || h == nil || bpc == nil || *bpc != 8 || *w <= 0 || *h <= 0 || int64(*w)*int64(*h) > maxImagePixels {
		return nil
	}
	data := r.decode(sm)
	if len(data) < *w**h {
		return nil
	}
	g := &image.Gray{Pix: data[:*w**h], Stride: *w, Rect: image.Rect(0, 0, *w, *h)}
	if *w == b.Dx() && *h == b.Dy() {
		return g
	}
	out := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	xdraw.NearestNeighbor.Scale(out, out.Bounds(), g, g.Bounds(), xdraw.Src, nil)
	return out
}

// withAlpha is src with opacity a.
func withAlpha(src image.Image, a *image.Gray) image.Image {
	b := src.Bounds()
	out := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
			c.A = a.Pix[(y-b.Min.Y)*a.Stride+x-b.Min.X]
			i := (y-b.Min.Y)*out.Stride + 4*(x-b.Min.X)
			out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = c.R, c.G, c.B, c.A
		}
	}
	return out
}

// findPage is page n's dictionary and the attributes it inherits, found
// by walking the page tree. pdfcpu's own walk recurses without a guard,
// and a page tree that refers to itself overflows the stack, which no
// recover can catch; this one visits each node once and only so deep.
func findPage(pc *model.Context, n int) (types.Dict, *model.InheritedPageAttrs) {
	root, err := pc.Pages()
	if err != nil || root == nil {
		return nil, nil
	}
	seen := map[int]bool{}
	rect := func(o types.Object) *types.Rectangle {
		a, err := pc.DereferenceArray(o)
		if err != nil || len(a) != 4 {
			return nil
		}
		var v [4]float64
		for i, e := range a {
			if v[i], err = pc.DereferenceNumber(e); err != nil {
				return nil
			}
		}
		return types.NewRectangle(v[0], v[1], v[2], v[3])
	}
	count := 0
	var walk func(o types.Object, inh model.InheritedPageAttrs, depth int) (types.Dict, *model.InheritedPageAttrs)
	walk = func(o types.Object, inh model.InheritedPageAttrs, depth int) (types.Dict, *model.InheritedPageAttrs) {
		if depth > 64 {
			return nil, nil
		}
		if ref, ok := o.(types.IndirectRef); ok {
			if seen[ref.ObjectNumber.Value()] {
				return nil, nil
			}
			seen[ref.ObjectNumber.Value()] = true
		}
		d, err := pc.DereferenceDict(o)
		if err != nil || d == nil {
			return nil, nil
		}
		if r, err := pc.DereferenceDict(d["Resources"]); err == nil && r != nil {
			inh.Resources = r
		}
		if r := rect(d["MediaBox"]); r != nil {
			inh.MediaBox = r
		}
		if r := rect(d["CropBox"]); r != nil {
			inh.CropBox = r
		}
		if v, ok := d["Rotate"]; ok {
			if f, err := pc.DereferenceNumber(v); err == nil {
				inh.Rotate = int(f)
			}
		}
		kids, err := pc.DereferenceArray(d["Kids"])
		if err != nil || kids == nil {
			if t := d.NameEntry("Type"); t != nil && *t == "Pages" {
				return nil, nil
			}
			count++
			if count == n {
				return d, &inh
			}
			return nil, nil
		}
		for _, k := range kids {
			if count >= n {
				break
			}
			if pd, pi := walk(k, inh, depth+1); pd != nil {
				return pd, pi
			}
		}
		return nil, nil
	}
	return walk(*root, model.InheritedPageAttrs{}, 0)
}
