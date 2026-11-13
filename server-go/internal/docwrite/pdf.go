package docwrite

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/go-pdf/fpdf"
)

// A US Letter page in points, one inch margins.
const (
	margin   = 72.0
	bodySize = 10.5
	bodyLine = 14.5
)

// fontSet is a TrueType family with all four faces, which draws any
// character the font has; without one the PDF falls back to the standard
// Helvetica and Courier, which draw Western European text only.
type fontSet struct{ regular, bold, italic, boldItalic, mono, monoBold string }

var fontCandidates = []fontSet{
	{"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf", "/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf", "/usr/share/fonts/truetype/dejavu/DejaVuSans-Oblique.ttf", "/usr/share/fonts/truetype/dejavu/DejaVuSans-BoldOblique.ttf", "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf", "/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Bold.ttf"},
	{"/usr/share/fonts/dejavu-sans-fonts/DejaVuSans.ttf", "/usr/share/fonts/dejavu-sans-fonts/DejaVuSans-Bold.ttf", "/usr/share/fonts/dejavu-sans-fonts/DejaVuSans-Oblique.ttf", "/usr/share/fonts/dejavu-sans-fonts/DejaVuSans-BoldOblique.ttf", "/usr/share/fonts/dejavu-sans-mono-fonts/DejaVuSansMono.ttf", "/usr/share/fonts/dejavu-sans-mono-fonts/DejaVuSansMono-Bold.ttf"},
	{"/usr/share/fonts/dejavu/DejaVuSans.ttf", "/usr/share/fonts/dejavu/DejaVuSans-Bold.ttf", "/usr/share/fonts/dejavu/DejaVuSans-Oblique.ttf", "/usr/share/fonts/dejavu/DejaVuSans-BoldOblique.ttf", "/usr/share/fonts/dejavu/DejaVuSansMono.ttf", "/usr/share/fonts/dejavu/DejaVuSansMono-Bold.ttf"},
	{"/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf", "/usr/share/fonts/truetype/liberation/LiberationSans-Bold.ttf", "/usr/share/fonts/truetype/liberation/LiberationSans-Italic.ttf", "/usr/share/fonts/truetype/liberation/LiberationSans-BoldItalic.ttf", "/usr/share/fonts/truetype/liberation/LiberationMono-Regular.ttf", "/usr/share/fonts/truetype/liberation/LiberationMono-Bold.ttf"},
	{"/usr/share/fonts/liberation-sans/LiberationSans-Regular.ttf", "/usr/share/fonts/liberation-sans/LiberationSans-Bold.ttf", "/usr/share/fonts/liberation-sans/LiberationSans-Italic.ttf", "/usr/share/fonts/liberation-sans/LiberationSans-BoldItalic.ttf", "/usr/share/fonts/liberation-mono/LiberationMono-Regular.ttf", "/usr/share/fonts/liberation-mono/LiberationMono-Bold.ttf"},
	{"/usr/share/fonts/liberation/LiberationSans-Regular.ttf", "/usr/share/fonts/liberation/LiberationSans-Bold.ttf", "/usr/share/fonts/liberation/LiberationSans-Italic.ttf", "/usr/share/fonts/liberation/LiberationSans-BoldItalic.ttf", "/usr/share/fonts/liberation/LiberationMono-Regular.ttf", "/usr/share/fonts/liberation/LiberationMono-Bold.ttf"},
	{"/System/Library/Fonts/Supplemental/Arial.ttf", "/System/Library/Fonts/Supplemental/Arial Bold.ttf", "/System/Library/Fonts/Supplemental/Arial Italic.ttf", "/System/Library/Fonts/Supplemental/Arial Bold Italic.ttf", "/System/Library/Fonts/Supplemental/Courier New.ttf", "/System/Library/Fonts/Supplemental/Courier New Bold.ttf"},
	{`C:\Windows\Fonts\arial.ttf`, `C:\Windows\Fonts\arialbd.ttf`, `C:\Windows\Fonts\ariali.ttf`, `C:\Windows\Fonts\arialbi.ttf`, `C:\Windows\Fonts\cour.ttf`, `C:\Windows\Fonts\courbd.ttf`},
}

type loadedFonts struct {
	body, mono [4][]byte // regular, bold, italic, bold italic (mono: regular, bold)
	hasMono    bool
}

var (
	fontsOnce sync.Once
	fonts     *loadedFonts
)

