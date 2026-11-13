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
			lf := &loadedFonts{}
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
			return lf
		}
	}
	return nil
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
	top        float64 // the page's top margin
	size, line float64 // body text size and line height
	justify    bool
}

var numericRe = regexp.MustCompile(`^[-+]?[$€£]?(\d{1,3}(,\d{3})+|\d+)(\.\d+)?%?$`)

func renderPDF(blocks []Block, pics map[string]*picture, o Options) ([]byte, int, error) {
	orient := "P"
	if o.Landscape {
		orient = "L"
	}
	m := o.marginPt()
	p := fpdf.New(orient, "pt", "Letter", "")
	p.SetMargins(m, m, m)
	p.SetAutoPageBreak(true, m)
	w := &pdfw{p: p, body: "Helvetica", mono: "Courier", tr: func(s string) string { return s }, pics: pics,
		top: m, size: o.pdfSize(), justify: o.Justify}
	w.line = w.size * bodyLine / bodySize
	f := findFonts()
	if o.Serif {
		w.body, f = "Times", findSerifFonts()
	}
	if f != nil {
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
	w.right = pw - m
	w.bottom = ph - m
	w.left = m
	p.SetFooterFunc(func() {
		p.SetY(ph - m/2)
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

// flow writes inline text justified: each line is filled with whole
// words (a word wider than the line is broken), and every line of the
// paragraph but its last, and any before a hard break, is stretched to
// the right margin by widening the spaces between words. fpdf justifies
// only a single-style MultiCell, so the words are placed here one by one.
// It leaves the cursor on the paragraph's last line, as runs does.
func (w *pdfw) flow(rs []Run, size, line float64) {
	type word struct {
		r       Run
		text    string
		w, gapW float64 // the word's width, and the space before it
		brk     bool    // a hard break before it
	}
	var words []word
	gap, brk := false, false
	for _, r := range rs {
		w.font(r, size)
		sp := w.p.GetStringWidth(" ")
		for _, part := range strings.SplitAfter(r.Text, "\n") {
			hard := strings.HasSuffix(part, "\n")
			part = strings.TrimSuffix(part, "\n")
			if part != "" && (part[0] == ' ' || part[0] == '\t') {
				gap = true
			}
			fs := strings.Fields(part)
			for i, f := range fs {
				t := w.tr(f)
				words = append(words, word{r: r, text: t, w: w.p.GetStringWidth(t), brk: brk})
				if (i > 0 || gap) && len(words) > 1 && !brk {
					words[len(words)-1].gapW = sp
				}
				gap, brk = false, false
			}
			if len(fs) > 0 && strings.TrimRight(part, " \t") != part {
				gap = true
			}
			if hard {
				brk, gap = true, false
			}
		}
	}
	width := w.right - w.left
	// Break a word wider than the line into pieces that fit.
	var fit []word
	for _, wd := range words {
		if wd.w <= width {
			fit = append(fit, wd)
			continue
		}
		w.font(wd.r, size)
		piece := ""
		for _, ch := range wd.text {
			if piece != "" && w.p.GetStringWidth(piece+string(ch)) > width {
				fit = append(fit, word{r: wd.r, text: piece, w: w.p.GetStringWidth(piece), gapW: wd.gapW, brk: wd.brk})
				wd.gapW, wd.brk, piece = 0, false, ""
			}
			piece += string(ch)
		}
		fit = append(fit, word{r: wd.r, text: piece, w: w.p.GetStringWidth(piece), gapW: wd.gapW, brk: wd.brk})
	}
	y := w.p.GetY()
	first := true
	draw := func(ln []word, stretch bool) {
		if !first {
			y += line
		}
		first = false
		if y+line > w.bottom {
			w.p.AddPage()
			y = w.p.GetY()
		}
		natural := 0.0
		for i, wd := range ln {
			natural += wd.w
			if i > 0 {
				natural += wd.gapW
			}
		}
		extra := 0.0
		gaps := 0
		for i := 1; i < len(ln); i++ {
			if ln[i].gapW > 0 {
				gaps++
			}
		}
		if stretch && gaps > 0 {
			extra = (width - natural) / float64(gaps)
		}
		x := w.left
		for i, wd := range ln {
			if i > 0 && wd.gapW > 0 {
				x += wd.gapW + extra
			}
			w.font(wd.r, size)
			if wd.r.Link != "" {
				w.p.SetTextColor(20, 70, 160)
			}
			w.p.SetXY(x, y)
			w.p.CellFormat(wd.w, line, wd.text, "", 0, "L", false, 0, wd.r.Link)
			if wd.r.Link != "" {
				w.p.SetTextColor(w.color[0], w.color[1], w.color[2])
			}
			x += wd.w
		}
	}
	var cur []word
	used := 0.0
	for _, wd := range fit {
		add := wd.w
		if len(cur) > 0 {
			add += wd.gapW
		}
		if len(cur) > 0 && (wd.brk || used+add > width) {
			draw(cur, !wd.brk)
			cur, used = nil, 0
			wd.gapW = 0
			add = wd.w
		}
		cur = append(cur, wd)
		used += add
	}
	if len(cur) > 0 {
		draw(cur, false)
	}
	w.p.SetXY(w.left, y)
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
			size *= w.size / bodySize
			if i > 0 {
				w.space(size * 0.8)
			}
			// Keep a heading with at least a few lines of what follows.
			if w.p.GetY()+size*1.4+w.line*3 > w.bottom {
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
			for _, item := range b.Items {
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
				w.p.SetFont(w.body, "", w.size)
				w.p.SetXY(saved+4, w.p.GetY())
				w.p.CellFormat(14, w.line, w.tr(marker), "", 0, "R", false, 0, "")
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
			w.runs([]Run{{Text: "[" + b.Text + "]", Italic: true}}, w.size, w.line)
			w.p.Ln(w.line)
			w.space(5)
		}
		return
	}
	name := "img:" + b.Src
	opt := fpdf.ImageOptions{ImageType: pic.kind}
	w.p.RegisterImageOptionsReader(name, opt, bytes.NewReader(pic.data))
	iw, ih := pic.size(w.right-w.left, w.bottom-w.top)
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
	size := w.size - 1
	if cols > 5 {
		size = w.size - 2
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
