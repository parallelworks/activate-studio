package docwrite

import (
	"fmt"
	"sort"
	"strings"
)

// columnWidths shares a table's width among its columns. natural is what
// each column needs to hold its text on one line and least what it needs
// to hold its longest word, both with the cell's padding. The widths
// always add up to total:
//
//   - when every column fits on one line, each gets its natural width and
//     what is left over in proportion to it;
//   - otherwise each keeps its longest word and the rest is shared by how
//     much more each column wants;
//   - when even the longest words do not all fit, the widest columns are
//     cut down to a common width, so that only their words break and no
//     column is lost.
func columnWidths(natural, least []float64, total float64) []float64 {
	n := len(natural)
	widths := make([]float64, n)
	if n == 0 || total <= 0 {
		return widths
	}
	sumN, sumL := 0.0, 0.0
	nat := make([]float64, n)
	for c := range natural {
		nat[c] = max(natural[c], least[c])
		sumN += nat[c]
		sumL += least[c]
	}
	switch {
	case sumN <= total:
		for c := range widths {
			widths[c] = nat[c] * total / sumN
		}
	case sumL <= total:
		extra, want := total-sumL, sumN-sumL
		for c := range widths {
			widths[c] = least[c] + extra*(nat[c]-least[c])/want
		}
	default:
		order := make([]int, n)
		for c := range order {
			order[c] = c
		}
		sort.SliceStable(order, func(i, j int) bool { return least[order[i]] < least[order[j]] })
		left := total
		for k, c := range order {
			share := left / float64(n-k)
			widths[c] = min(least[c], share)
			left -= widths[c]
		}
	}
	return widths
}

// lpiece is a stretch of a word in one style.
type lpiece struct {
	r    Run
	text string
	w    float64
}

// lword is a run of characters with no space in it: what a line may not
// be broken inside. It has several pieces when its style changes part way
// ("**bold**," or a word and its footnote number).
type lword struct {
	pieces []lpiece
	w      float64
	space  float64 // the space before it, when it does not start a line
	brk    int     // hard line breaks before it
}

// words measures inline text at a size as words to lay out.
func (w *pdfw) words(rs []Run, size float64) []lword {
	var out []lword
	open := false // the last word takes what comes next
	space, brk := 0.0, 0
	for _, r := range rs {
		w.font(r, size)
		sp := w.p.GetStringWidth(" ")
		text := w.tr(r.Text)
		for len(text) > 0 {
			switch text[0] {
			case '\n':
				brk++
				space, open = 0, false
				text = text[1:]
				continue
			case ' ', '\t', '\r':
				if brk == 0 {
					space = sp
				}
				open = false
				text = text[1:]
				continue
			}
			n := strings.IndexAny(text, " \t\r\n")
			if n < 0 {
				n = len(text)
			}
			p := lpiece{r: r, text: text[:n], w: w.p.GetStringWidth(text[:n])}
			text = text[n:]
			if open {
				last := &out[len(out)-1]
				last.pieces = append(last.pieces, p)
				last.w += p.w
			} else {
				out = append(out, lword{pieces: []lpiece{p}, w: p.w, space: space, brk: brk})
			}
			open, space, brk = true, 0, 0
		}
	}
	return out
}

// slack absorbs the rounding in a sum of character widths, so that a
// word set in a space of exactly its own width is not taken as too wide.
const slack = 0.01

// split breaks a word wider than width into pieces that fit, a
// character at a time.
func (w *pdfw) split(wd lword, size, width float64) []lword {
	if wd.w <= width+slack {
		return []lword{wd}
	}
	var out []lword
	cur := lword{space: wd.space, brk: wd.brk}
	for _, p := range wd.pieces {
		w.font(p.r, size)
		part := ""
		flush := func() {
			if part != "" {
				pw := w.p.GetStringWidth(part)
				cur.pieces = append(cur.pieces, lpiece{r: p.r, text: part, w: pw})
				cur.w += pw
				part = ""
			}
		}
		for _, ch := range p.text {
			if cur.w+w.p.GetStringWidth(part+string(ch)) > width+slack && (part != "" || len(cur.pieces) > 0) {
				flush()
				out = append(out, cur)
				cur = lword{}
			}
			part += string(ch)
		}
		flush()
	}
	if len(cur.pieces) > 0 {
		out = append(out, cur)
	}
	return out
}