// findFonts loads the first complete family it finds. STUDIO_PDF_FONT_DIR, when set, names a folder holding a
// family as Regular/Bold/Italic/BoldItalic .ttf files, for a host whose
// fonts are elsewhere.
func findFonts() *loadedFonts {
	fontsOnce.Do(func() {
		cands := fontCandidates
		if dir := os.Getenv("STUDIO_PDF_FONT_DIR"); dir != "" {
			if m, _ := filepath.Glob(filepath.Join(dir, "*.ttf")); len(m) > 0 {
				pick := func(want ...string) string {
					for _, f := range m {
						b := strings.ToLower(filepath.Base(f))
						ok := true
						for _, w := range want {
							ok = ok && strings.Contains(b, w)
						}
						if ok {
							return f
						}
					}
					return ""
				}
				reg := pick("regular")
				if reg == "" && len(m) > 0 {
					reg = m[0]
				}
				cands = append([]fontSet{{reg, pick("bold"), pick("italic"), pick("bolditalic"), pick("mono"), pick("mono", "bold")}}, cands...)
			}
		}
		for _, c := range cands {
			read := func(p string) []byte {
				if p == "" {
					return nil
				}
				b, err := os.ReadFile(p)
				if err != nil {
					return nil
				}
				return b
			}
			r := read(c.regular)
			if r == nil {
				continue
			}
			lf := &loadedFonts{}
			lf.body[0] = r
			for i, p := range []string{c.bold, c.italic, c.boldItalic} {
				if lf.body[i+1] = read(p); lf.body[i+1] == nil {
					lf.body[i+1] = r
				}
			}
			if m := read(c.mono); m != nil {
				lf.mono[0], lf.hasMono = m, true
				if lf.mono[1] = read(c.monoBold); lf.mono[1] == nil {
					lf.mono[1] = m
				}
			}
			fonts = lf
			return
		}
	})
	return fonts
}

type pdfw struct {
	p          *fpdf.Fpdf
	body, mono string
	tr         func(string) string
	left       float64 // content's left edge (moves with list indent)
	right      float64 // page's right content edge
	bottom     float64
	color      [3]int
	pics       map[string]*picture
}

var numericRe = regexp.MustCompile(`^[-+]?[$€£]?(\d{1,3}(,\d{3})+|\d+)(\.\d+)?%?$`)

func renderPDF(blocks []Block, pics map[string]*picture) ([]byte, int, error) {
	p := fpdf.New("P", "pt", "Letter", "")
	p.SetMargins(margin, margin, margin)
	p.SetAutoPageBreak(true, margin)
	w := &pdfw{p: p, body: "Helvetica", mono: "Courier", tr: func(s string) string { return s }, pics: pics}
	if f := findFonts(); f != nil {
		for i, st := range []string{"", "B", "I", "BI"} {
			p.AddUTF8FontFromBytes("body", st, f.body[i])
		}
		w.body, w.mono = "body", "body"
		if f.hasMono {
			p.AddUTF8FontFromBytes("mono", "", f.mono[0])
			p.AddUTF8FontFromBytes("mono", "B", f.mono[1])
			w.mono = "mono"
		}
	} else {
		cp := p.UnicodeTranslatorFromDescriptor("")
		w.tr = func(s string) string { return cp(asciiFold(s)) }
	}
	if t := Title(blocks); t != "" {
		p.SetTitle(t, true)
	}
	p.SetCreator("ACTIVATE Studio", true)
	pw, ph := p.GetPageSize()
	w.right = pw - margin
	w.bottom = ph - margin
	w.left = margin
	p.SetFooterFunc(func() {
		p.SetY(ph - margin/2)
		p.SetFont(w.body, "", 8.5)
		p.SetTextColor(120, 120, 120)
		p.CellFormat(0, 10, strconv.Itoa(p.PageNo()), "", 0, "C", false, 0, "")
		p.SetTextColor(w.color[0], w.color[1], w.color[2])
	})
	p.AddPage()
	w.blocks(blocks, 0)
	if err := p.Error(); err != nil {
		return nil, 0, err
	}
	var buf bytes.Buffer
	if err := p.Output(&buf); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), p.PageNo(), nil
}

func (w *pdfw) font(r Run, size float64) {
	st := ""
	if r.Bold {
		st += "B"
	}
	if r.Italic && !r.Code {
		st += "I"
	}
	fam := w.body
	if r.Code {
		fam = w.mono
		if fam == "mono" && strings.Contains(st, "I") {
			st = strings.ReplaceAll(st, "I", "")
		}
		size -= 1
	}
	if r.Link != "" {
		st += "U"
	}
	if r.Strike {
		st += "S"
	}
	w.p.SetFont(fam, st, size)
}

// runs writes inline text, flowing and wrapping between the left edge and
// the right margin.
func (w *pdfw) runs(rs []Run, size, line float64) {
	for _, r := range rs {
		w.font(r, size)
		if r.Link != "" {
			w.p.SetTextColor(20, 70, 160)
			w.p.WriteLinkString(line, w.tr(r.Text), r.Link)
			w.p.SetTextColor(w.color[0], w.color[1], w.color[2])
			continue
		}
		w.p.Write(line, w.tr(r.Text))
	}
}

