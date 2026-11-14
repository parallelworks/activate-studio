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

// serifCandidates are the families for a document set in a serif face:
// Times New Roman, or Liberation Serif, its metric twin.
var serifCandidates = []fontSet{
	{"/usr/share/fonts/truetype/liberation/LiberationSerif-Regular.ttf", "/usr/share/fonts/truetype/liberation/LiberationSerif-Bold.ttf", "/usr/share/fonts/truetype/liberation/LiberationSerif-Italic.ttf", "/usr/share/fonts/truetype/liberation/LiberationSerif-BoldItalic.ttf", "/usr/share/fonts/truetype/liberation/LiberationMono-Regular.ttf", "/usr/share/fonts/truetype/liberation/LiberationMono-Bold.ttf"},
	{"/usr/share/fonts/liberation-serif/LiberationSerif-Regular.ttf", "/usr/share/fonts/liberation-serif/LiberationSerif-Bold.ttf", "/usr/share/fonts/liberation-serif/LiberationSerif-Italic.ttf", "/usr/share/fonts/liberation-serif/LiberationSerif-BoldItalic.ttf", "/usr/share/fonts/liberation-mono/LiberationMono-Regular.ttf", "/usr/share/fonts/liberation-mono/LiberationMono-Bold.ttf"},
	{"/usr/share/fonts/liberation/LiberationSerif-Regular.ttf", "/usr/share/fonts/liberation/LiberationSerif-Bold.ttf", "/usr/share/fonts/liberation/LiberationSerif-Italic.ttf", "/usr/share/fonts/liberation/LiberationSerif-BoldItalic.ttf", "/usr/share/fonts/liberation/LiberationMono-Regular.ttf", "/usr/share/fonts/liberation/LiberationMono-Bold.ttf"},
	{"/System/Library/Fonts/Supplemental/Times New Roman.ttf", "/System/Library/Fonts/Supplemental/Times New Roman Bold.ttf", "/System/Library/Fonts/Supplemental/Times New Roman Italic.ttf", "/System/Library/Fonts/Supplemental/Times New Roman Bold Italic.ttf", "/System/Library/Fonts/Supplemental/Courier New.ttf", "/System/Library/Fonts/Supplemental/Courier New Bold.ttf"},
	{`C:\Windows\Fonts\times.ttf`, `C:\Windows\Fonts\timesbd.ttf`, `C:\Windows\Fonts\timesi.ttf`, `C:\Windows\Fonts\timesbi.ttf`, `C:\Windows\Fonts\cour.ttf`, `C:\Windows\Fonts\courbd.ttf`},
	{"/usr/share/fonts/truetype/dejavu/DejaVuSerif.ttf", "/usr/share/fonts/truetype/dejavu/DejaVuSerif-Bold.ttf", "/usr/share/fonts/truetype/dejavu/DejaVuSerif-Italic.ttf", "/usr/share/fonts/truetype/dejavu/DejaVuSerif-BoldItalic.ttf", "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf", "/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Bold.ttf"},
}

type loadedFonts struct {
	body, mono [4][]byte // regular, bold, italic, bold italic (mono: regular, bold)
	hasMono    bool
	// What each face can draw, as body and mono are ordered.
	cover, monoCover [4]coverage
	// How wide the body face sets text, against Arial as 1.
	wide float64
}

// faceWidth is how wide a family sets text against Arial, from the name
// of its file: the Word writer measures with the PDF's font and scales
// to the font the document names.
func faceWidth(path string) float64 {
	l := strings.ToLower(filepath.Base(path))
	switch {
	case strings.Contains(l, "dejavuserif"):
		return 1.17
	case strings.Contains(l, "dejavu"):
		return 1.14
	case strings.Contains(l, "serif") || strings.Contains(l, "times"):
		return 0.9
	}
	return 1
}

var (
	fontsOnce, serifOnce sync.Once
	fonts, serifFonts    *loadedFonts
)

// findSerifFonts is findFonts for a document set in a serif face; nil
// when the host has no serif family, and the PDF uses Times.
func findSerifFonts() *loadedFonts {
	serifOnce.Do(func() { serifFonts = loadFamily(serifCandidates) })
	return serifFonts
}

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
		fonts = loadFamily(cands)
	})
	return fonts
}