// wrap sets words in lines no wider than width. A hard break starts a
// new line, and an empty one for each break after the first.
func (w *pdfw) wrap(words []lword, size, width float64) [][]lword {
	var lines [][]lword
	var cur []lword
	used := 0.0
	for i, whole := range words {
		for k, wd := range w.split(whole, size, width) {
			add := wd.w
			if len(cur) > 0 {
				add += wd.space
			}
			hard := k == 0 && wd.brk > 0 && i > 0
			if hard || len(cur) > 0 && (k > 0 || used+add > width+slack) {
				lines = append(lines, cur)
				for b := 1; hard && b < wd.brk; b++ {
					lines = append(lines, nil)
				}
				cur, used, add = nil, 0, wd.w
			}
			cur = append(cur, wd)
			used += add
		}
	}
	if len(cur) > 0 || len(lines) == 0 {
		lines = append(lines, cur)
	}
	return lines
}

func lineWidth(ln []lword) float64 {
	t := 0.0
	for i, wd := range ln {
		t += wd.w
		if i > 0 {
			t += wd.space
		}
	}
	return t
}

// extent is the width of words set without wrapping (their longest line
// between hard breaks) and the width of their longest word.
func extent(words []lword) (natural, least float64) {
	line := 0.0
	for i, wd := range words {
		if wd.brk > 0 || i == 0 {
			line = wd.w
		} else {
			line += wd.space + wd.w
		}
		natural = max(natural, line)
		least = max(least, wd.w)
	}
	return natural, least
}

// drawLine draws a line of words from x, in a box width wide, aligned
// "L", "R" or "C".
func (w *pdfw) drawLine(ln []lword, x, y, width, size, line float64, align string) {
	switch lw := lineWidth(ln); align {
	case "R":
		x += width - lw
	case "C":
		x += (width - lw) / 2
	}
	for i, wd := range ln {
		if i > 0 {
			x += wd.space
		}
		for _, p := range wd.pieces {
			w.font(p.r, size)
			if p.r.Link != "" {
				w.p.SetTextColor(20, 70, 160)
			}
			w.p.SetXY(x, y-w.rise(p.r, size))
			w.p.CellFormat(p.w, line, p.text, "", 0, "L", false, 0, p.r.Link)
			if p.r.Link != "" {
				w.p.SetTextColor(w.color[0], w.color[1], w.color[2])
			}
			x += p.w
		}
	}
}

// tableLayout is a table's text size, cell padding and column widths
// for a text column total wide. The size steps down from base while the
// columns cannot all hold their longest words, as far as floor; at floor
// the padding at each side of a cell goes from pad to tight. grow is a
// factor on the measured widths, for a font that may be wider than the
// one measured with. fits is false when words had to be broken all the
// same.
func (w *pdfw) tableLayout(b Block, cols int, base, floor, total, pad, tight, grow float64) (size, padding float64, widths []float64, fits bool) {
	size, padding = base, pad
	for {
		natural := make([]float64, cols)
		least := make([]float64, cols)
		rows := append([][][]Run{b.Header}, b.Rows...)
		for ri, row := range rows {
			for c := 0; c < cols && c < len(row); c++ {
				n, l := extent(w.words(cellRuns(row[c], ri == 0), size))
				natural[c] = max(natural[c], n*grow+2*padding+2*slack)
				least[c] = max(least[c], l*grow+2*padding+2*slack)
			}
		}
		// A word is given at most half the table, and a column a little
		// room at least, unless the table is too crowded for that.
		roomy := make([]float64, cols)
		sumL, sumR := 0.0, 0.0
		for c := range least {
			least[c] = max(min(least[c], total/2), 2*padding+size/2)
			roomy[c] = max(least[c], 24)
			sumL += least[c]
			sumR += roomy[c]
		}
		if sumR <= total {
			least, sumL = roomy, sumR
		}
		switch {
		case sumL <= total:
			return size, padding, columnWidths(natural, least, total), true
		case size-0.5 >= floor:
			size -= 0.5
		case padding > tight:
			padding = tight
		default:
			return size, padding, columnWidths(natural, least, total), false
		}
	}
}

// crowded says a table's words were broken to fit it on the page.
func crowded(cols int) string {
	return fmt.Sprintf("a table of %d columns is wider than the page holds, and long words in it were broken; set orientation: landscape, or give the table fewer columns", cols)
}

// cellRuns is a table cell's text, trimmed, and bold in a header row.
func cellRuns(rs []Run, head bool) []Run {
	out := make([]Run, 0, len(rs))
	for _, r := range rs {
		r.Bold = r.Bold || head
		out = append(out, r)
	}
	for len(out) > 0 && strings.TrimSpace(out[0].Text) == "" {
		out = out[1:]
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1].Text) == "" {
		out = out[:len(out)-1]
	}
	return out
}