func (w *pdfw) space(h float64) {
	if w.p.GetY()+h < w.bottom {
		w.p.Ln(h)
	}
}

func (w *pdfw) blocks(bs []Block, depth int) {
	for i, b := range bs {
		switch b.Kind {
		case KHeading:
			size := map[int]float64{1: 19, 2: 15, 3: 12.5}[b.Level]
			if size == 0 {
				size = 11
			}
			if i > 0 {
				w.space(size * 0.8)
			}
			// Keep a heading with at least a few lines of what follows.
			if w.p.GetY()+size*1.4+bodyLine*3 > w.bottom {
				w.p.AddPage()
			}
			w.p.SetX(w.left)
			rs := make([]Run, len(b.Runs))
			for k, r := range b.Runs {
				r.Bold = true
				rs[k] = r
			}
			w.runs(rs, size, size*1.3)
			w.p.Ln(size * 1.3)
			w.space(size * 0.35)
		case KPara:
			w.p.SetX(w.left)
			w.runs(b.Runs, bodySize, bodyLine)
			w.p.Ln(bodyLine)
			w.space(5)
		case KList:
			n := b.Start
			for _, item := range b.Items {
				marker := "•"
				if depth%2 == 1 {
					marker = "◦"
				}
				if b.Ordered {
					marker = strconv.Itoa(n) + "."
					n++
				}
				if w.p.GetY()+bodyLine > w.bottom {
					w.p.AddPage()
				}
				saved := w.left
				w.p.SetFont(w.body, "", bodySize)
				w.p.SetXY(saved+4, w.p.GetY())
				w.p.CellFormat(14, bodyLine, w.tr(marker), "", 0, "R", false, 0, "")
				w.left = saved + 22
				w.p.SetLeftMargin(w.left)
				w.p.SetX(w.left)
				w.blocks(item, depth+1)
				w.left = saved
				w.p.SetLeftMargin(saved)
			}
			w.space(3)
		case KCode:
			w.p.SetFont(w.mono, "", 8.5)
			w.p.SetFillColor(244, 245, 247)
			w.p.SetX(w.left)
			w.p.SetCellMargin(6)
			w.p.MultiCell(w.right-w.left, 11.5, w.tr(b.Text), "", "L", true)
			w.p.SetCellMargin(2)
			w.space(7)
		case KQuote:
			y0 := w.p.GetY()
			saved := w.left
			w.left = saved + 14
			w.p.SetLeftMargin(w.left)
			w.color = [3]int{85, 85, 85}
			w.p.SetTextColor(85, 85, 85)
			w.blocks(b.Children, depth)
			w.color = [3]int{0, 0, 0}
			w.p.SetTextColor(0, 0, 0)
			w.left = saved
			w.p.SetLeftMargin(saved)
			if y1 := w.p.GetY(); y1 > y0 {
				w.p.SetDrawColor(190, 190, 190)
				w.p.SetLineWidth(2)
				w.p.Line(saved+4, y0, saved+4, y1-5)
				w.p.SetLineWidth(0.5)
			}
		case KRule:
			y := w.p.GetY() + 4
			w.p.SetDrawColor(200, 200, 200)
			w.p.SetLineWidth(0.5)
			w.p.Line(w.left, y, w.right, y)
			w.p.Ln(12)
		case KTable:
			w.table(b)
			w.space(8)
		case KImage:
			w.image(b)
		}
	}
}

// image draws a picture at its own size, or narrower to fit between the
// margins and shorter to fit a page, on a new page when it does not fit
// below. One the document could not embed shows its alt text.
func (w *pdfw) image(b Block) {
	pic := w.pics[b.Src]
	if pic == nil {
		if b.Text != "" {
			w.p.SetX(w.left)
			w.runs([]Run{{Text: "[" + b.Text + "]", Italic: true}}, bodySize, bodyLine)
			w.p.Ln(bodyLine)
			w.space(5)
		}
		return
	}
	name := "img:" + b.Src
	opt := fpdf.ImageOptions{ImageType: pic.kind}
	w.p.RegisterImageOptionsReader(name, opt, bytes.NewReader(pic.data))
	iw, ih := pic.size(w.right-w.left, w.bottom-margin)
	if w.p.GetY()+ih > w.bottom {
		w.p.AddPage()
	}
	y := w.p.GetY()
	w.p.ImageOptions(name, w.left, y, iw, ih, false, opt, 0, "")
	w.p.SetXY(w.left, y+ih)
	w.space(8)
}