// loadFamily reads the first family of cands found on this host.
func loadFamily(cands []fontSet) *loadedFonts {
	// A family with all four faces first: a host may hold a family's
	// regular and bold only (Debian's fonts-dejavu-core), and italic
	// drawn from the regular face comes out upright.
	for _, complete := range []bool{true, false} {
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
			lf := &loadedFonts{wide: faceWidth(c.regular)}
			lf.body[0] = r
			whole := true
			for i, p := range []string{c.bold, c.italic, c.boldItalic} {
				if lf.body[i+1] = read(p); lf.body[i+1] == nil {
					lf.body[i+1] = r
					whole = false
				}
			}
			if complete && !whole {
				continue
			}
			if m := read(c.mono); m != nil {
				lf.mono[0], lf.hasMono = m, true
				if lf.mono[1] = read(c.monoBold); lf.mono[1] == nil {
					lf.mono[1] = m
				}
			}
			for i := range lf.body {
				lf.cover[i] = faceCoverage(lf.body[i])
			}
			if lf.hasMono {
				lf.monoCover[0], lf.monoCover[1] = faceCoverage(lf.mono[0]), faceCoverage(lf.mono[1])
			}
			return lf
		}
	}
	return nil
}

type pdfw struct {
	p          *fpdf.Fpdf
	o          Options
	body, mono string
	lf         *loadedFonts        // the embedded family; nil with the standard fonts
	cp         func(string) string // the standard fonts' encoding
	cov        coverage            // what the font now set can draw
	g          glyphs              // characters fitted to the font
	left       float64             // content's left edge (moves with list indent)
	right      float64             // page's right content edge
	bottom     float64
	color      [3]int
	pics       map[string]*picture
	top        float64 // the page's top margin
	size, line float64 // body text size and line height
	justify    bool
	cm         float64 // the library's cell margin, restored after a block that changes it
	wide       float64 // how wide the body font sets text, against Arial as 1
	problems   []string
}

var numericRe = regexp.MustCompile(`^[-+]?[$€£]?(\d{1,3}(,\d{3})+|\d+)(\.\d+)?%?$`)

// newPDFWriter is a PDF with its fonts loaded and no page yet. The Word
// writer makes one too, to measure text with.
func newPDFWriter(pics map[string]*picture, o Options) *pdfw {
	orient := "P"
	if o.Landscape {
		orient = "L"
	}
	m := o.marginPt()
	p := fpdf.New(orient, "pt", "Letter", "")
	p.SetMargins(m, m, m)
	p.SetAutoPageBreak(true, m)
	w := &pdfw{p: p, o: o, body: "Helvetica", mono: "Courier", pics: pics, top: m, size: o.pdfSize(), justify: o.Justify}
	w.line = o.pdfLine(w.size)
	w.cm = p.GetCellMargin()
	w.wide = 1
	f := findFonts()
	if o.Serif {
		w.body, f, w.wide = "Times", findSerifFonts(), 0.9
	}
	if f != nil {
		for i, st := range []string{"", "B", "I", "BI"} {
			p.AddUTF8FontFromBytes("body", st, f.body[i])
		}
		w.body, w.mono, w.lf, w.wide = "body", "body", f, f.wide
		if f.hasMono {
			p.AddUTF8FontFromBytes("mono", "", f.mono[0])
			p.AddUTF8FontFromBytes("mono", "B", f.mono[1])
			w.mono = "mono"
		}
	} else {
		w.cp = p.UnicodeTranslatorFromDescriptor("")
	}
	pw, ph := p.GetPageSize()
	w.right = pw - m
	w.bottom = ph - m
	w.left = m
	return w
}

// setFont sets the font, and with it what tr can draw.
func (w *pdfw) setFont(fam, st string, size float64) {
	w.p.SetFont(fam, st, size)
	bold := strings.Contains(st, "B")
	switch {
	case w.lf == nil:
		w.cov = standardCoverage
	case fam == "mono":
		w.cov = w.lf.monoCover[0]
		if bold {
			w.cov = w.lf.monoCover[1]
		}
	default:
		i := 0
		if bold {
			i = 1
		}
		if strings.Contains(st, "I") {
			i += 2
		}
		w.cov = w.lf.cover[i]
	}
}

// tr makes text drawable in the font now set: a character the font lacks
// becomes an equivalent it has, or a placeholder that is reported.
func (w *pdfw) tr(s string) string {
	s = w.g.fit(s, w.cov)
	if w.cp != nil {
		s = w.cp(s)
	}
	return s
}