func (w *pdfw) table(b Block) {
	cols := len(b.Header)
	for _, r := range b.Rows {
		cols = max(cols, len(r))
	}
	if cols == 0 {
		return
	}
	size := bodySize - 1
	if cols > 5 {
		size = bodySize - 2
	}
	line := size * 1.3
	const pad = 4.0
	cell := func(row [][]Run, c int) string {
		if c < len(row) {
			return strings.TrimSpace(Plain(row[c]))
		}
		return ""
	}
	// Widths: each column's natural width when they fit, otherwise each
	// keeps its longest word and the rest is shared by how much more it
	// wants.
	total := w.right - w.left
	natural := make([]float64, cols)
	least := make([]float64, cols)
	all := append([][][]Run{b.Header}, b.Rows...)
	for ri, row := range all {
		st := ""
		if ri == 0 {
			st = "B"
		}
		w.p.SetFont(w.body, st, size)
		for c := 0; c < cols; c++ {
			s := w.tr(cell(row, c))
			for _, ln := range strings.Split(s, "\n") {
				natural[c] = max(natural[c], w.p.GetStringWidth(ln)+2*pad)
			}
			for _, word := range strings.Fields(s) {
				least[c] = max(least[c], min(w.p.GetStringWidth(word)+2*pad, total/2))
			}
			least[c] = max(least[c], 24)
		}
	}
	widths := make([]float64, cols)
	sumN, sumL := 0.0, 0.0
	for c := range natural {
		natural[c] = max(natural[c], least[c])
		sumN += natural[c]
		sumL += least[c]
	}
	switch {
	case sumN <= total:
		for c := range widths {
			widths[c] = natural[c] * total / sumN
		}
	case sumL >= total:
		for c := range widths {
			widths[c] = least[c] * total / sumL
		}
	default:
		extra := total - sumL
		for c := range widths {
			widths[c] = least[c] + extra*(natural[c]-least[c])/(sumN-sumL)
		}
	}
	numeric := make([]bool, cols)
	for c := 0; c < cols; c++ {
		seen := 0
		numeric[c] = true
		for _, r := range b.Rows {
			v := cell(r, c)
			if v == "" {
				continue
			}
			seen++
			if !numericRe.MatchString(v) {
				numeric[c] = false
				break
			}
		}
		numeric[c] = numeric[c] && seen > 0
	}
	align := func(c int) string {
		if c < len(b.Align) {
			switch b.Align[c] {
			case "right":
				return "R"
			case "center":
				return "C"
			case "left":
				return "L"
			}
		}
		if numeric[c] {
			return "R"
		}
		return "L"
	}
	lines := func(row [][]Run, bold bool) ([][]string, float64) {
		st := ""
		if bold {
			st = "B"
		}
		w.p.SetFont(w.body, st, size)
		out := make([][]string, cols)
		h := 0.0
		for c := 0; c < cols; c++ {
			for _, para := range strings.Split(w.tr(cell(row, c)), "\n") {
				if para == "" {
					out[c] = append(out[c], "")
					continue
				}
				out[c] = append(out[c], w.p.SplitText(para, widths[c]-2*pad)...)
			}
			h = max(h, float64(len(out[c]))*line+2*pad)
		}
		return out, h
	}
	draw := func(row [][]Run, head bool) {
		ls, h := lines(row, head)
		if w.p.GetY()+h > w.bottom {
			w.p.AddPage()
			if !head && len(b.Header) > 0 {
				hl, hh := lines(b.Header, true)
				w.cells(hl, widths, hh, true, align, line, pad, size)
				ls, h = lines(row, false)
			}
		}
		w.cells(ls, widths, h, head, align, line, pad, size)
	}
	w.p.SetDrawColor(200, 200, 200)
	w.p.SetLineWidth(0.5)
	if len(b.Header) > 0 {
		draw(b.Header, true)
	}
	for _, r := range b.Rows {
		draw(r, false)
	}
	w.p.SetX(w.left)
	w.p.Ln(2)
}

func (w *pdfw) cells(ls [][]string, widths []float64, h float64, head bool, align func(int) string, line, pad, size float64) {
	x, y := w.left, w.p.GetY()
	st := ""
	if head {
		st = "B"
		w.p.SetFillColor(240, 242, 245)
	}
	for c := range widths {
		style := "D"
		if head {
			style = "DF"
		}
		w.p.Rect(x, y, widths[c], h, style)
		w.p.SetFont(w.body, st, size)
		for i, l := range ls[c] {
			w.p.SetXY(x+pad, y+pad+float64(i)*line)
			w.p.CellFormat(widths[c]-2*pad, line, l, "", 0, align(c), false, 0, "")
		}
		x += widths[c]
	}
	w.p.SetXY(w.left, y+h)
}

// asciiFold maps the punctuation models favour onto what the standard
// PDF fonts can draw, for a host with no TrueType font to embed.
var asciiFold = strings.NewReplacer(
	"≥", ">=", "≤", "<=", "→", "->", "←", "<-", "≈", "~", "≠", "!=", "×", "x", "✓", "[x]", "✔", "[x]", "✗", "[ ]",
	"◦", "-", "▪", "-", "\u00a0", " ",
).Replace