// running draws a page's header or footer line, centered between the
// page's margins at y, cut short when it is wider than the page holds.
func (w *pdfw) running(text string, y float64) {
	m := w.top
	pw, _ := w.p.GetPageSize()
	cov := w.cov
	w.setFont(w.body, "", 8.5)
	t := w.tr(text)
	if width := pw - 2*m - 2*w.p.GetCellMargin(); w.p.GetStringWidth(t) > width {
		rs := []rune(text)
		for len(rs) > 1 && w.p.GetStringWidth(w.tr(string(rs)+"...")) > width {
			rs = rs[:len(rs)-1]
		}
		t = w.tr(strings.TrimSpace(string(rs)) + "...")
	}
	w.p.SetTextColor(120, 120, 120)
	w.p.SetXY(m, y)
	w.p.CellFormat(pw-2*m, 10, t, "", 0, "C", false, 0, "")
	w.p.SetTextColor(w.color[0], w.color[1], w.color[2])
	w.cov = cov
}

func renderPDF(blocks []Block, pics map[string]*picture, o Options) ([]byte, int, []string, error) {
	w := newPDFWriter(pics, o)
	p, m := w.p, w.top
	if t := Title(blocks); t != "" {
		p.SetTitle(t, true)
	}
	p.SetCreator("ACTIVATE Studio", true)
	_, ph := p.GetPageSize()
	if o.Header != "" {
		p.SetHeaderFunc(func() {
			// The page's text starts where it would without a header.
			x, y := p.GetXY()
			w.running(o.Header, m/2-5)
			p.SetXY(x, y)
		})
	}
	p.SetFooterFunc(func() {
		// The footer's text sits on the line above the page's number.
		if o.Footer != "" {
			w.running(o.Footer, ph-m/2-11)
		}
		w.running(strconv.Itoa(p.PageNo()), ph-m/2)
	})
	p.AddPage()
	w.blocks(blocks, 0)
	if err := p.Error(); err != nil {
		return nil, 0, nil, err
	}
	var buf bytes.Buffer
	if err := p.Output(&buf); err != nil {
		return nil, 0, nil, err
	}
	problems := w.problems
	if pr := w.g.problem(); pr != "" {
		problems = append(problems, pr)
	}
	return buf.Bytes(), p.PageNo(), problems, nil
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
	if r.Sup {
		size *= supScale
	}
	if r.Link != "" {
		st += "U"
	}
	if r.Strike {
		st += "S"
	}
	w.setFont(fam, st, size)
}

// A footnote's number is set at supScale of the text's size, raised.
const supScale = 0.62

// rise is how far above the line a run is drawn.
func (w *pdfw) rise(r Run, size float64) float64 {
	if r.Sup {
		return size * 0.3
	}
	return 0
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
		if up := w.rise(r, size); up > 0 {
			w.p.SetXY(w.p.GetX(), w.p.GetY()-up)
			w.p.Write(line, w.tr(r.Text))
			w.p.SetXY(w.p.GetX(), w.p.GetY()+up)
			continue
		}
		w.p.Write(line, w.tr(r.Text))
	}
}

// flow writes inline text justified: each line is filled with whole
// words (a word wider than the line is broken), and every line of the
// paragraph but its last, and any before a hard break, is stretched to
// the right margin by widening the spaces between words. fpdf justifies
// only a single-style MultiCell, so the words are placed here one by one.
// It leaves the cursor on the paragraph's last line, as runs does.
func (w *pdfw) flow(rs []Run, size, line float64) {
	// Between the same edges as unjustified text, which the library sets
	// a cell margin inside the page margins.
	width := w.right - w.left - 2*w.p.GetCellMargin()
	lines := w.wrap(w.words(rs, size), size, width)
	y := w.p.GetY()
	for i, ln := range lines {
		if i > 0 {
			y += line
		}
		if y+line > w.bottom {
			w.p.AddPage()
			y = w.p.GetY()
		}
		// Stretched unless the paragraph or a hard break ends the line.
		if i+1 < len(lines) && len(lines[i+1]) > 0 && lines[i+1][0].brk == 0 {
			gaps := 0
			for k := 1; k < len(ln); k++ {
				if ln[k].space > 0 {
					gaps++
				}
			}
			if gaps > 0 {
				extra := (width - lineWidth(ln)) / float64(gaps)
				ln = append([]lword(nil), ln...)
				for k := 1; k < len(ln); k++ {
					if ln[k].space > 0 {
						ln[k].space += extra
					}
				}
			}
		}
		w.drawLine(ln, w.left, y, width, size, line, "L")
	}
	w.p.SetXY(w.left, y)
}

func (w *pdfw) space(h float64) {
	if w.p.GetY()+h < w.bottom {
		w.p.Ln(h)
	}
}

// ensure starts a new page when h does not fit below, unless the page is
// empty or h is more than a page holds.
func (w *pdfw) ensure(h float64) {
	if y := w.p.GetY(); y+h > w.bottom && y > w.top+1 && h <= w.bottom-w.top {
		w.p.AddPage()
	}
}

func (w *pdfw) headingSize(level int) float64 {
	size := map[int]float64{1: 19, 2: 15, 3: 12.5}[level]
	if size == 0 {
		size = 11
	}
	return size * w.size / bodySize
}

// textLines is how many lines inline text takes between the margins.
func (w *pdfw) textLines(rs []Run, size float64) int {
	return len(w.wrap(w.words(rs, size), size, w.right-w.left-2*w.p.GetCellMargin()))
}

var captionRe = regexp.MustCompile(`(?i)^(figure|fig\.|table|chart|exhibit|diagram|image|photo|source|note)\b`)

// isCaption reports a short paragraph that labels a figure or a table:
// one that opens "Figure 1", "Table 2", "Source:" and the like, or is set
// wholly in italic.
func isCaption(b Block) bool {
	if b.Kind != KPara || len(b.Runs) == 0 {
		return false
	}
	t := strings.TrimSpace(Plain(b.Runs))
	if t == "" || len(t) > 300 {
		return false
	}
	if captionRe.MatchString(t) {
		return true
	}
	for _, r := range b.Runs {
		if !r.Italic && strings.TrimSpace(r.Text) != "" {
			return false
		}
	}
	return true
}

// room is the height the start of bs needs to stay in one piece: a
// heading with what it heads, a figure with its caption, a caption with
// its table's first rows, a few lines of anything else.
func (w *pdfw) room(bs []Block) float64 {
	if len(bs) == 0 {
		return 0
	}
	switch b := bs[0]; b.Kind {
	case KHeading:
		size := w.headingSize(b.Level)
		return size*0.8 + float64(w.textLines(b.Runs, size))*size*1.3 + size*0.35 + w.room(bs[1:])
	case KPara:
		n := w.textLines(b.Runs, w.size)
		if isCaption(b) && len(bs) > 1 && bs[1].Kind == KTable {
			return float64(n)*w.line + 5 + w.room(bs[1:])
		}
		return float64(min(n, 3)) * w.line
	case KTable:
		return w.layTable(b).start()
	case KImage:
		_, ih, capH := w.imageSize(bs)
		return ih + capH
	case KCode:
		return 11.5 * float64(min(strings.Count(b.Text, "\n")+1, 3))
	case KList:
		if len(b.Items) > 0 {
			return max(w.line, w.room(b.Items[0]))
		}
	case KQuote:
		return w.room(b.Children)
	}
	return w.line
}

func (w *pdfw) blocks(bs []Block, depth int) {
	for i, b := range bs {
		switch b.Kind {
		case KHeading:
			size := w.headingSize(b.Level)
			if i > 0 {
				w.space(size * 0.8)
			}
			// Keep a heading with the start of what follows it, or with a
			// few lines when that is more than a page holds.
			need := w.room(bs[i:]) - size*0.8
			if need > w.bottom-w.top {
				need = size*1.4 + w.line*3
			}
			w.ensure(need)
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
			// Keep a table's caption with the table's first rows.
			if isCaption(b) && i+1 < len(bs) && bs[i+1].Kind == KTable {
				w.ensure(w.room(bs[i:]))
			}
			w.p.SetX(w.left)
			if w.justify {
				w.flow(b.Runs, w.size, w.line)
			} else {
				w.runs(b.Runs, w.size, w.line)
			}
			w.p.Ln(w.line)
			w.space(5)
		case KBreak:
			if w.p.GetY() > w.top+1 {
				w.p.AddPage()
			}
		case KList:
			n := b.Start
			for k, item := range b.Items {
				marker := "•"
				if depth%2 == 1 {
					marker = "◦"
				}
				if b.Ordered {
					marker = strconv.Itoa(n) + "."
					n++
				}
				if w.p.GetY()+w.line > w.bottom {
					w.p.AddPage()
				}
				saved := w.left
				if k < len(b.Checks) && b.Checks[k] != 0 {
					w.checkbox(saved+18, w.p.GetY(), b.Checks[k] == Checked)
				} else {
					w.setFont(w.body, "", w.size)
					w.p.SetXY(saved+4, w.p.GetY())
					w.p.CellFormat(14, w.line, w.tr(marker), "", 0, "R", false, 0, "")
				}
				w.left = saved + 22
				w.p.SetLeftMargin(w.left)
				w.p.SetX(w.left)
				w.blocks(item, depth+1)
				w.left = saved
				w.p.SetLeftMargin(saved)
			}
			w.space(3)
		case KCode:
			w.setFont(w.mono, "", 8.5)
			w.p.SetFillColor(244, 245, 247)
			w.p.SetX(w.left)
			w.p.SetCellMargin(6)
			w.p.MultiCell(w.right-w.left, 11.5, w.tr(b.Text), "", "L", true)
			w.p.SetCellMargin(w.cm)
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
			w.image(bs[i:])
		}
	}
}

// checkbox draws a task's box on the line at y, its right edge at right:
// empty, or with a check mark.
func (w *pdfw) checkbox(right, y float64, checked bool) {
	side := w.size * 0.72
	x, top := right-side-1.5, y+(w.line-side)/2
	w.p.SetDrawColor(70, 70, 70)
	w.p.SetLineWidth(0.7)
	w.p.Rect(x, top, side, side, "D")
	if checked {
		w.p.SetLineWidth(1.1)
		w.p.SetLineCapStyle("round")
		w.p.Line(x+side*0.2, top+side*0.52, x+side*0.42, top+side*0.76)
		w.p.Line(x+side*0.42, top+side*0.76, x+side*0.82, top+side*0.22)
		w.p.SetLineCapStyle("butt")
	}
	w.p.SetLineWidth(0.5)
}

// imageSize is the size the picture at the head of bs is drawn at, and
// the height of the caption under it, which the picture leaves room for
// on its page. A picture the document could not embed has no width.
func (w *pdfw) imageSize(bs []Block) (iw, ih, capH float64) {
	if len(bs) > 1 && isCaption(bs[1]) {
		capH = 8 + float64(w.textLines(bs[1].Runs, w.size))*w.line
	}
	pic := w.pics[bs[0].Src]
	if pic == nil {
		return 0, w.line, capH
	}
	page := w.bottom - w.top
	if capH > page/2 {
		capH = 0
	}
	iw, ih = pic.size(w.right-w.left, page-capH)
	return iw, ih, capH
}

// image draws the picture at the head of bs at its own size, or narrower
// to fit between the margins and shorter to fit a page, on a new page
// when there is too little room below for it and its caption. One the document could
// not embed shows its alt text.
func (w *pdfw) image(bs []Block) {
	b := bs[0]
	pic := w.pics[b.Src]
	if pic == nil {
		if b.Text != "" {
			w.p.SetX(w.left)
			w.runs([]Run{{Text: "[" + b.Text + "]", Italic: true}}, w.size, w.line)
			w.p.Ln(w.line)
			w.space(5)
		}
		return
	}
	name := "img:" + b.Src
	opt := fpdf.ImageOptions{ImageType: pic.kind}
	w.p.RegisterImageOptionsReader(name, opt, bytes.NewReader(pic.data))
	iw, ih, capH := w.imageSize(bs)
	if below := w.bottom - w.p.GetY() - capH; ih > below+slack {
		// With most of the page still free the picture is drawn smaller
		// to fit it, and what leads up to it is not left on a page of its
		// own; otherwise it starts a new page.
		if page := w.bottom - w.top; below >= 0.6*page && w.p.GetY() > w.top+1 {
			iw, ih = pic.size(w.right-w.left, below)
		} else {
			w.p.AddPage()
		}
	}
	y := w.p.GetY()
	w.p.ImageOptions(name, w.left, y, iw, ih, false, opt, 0, "")
	w.p.SetXY(w.left, y+ih)
	w.space(8)
}

// tableLay is a table measured for the page: its text size, its column
// widths, and each row's cells wrapped into lines.
type tableLay struct {
	cols       int
	size, line float64
	widths     []float64
	pad        float64 // the padding at each side of a cell
	fits       bool    // no word had to be broken for want of room
	align      []string
	head       [][][]lword   // the header row's cells; nil without one
	rows       [][][][]lword // each body row's cells
}

// A table cell's padding: at its sides (less in a table too crowded for
// it), and above and below.
const (
	cellPad   = 5.5
	cellTight = 3.0
	cellVPad  = 4.0
)

func (t *tableLay) height(cells [][][]lword) float64 {
	n := 1
	for _, c := range cells {
		n = max(n, len(c))
	}
	return float64(n)*t.line + 2*cellVPad
}

// start is the height of the header row and the first row under it,
// which stay on one page.
func (t *tableLay) start() float64 {
	h := 0.0
	if t.head != nil {
		h = t.height(t.head)
	}
	if len(t.rows) > 0 {
		h += t.height(t.rows[0])
	}
	return h
}

func (w *pdfw) layTable(b Block) *tableLay {
	cols := len(b.Header)
	for _, r := range b.Rows {
		cols = max(cols, len(r))
	}
	t := &tableLay{cols: cols}
	if cols == 0 {
		return t
	}
	base := w.size - 1
	if cols > 5 {
		base = w.size - 2
	}
	// Widths: each column's natural width when they fit, otherwise each
	// keeps its longest word and the rest is shared by how much more it
	// wants.
	t.size, t.pad, t.widths, t.fits = w.tableLayout(b, cols, base, min(base, 7), w.right-w.left, cellPad, cellTight, 1)
	t.line = t.size * 1.3
	cell := func(row [][]Run, c int) string {
		if c < len(row) {
			return strings.TrimSpace(Plain(row[c]))
		}
		return ""
	}
	t.align = make([]string, cols)
	for c := 0; c < cols; c++ {
		seen := 0
		numeric := true
		for _, r := range b.Rows {
			v := cell(r, c)
			if v == "" {
				continue
			}
			seen++
			if !numericRe.MatchString(v) {
				numeric = false
				break
			}
		}
		t.align[c] = "L"
		if numeric && seen > 0 {
			t.align[c] = "R"
		}
		if c < len(b.Align) {
			switch b.Align[c] {
			case "right":
				t.align[c] = "R"
			case "center":
				t.align[c] = "C"
			case "left":
				t.align[c] = "L"
			}
		}
	}
	lay := func(row [][]Run, head bool) [][][]lword {
		out := make([][][]lword, cols)
		for c := 0; c < cols; c++ {
			var rs []Run
			if c < len(row) {
				rs = cellRuns(row[c], head)
			}
			out[c] = w.wrap(w.words(rs, t.size), t.size, t.widths[c]-2*t.pad)
		}
		return out
	}
	if len(b.Header) > 0 {
		t.head = lay(b.Header, true)
	}
	for _, r := range b.Rows {
		t.rows = append(t.rows, lay(r, false))
	}
	return t
}

func (w *pdfw) table(b Block) {
	t := w.layTable(b)
	if t.cols == 0 {
		return
	}
	if !t.fits {
		w.problems = append(w.problems, crowded(t.cols))
	}
	// Words are placed at their measured widths, with no cell margin of
	// the library's inside the cell's own padding.
	w.p.SetCellMargin(0)
	// The header row is not left alone at the foot of a page.
	w.ensure(t.start())
	if t.head != nil {
		w.cells(t, t.head, true)
	}
	for _, r := range t.rows {
		if h := t.height(r); w.p.GetY()+h > w.bottom+slack && w.p.GetY() > w.top+1 {
			w.p.AddPage()
			if t.head != nil {
				w.cells(t, t.head, true)
			}
		}
		w.cells(t, r, false)
	}
	w.p.SetCellMargin(w.cm)
	w.p.SetX(w.left)
	w.p.Ln(2)
}

func (w *pdfw) cells(t *tableLay, cells [][][]lword, head bool) {
	x, y := w.left, w.p.GetY()
	h := t.height(cells)
	w.p.SetDrawColor(200, 200, 200)
	w.p.SetLineWidth(0.5)
	style := "D"
	if head {
		style = "DF"
		w.p.SetFillColor(240, 242, 245)
	}
	for c, width := range t.widths {
		w.p.Rect(x, y, width, h, style)
		for i, ln := range cells[c] {
			w.drawLine(ln, x+t.pad, y+cellVPad+float64(i)*t.line, width-2*t.pad, t.size, t.line, t.align[c])
		}
		x += width
	}
	w.p.SetXY(w.left, y+h)
}
