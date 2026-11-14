package docwrite

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"

	gpdf "github.com/giraffesyo/pdf"
)

func docxParts(t *testing.T, md string) (map[string]string, *Rendered) {
	t.Helper()
	r, err := Render(".docx", md, nil)
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(r.Data), int64(len(r.Data)))
	if err != nil {
		t.Fatal(err)
	}
	parts := map[string]string{}
	for _, f := range z.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		d := xml.NewDecoder(bytes.NewReader(b))
		for {
			if _, err := d.Token(); err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
		}
		parts[f.Name] = string(b)
	}
	return parts, r
}

func pdfText(t *testing.T, md string) (string, *Rendered) {
	t.Helper()
	r, err := Render(".pdf", md, nil)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := gpdf.Extract(context.Background(), bytes.NewReader(r.Data), int64(len(r.Data)))
	if err != nil {
		t.Fatal(err)
	}
	return doc.Text(), r
}

func TestColumnWidths(t *testing.T) {
	cases := []struct {
		name           string
		natural, least []float64
		total          float64
	}{
		{"all fit on one line", []float64{40, 60, 100}, []float64{30, 30, 40}, 400},
		{"words fit, lines do not", []float64{40, 60, 900}, []float64{38, 55, 70}, 400},
		{"one long column does not starve the rest", []float64{45, 50, 48, 52, 3000}, []float64{45, 50, 48, 52, 80}, 300},
		{"exactly the longest words", []float64{200, 300}, []float64{100, 200}, 300},
		{"longest words do not fit", []float64{90, 90, 300, 40}, []float64{90, 90, 250, 20}, 200},
		{"very many columns", repeat(60, 30), repeat(50, 30), 468},
		{"one column", []float64{50}, []float64{20}, 468},
	}
	for _, c := range cases {
		got := columnWidths(c.natural, c.least, c.total)
		sum, sumL := 0.0, 0.0
		for i, w := range got {
			sum += w
			sumL += c.least[i]
			if w <= 0 {
				t.Errorf("%s: column %d has no width: %v", c.name, i, got)
			}
		}
		if math.Abs(sum-c.total) > 1e-6 {
			t.Errorf("%s: widths add up to %v, want %v", c.name, sum, c.total)
		}
		for i, w := range got {
			if sumL <= c.total && w < c.least[i]-1e-9 {
				t.Errorf("%s: column %d is %v wide, under its longest word %v", c.name, i, w, c.least[i])
			}
			// With too little room a column that asks for less than an
			// even share keeps it whole.
			if sumL > c.total && c.least[i] <= c.total/float64(len(got)) && w < c.least[i]-1e-9 {
				t.Errorf("%s: column %d lost room it could have kept: %v of %v", c.name, i, w, c.least[i])
			}
		}
	}
}

func repeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

const (
	narrowTable = "| System | Partition | Core-hours | GPU-hours | Utilization | Notes |\n|:--|:--|--:|--:|:-:|:--|\n" +
		"| Alpha | standard | 1,204,330 | 0 | 71% | Steady across the quarter |\n" +
		"| Alpha | gpu | 88,410 | 22,102 | 93% | Constrained in all three months; queue wait exceeded two days for large jobs |\n"
	wideTable = "| Project | Oct core-h | Nov core-h | Dec core-h | Oct GPU-h | Nov GPU-h | Dec GPU-h | Allocation | Used | Remaining | Status |\n" +
		"|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|---|\n" +
		"| climate-program | 120,000 | 131,000 | 140,200 | 2,100 | 2,400 | 2,950 | 450,000 | 391,200 | 58,800 | On track |\n" +
		"| ml-team | 44,000 | 52,300 | 61,000 | 5,800 | 6,900 | 7,400 | 200,000 | 157,300 | 42,700 | Watch |\n"
	styledTable = "| Item | Detail |\n|---|---|\n| **Total** | Uses `sacct` and a [link](https://example.org) with *emphasis* in a long sentence that has to wrap onto more lines than one |\n"
)

// tableOf is the first table in Markdown.
func tableOf(t *testing.T, md string) Block {
	t.Helper()
	for _, b := range Parse(md) {
		if b.Kind == KTable {
			return b
		}
	}
	t.Fatal("no table")
	return Block{}
}

func TestPDFTableKeepsWordsWhole(t *testing.T) {
	cases := []struct {
		name, md string
		o        Options
	}{
		{"six columns", narrowTable, Options{}},
		{"six columns, serif, justified", narrowTable, Options{Serif: true, Justify: true, Size: 11}},
		{"eleven columns", wideTable, Options{}},
		{"eleven columns, serif", wideTable, Options{Serif: true, Size: 11}},
		{"eleven columns, large text", wideTable, Options{Size: 14, Landscape: true}},
		{"styled cells", styledTable, Options{Margin: 2}},
	}
	for _, c := range cases {
		w := newPDFWriter(nil, c.o)
		w.p.AddPage()
		b := tableOf(t, c.md)
		lay := w.layTable(b)
		if !lay.fits {
			t.Errorf("%s: the table was taken as too wide", c.name)
		}
		sum := 0.0
		for _, cw := range lay.widths {
			sum += cw
		}
		if want := w.right - w.left; math.Abs(sum-want) > 1e-6 {
			t.Errorf("%s: table is %v wide, the text column %v", c.name, sum, want)
		}
		rows := append([][][]Run{b.Header}, b.Rows...)
		laid := append([][][][]lword{lay.head}, lay.rows...)
		for ri, row := range rows {
			for ci, cell := range row {
				words := w.words(cellRuns(cell, ri == 0), lay.size)
				n := 0
				for _, ln := range laid[ri][ci] {
					n += len(ln)
					if lw := lineWidth(ln); lw > lay.widths[ci]-2*lay.pad+slack {
						t.Errorf("%s: row %d column %d: a line %v wide in %v", c.name, ri, ci, lw, lay.widths[ci]-2*lay.pad)
					}
				}
				if n != len(words) {
					t.Errorf("%s: row %d column %d: %d words were set as %d pieces: %q", c.name, ri, ci, len(words), n, Plain(cell))
				}
				for _, wd := range words {
					if lay.widths[ci] < wd.w+2*lay.pad {
						t.Errorf("%s: column %d is %v wide, under its word of %v", c.name, ci, lay.widths[ci], wd.w+2*lay.pad)
					}
				}
			}
		}
	}
}

func TestCrowdedTableDegrades(t *testing.T) {
	head, row := "|", "|"
	for i := 0; i < 24; i++ {
		head += " Reservation" + strconv.Itoa(i) + " |"
		row += " checkpointed-value |"
	}
	md := "# Wide\n\n" + head + "\n" + strings.Repeat("|---", 24) + "|\n" + row + "\n"
	w := newPDFWriter(nil, Options{})
	w.p.AddPage()
	lay := w.layTable(tableOf(t, md))
	if lay.fits || len(lay.widths) != 24 {
		t.Fatalf("fits %v, %d columns", lay.fits, len(lay.widths))
	}
	sum := 0.0
	for _, cw := range lay.widths {
		sum += cw
		if cw < 2*lay.pad {
			t.Errorf("a column of %v has no room for text", cw)
		}
	}
	if math.Abs(sum-(w.right-w.left)) > 1e-6 {
		t.Errorf("table is %v wide", sum)
	}
	for _, suffix := range []string{".pdf", ".docx"} {
		r, err := Render(suffix, md, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Problems) != 1 || !strings.Contains(r.Problems[0], "24 columns") {
			t.Errorf("%s: problems %v", suffix, r.Problems)
		}
	}
	// The same table fits no better for being asked twice, and a table
	// that fits is not reported.
	if r, _ := Render(".pdf", narrowTable, nil); len(r.Problems) != 0 {
		t.Errorf("problems %v", r.Problems)
	}
}

var gridCol = regexp.MustCompile(`<w:gridCol w:w="(\d+)"/>`)

func TestDOCXTableWidth(t *testing.T) {
	cases := []struct {
		name, front, table string
		textWidth          int // twips
		size               int // the most the table's text may be, in half points; 0 for the body's size
	}{
		{"defaults", "", narrowTable, 9360, 18},
		{"few columns keep the body size", "", styledTable, 9360, 0},
		{"eleven columns", "", wideTable, 9360, 18},
		{"serif, justified", "---\nalign: justify\nfont: Times New Roman\nsize: 11\n---\n", wideTable, 9360, 18},
		{"landscape, narrow margins", "---\norientation: landscape\nmargin: 0.5\n---\n", wideTable, 14400, 18},
		{"wide margins", "---\nmargin: 2\nsize: 12\n---\n", narrowTable, 6480, 20},
	}
	for _, c := range cases {
		parts, r := docxParts(t, c.front+"# Tables\n\n"+c.table)
		doc := parts["word/document.xml"]
		sum := 0
		var widths []int
		for _, m := range gridCol.FindAllStringSubmatch(doc, -1) {
			n, _ := strconv.Atoi(m[1])
			widths = append(widths, n)
			sum += n
		}
		if sum != c.textWidth {
			t.Errorf("%s: columns add up to %d twips, the text column is %d", c.name, sum, c.textWidth)
		}
		if !strings.Contains(doc, `<w:tblW w:w="`+strconv.Itoa(c.textWidth)+`" w:type="dxa"/>`) {
			t.Errorf("%s: the table's width is not the text column's", c.name)
		}
		if n := strings.Count(doc, "<w:tr>"); n == 0 || n != strings.Count(doc, "<w:cantSplit/>") {
			t.Errorf("%s: %d rows, %d kept whole", c.name, n, strings.Count(doc, "<w:cantSplit/>"))
		}
		// A table of many columns is set smaller than the body, and
		// smaller still only as far as it must be, never under 7 pt.
		set := 0
		if m := regexp.MustCompile(`<w:szCs w:val="(\d+)"/>`).FindStringSubmatch(doc); m != nil {
			set, _ = strconv.Atoi(m[1])
		}
		if c.size == 0 && set != 0 || c.size != 0 && (set > c.size || set < 14) {
			t.Errorf("%s: table text is %d half points, want at most %d", c.name, set, c.size)
		}
		if len(r.Problems) != 0 {
			t.Errorf("%s: problems %v", c.name, r.Problems)
		}
		// No column is narrower than its longest word, measured as the
		// widths were worked out, with the cell's margins.
		o, _, _ := SplitFrontMatter(c.front)
		d := &docxw{o: o}
		grow := d.grow()
		size := 11.0
		if o.Size > 0 {
			size = o.Size
		}
		if m := regexp.MustCompile(`<w:szCs w:val="(\d+)"/>`).FindStringSubmatch(doc); m != nil {
			n, _ := strconv.Atoi(m[1])
			size = float64(n) / 2
		}
		margins := 10.0
		if strings.Contains(doc, "<w:tblCellMar>") {
			margins = 6
		}
		b := tableOf(t, c.table)
		for ri, row := range append([][][]Run{b.Header}, b.Rows...) {
			for ci, cell := range row {
				for _, wd := range d.meas.words(cellRuns(cell, ri == 0), size) {
					if need := wd.w*grow + margins; float64(widths[ci])/20 < need {
						t.Errorf("%s: column %d is %v pt wide, under its word of %v pt", c.name, ci, float64(widths[ci])/20, need)
					}
				}
			}
		}
	}
}

func TestFrontMatterPageFurniture(t *testing.T) {
	cases := []struct {
		front    string
		want     Options
		problems int
		note     string
	}{
		{"header: Draft\nfooter: Quarterly report\nspacing: 1.15", Options{Header: "Draft", Footer: "Quarterly report", Spacing: 1.15}, 0, `(1.15 line spacing, header "Draft", footer "Quarterly report")`},
		{"spacing: 2", Options{Spacing: 2}, 0, "(2 line spacing)"},
		{"spacing: double", Options{Spacing: 2}, 0, "(2 line spacing)"},
		{"line-spacing: 1.5x", Options{Spacing: 1.5}, 0, "(1.5 line spacing)"},
		{"spacing: 0.8", Options{}, 1, ""},
		{"spacing: 3", Options{}, 1, ""},
		{"spacing: wide", Options{}, 1, ""},
		{`header: "Review copy: not for release"`, Options{Header: "Review copy: not for release"}, 0, `(header "Review copy: not for release")`},
		{"footer:   spaced    out  ", Options{Footer: "spaced out"}, 0, `(footer "spaced out")`},
		{"header: " + strings.Repeat("long ", 30), Options{}, 1, ""},
		{"header:", Options{}, 0, ""},
		{"align: justify\nfooter: Sample\nheadr: x", Options{Justify: true, Footer: "Sample"}, 1, `(justified, footer "Sample")`},
	}
	for _, c := range cases {
		o, body, problems := SplitFrontMatter("---\n" + c.front + "\n---\nText\n")
		if o != c.want {
			t.Errorf("%q: options %+v, want %+v", c.front, o, c.want)
		}
		if body != "Text\n" {
			t.Errorf("%q: body %q", c.front, body)
		}
		if len(problems) != c.problems {
			t.Errorf("%q: problems %v", c.front, problems)
		}
		if got := o.describe(); got != c.note && got != " "+c.note {
			t.Errorf("%q: described as %q, want %q", c.front, got, c.note)
		}
	}
	for _, key := range []string{"spacing", "header", "footer", "margin", "size", "font", "orientation", "align"} {
		if !strings.Contains(OptionKeys, key) {
			t.Errorf("OptionKeys lacks %s", key)
		}
	}
}

func TestPageFurnitureRendered(t *testing.T) {
	md := "---\nheader: Sample marking\nfooter: Utilization report\nspacing: 2\n---\n# Title\n\n" + strings.Repeat("A line of body text that fills the page. ", 200) + "\n\n| a | b |\n|---|---|\n| 1 | 2 |\n"
	text, r := pdfText(t, md)
	plain, _ := pdfText(t, strings.Replace(md, "spacing: 2\n", "", 1))
	if n := strings.Count(text, "Sample marking"); n < 2 || n != strings.Count(text, "Utilization report") {
		t.Errorf("header on %d pages, footer on %d", n, strings.Count(text, "Utilization report"))
	}
	if strings.Count(text, "Sample marking") <= strings.Count(plain, "Sample marking") {
		t.Error("double spacing took no more pages")
	}
	if !strings.Contains(r.Note, `2 line spacing, header "Sample marking", footer "Utilization report"`) {
		t.Errorf("note %q", r.Note)
	}

	parts, _ := docxParts(t, md)
	if !strings.Contains(parts["word/header1.xml"], "Sample marking") || !strings.Contains(parts["word/document.xml"], `<w:headerReference w:type="default" r:id="rIdHdr"/>`) ||
		!strings.Contains(parts["[Content_Types].xml"], "/word/header1.xml") || !strings.Contains(parts["word/_rels/document.xml.rels"], `Target="header1.xml"`) {
		t.Error("the Word header is missing or not registered")
	}
	ftr := parts["word/footer1.xml"]
	if i, j := strings.Index(ftr, "Utilization report"), strings.Index(ftr, " PAGE "); i < 0 || j < i || strings.Count(ftr, "<w:p>") != 2 {
		t.Errorf("the footer's text is not on a line of its own above the page number: %s", ftr)
	}
	if !strings.Contains(parts["word/styles.xml"], `w:line="480"`) {
		t.Error("styles.xml lacks double spacing")
	}
	if !strings.Contains(parts["word/document.xml"], `<w:spacing w:before="0" w:after="0" w:line="276" w:lineRule="auto"/>`) {
		t.Error("table lines took the body's spacing")
	}

	// Without the options a document has no header part and the default
	// spacing.
	parts, _ = docxParts(t, "# Title\n\nText.\n")
	if _, ok := parts["word/header1.xml"]; ok || strings.Contains(parts["word/document.xml"], "headerReference") {
		t.Error("a header was made without the option")
	}
	if !strings.Contains(parts["word/styles.xml"], `w:line="276"`) || strings.Count(parts["word/footer1.xml"], "<w:p>") != 1 {
		t.Error("the default spacing or footer changed")
	}
}

func TestDOCXSettings(t *testing.T) {
	parts, _ := docxParts(t, "---\nalign: justify\n---\n**Prepared for:** Program Office  \n**Date:** 14 November 2026\n")
	set := parts["word/settings.xml"]
	if !strings.Contains(set, "<w:doNotExpandShiftReturn/>") {
		t.Errorf("settings.xml does not stop a broken justified line from stretching: %q", set)
	}
	if !strings.Contains(parts["[Content_Types].xml"], `<Override PartName="/word/settings.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.settings+xml"/>`) {
		t.Error("settings.xml is not in [Content_Types].xml")
	}
	if !strings.Contains(parts["word/_rels/document.xml.rels"], `Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/settings" Target="settings.xml"`) {
		t.Error("settings.xml is not in document.xml.rels")
	}
	if doc := parts["word/document.xml"]; !strings.Contains(doc, "<w:br/>") || !strings.Contains(doc, `<w:jc w:val="both"/>`) {
		t.Error("the justified paragraph lost its line break")
	}
}

func TestFootnotes(t *testing.T) {
	md := "# Report\n\nSecond is cited first[^b], then the first[^a], and again[^a].\n\n[^a]: Exports cover **three** months.\n[^b]: Taken from the scheduler.\n[^c]: Never cited.\n\n## Appendix\n\nLast words.\n"
	bs := Parse(md)
	var sup []string
	for _, r := range bs[1].Runs {
		if r.Sup {
			sup = append(sup, r.Text)
		}
		if strings.Contains(r.Text, "[^") {
			t.Errorf("a reference was left as text: %q", r.Text)
		}
	}
	if strings.Join(sup, ",") != "1,2,2" {
		t.Errorf("references numbered %v", sup)
	}
	n := len(bs)
	if n < 2 || bs[n-2].Kind != KHeading || Plain(bs[n-2].Runs) != NotesHeading || bs[n-1].Kind != KList || !bs[n-1].Ordered {
		t.Fatalf("the document does not end with its notes: %+v", bs)
	}
	notes := bs[n-1].Items
	if len(notes) != 2 || !strings.HasPrefix(Plain(notes[0][0].Runs), "Taken from") || !strings.HasPrefix(Plain(notes[1][0].Runs), "Exports cover") {
		t.Errorf("notes %+v", notes)
	}
	for _, b := range bs {
		if strings.Contains(Plain(b.Runs), "[^") {
			t.Errorf("a definition was left as text: %q", Plain(b.Runs))
		}
	}
	// A document with no footnotes gains no heading.
	if got := Parse("# Report\n\nNo notes here.\n"); len(got) != 2 {
		t.Errorf("blocks %+v", got)
	}

	parts, _ := docxParts(t, md)
	doc := parts["word/document.xml"]
	if strings.Count(doc, `<w:vertAlign w:val="superscript"/>`) != 3 || strings.Contains(doc, "[^") {
		t.Error("Word references are not superscript numbers")
	}
	if i, j := strings.Index(doc, ">Notes<"), strings.Index(doc, "Taken from the scheduler."); i < 0 || j < i || strings.Index(doc, "Last words.") > i {
		t.Error("Word notes are not listed at the end")
	}
	text, _ := pdfText(t, md)
	if strings.Contains(text, "[^") || !strings.Contains(text, "Notes") || strings.Index(text, "Taken from the scheduler.") < strings.Index(text, "Last words.") {
		t.Errorf("PDF notes are not listed at the end: %q", text)
	}
}

func TestTaskLists(t *testing.T) {
	md := "- [x] Records collected\n- [ ] Review booked\n  - [X] Room\n- an ordinary item\n\n1. first\n2. second\n"
	bs := Parse(md)
	l := bs[0]
	if len(l.Checks) != 3 || l.Checks[0] != Checked || l.Checks[1] != Unchecked || l.Checks[2] != 0 {
		t.Fatalf("checks %v", l.Checks)
	}
	if got := Plain(l.Items[0][0].Runs); got != "Records collected" {
		t.Errorf("item text %q", got)
	}
	if nested := l.Items[1][1]; nested.Kind != KList || len(nested.Checks) != 1 || nested.Checks[0] != Checked {
		t.Errorf("nested %+v", nested)
	}
	if bs[1].Checks != nil {
		t.Errorf("a list without tasks has boxes: %v", bs[1].Checks)
	}
	parts, _ := docxParts(t, md)
	doc := parts["word/document.xml"]
	if strings.Count(doc, "\u2612") != 2 || strings.Count(doc, "\u2610") != 1 || strings.Contains(doc, "[x]") {
		t.Error("Word tasks lack their boxes")
	}
	// A task's box stands in place of its bullet.
	if strings.Count(doc, "<w:numPr>") != 3 {
		t.Errorf("%d numbered or bulleted paragraphs, want 3", strings.Count(doc, "<w:numPr>"))
	}
	text, _ := pdfText(t, md)
	if strings.Contains(text, "[x]") || strings.Contains(text, "[ ]") || !strings.Contains(text, "Records collected") {
		t.Errorf("PDF tasks: %q", text)
	}
}

func TestGlyphFit(t *testing.T) {
	ascii := func(r rune) bool { return r >= 0x20 && r < 0x7F }
	latin := func(r rune) bool {
		return ascii(r) || r == '\u00a0' || r == '\u2013' || r == '\u00d7' || r == '\u2022' || r == '\ufffd'
	}
	cases := []struct {
		name, in, want string
		cov            coverage
		missing        string
	}{
		{"plain text is untouched", "Core-hours 1,204", "Core-hours 1,204", ascii, ""},
		{"no coverage known", "anything \u2011 \u2713", "anything \u2011 \u2713", nil, ""},
		{"hyphens", "non\u2011breaking \u2010word", "non-breaking -word", ascii, ""},
		{"spaces", "10\u202fkg and 5\u2009m", "10 kg and 5 m", ascii, ""},
		{"a narrow space stays unbreaking", "10\u202fkg", "10\u00a0kg", latin, ""},
		{"quotes", "\u201cquoted\u201d it\u2019s", `"quoted" it's`, ascii, ""},
		{"arrows", "a \u2192 b", "a -> b", ascii, ""},
		{"marks", "\u2713 done \u2717 open", "[x] done x open", ascii, ""},
		{"a cross becomes a multiplication sign", "\u2717", "\u00d7", latin, ""},
		{"bullets", "\u25aa item", "\u2022 item", latin, ""},
		{"minus and ellipsis", "\u22125 \u2026", "\u20135 ...", latin, ""},
		{"minus without a dash", "\u22125", "-5", ascii, ""},
		{"zero width marks draw nothing", "a\u200bb\ufeffc", "abc", ascii, ""},
		{"no equivalent", "snow \u2603 and \u2603 and \U0001F600", "snow ? and ? and ?", ascii, "\u2603,\U0001F600"},
		{"a placeholder the font has", "\u2603", "\ufffd", latin, "\u2603"},
		{"line breaks are kept", "a\nb\tc", "a\nb\tc", ascii, ""},
	}
	for _, c := range cases {
		var g glyphs
		if got := g.fit(c.in, c.cov); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
		var missing []string
		for _, r := range g.missing {
			missing = append(missing, string(r))
		}
		if got := strings.Join(missing, ","); got != c.missing {
			t.Errorf("%s: missing %q, want %q", c.name, got, c.missing)
		}
		if (g.problem() == "") != (c.missing == "") {
			t.Errorf("%s: problem %q", c.name, g.problem())
		}
	}
	var g glyphs
	g.fit("\u2603", ascii)
	if got := g.problem(); got != "1 character is not in the PDF font and was replaced by a placeholder: \u2603 (U+2603)" {
		t.Errorf("problem %q", got)
	}
	g.fit("\U0001F600 \u2603", ascii)
	if got := g.problem(); !strings.HasPrefix(got, "2 characters are not in the PDF font and were replaced by a placeholder: \u2603 (U+2603), \U0001F600 (U+1F600)") {
		t.Errorf("problem %q", got)
	}
	g = glyphs{}
	for r := rune(0x4E00); r < 0x4E20; r++ {
		g.fit(string(r), ascii)
	}
	if got := g.problem(); !strings.HasPrefix(got, "32 characters") || !strings.HasSuffix(got, "and 20 more") {
		t.Errorf("problem %q", got)
	}
}

func TestPDFReportsMissingGlyphs(t *testing.T) {
	text, r := pdfText(t, "# Status\n\nA non\u2011breaking hyphen, a face \U0001F600, and a table:\n\n| Item | State |\n|---|---|\n| Review | \U0001F600 done |\n")
	if len(r.Problems) != 1 || !strings.Contains(r.Problems[0], "1 character is not in the PDF font") || !strings.Contains(r.Problems[0], "U+1F600") {
		t.Errorf("problems %v", r.Problems)
	}
	// The hyphen is drawn, as itself or as a plain one, never dropped.
	if !strings.Contains(text, "non\u2011breaking") && !strings.Contains(text, "non-breaking") {
		t.Errorf("the hyphen was lost: %q", text)
	}
	if _, r := pdfText(t, "# Status\n\nPlain text, 5 m, 85 %.\n"); len(r.Problems) != 0 {
		t.Errorf("problems %v", r.Problems)
	}
	// Word draws with the reader's fonts, so nothing is replaced there.
	if r, _ := Render(".docx", "A face \U0001F600.\n", nil); len(r.Problems) != 0 {
		t.Errorf("problems %v", r.Problems)
	}
}

func TestWrap(t *testing.T) {
	w := newPDFWriter(nil, Options{})
	w.p.AddPage()
	const size = 10.0
	rs := Parse("Usage **records**[^1] were taken from `sacct`, then joined.\n\n[^1]: n\n")[0].Runs
	words := w.words(rs, size)
	var texts []string
	for _, wd := range words {
		s := ""
		for _, p := range wd.pieces {
			s += p.text
		}
		texts = append(texts, s)
	}
	// A word and what touches it (its footnote number, its comma) are one.
	if got := strings.Join(texts, "|"); got != "Usage|records1|were|taken|from|sacct,|then|joined." {
		t.Errorf("words %q", got)
	}
	_, least := extent(words)
	for _, width := range []float64{least, least + 20, 200, 2000} {
		n := 0
		for _, ln := range w.wrap(words, size, width) {
			n += len(ln)
			if lineWidth(ln) > width+slack {
				t.Errorf("width %v: a line of %v", width, lineWidth(ln))
			}
		}
		if n != len(words) {
			t.Errorf("width %v: %d words set as %d pieces", width, len(words), n)
		}
	}
	// Only a word wider than the line is broken, and all of it is kept.
	lines := w.wrap(words, size, least/2)
	got := ""
	for _, ln := range lines {
		if lineWidth(ln) > least/2+slack {
			t.Errorf("a line of %v in %v", lineWidth(ln), least/2)
		}
		for _, wd := range ln {
			for _, p := range wd.pieces {
				got += p.text
			}
		}
	}
	if got != strings.Join(texts, "") {
		t.Errorf("text lost in breaking: %q", got)
	}
	// Hard breaks start lines, and two of them leave an empty line.
	lines = w.wrap(w.words([]Run{{Text: "one\ntwo\n\nthree"}}, size), size, 500)
	if len(lines) != 4 || len(lines[2]) != 0 {
		t.Errorf("lines %v", lines)
	}
}

func TestKeepTogether(t *testing.T) {
	para := strings.Repeat("Filler words for the page. ", 12)
	// The heading falls where it and three lines no longer fit: it starts
	// the next page with its text.
	for n := 8; n < 14; n++ {
		md := strings.Repeat(para+"\n\n", n) + "## KeptHeading\n\n" + para + "\n"
		r, err := Render(".pdf", md, nil)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := gpdf.Extract(context.Background(), bytes.NewReader(r.Data), int64(len(r.Data)))
		if err != nil {
			t.Fatal(err)
		}
		for _, pg := range doc.Pages {
			text := strings.TrimSpace(pg.Text())
			if i := strings.Index(text, "KeptHeading"); i >= 0 && !strings.Contains(text[i:], "Filler") {
				t.Errorf("%d paragraphs: the heading was left at the foot of page %d", n, pg.Number)
			}
		}
	}
	// A table's header row is not left alone at the foot of a page.
	for n := 8; n < 14; n++ {
		md := strings.Repeat(para+"\n\n", n) + "Lead.\n\n| KeptHeader | B |\n|---|---|\n| FirstRow | 2 |\n| second row | 3 |\n"
		r, err := Render(".pdf", md, nil)
		if err != nil {
			t.Fatal(err)
		}
		doc, _ := gpdf.Extract(context.Background(), bytes.NewReader(r.Data), int64(len(r.Data)))
		for _, pg := range doc.Pages {
			text := pg.Text()
			if strings.Contains(text, "KeptHeader") && !strings.Contains(text, "FirstRow") {
				t.Errorf("%d paragraphs: the header row is alone on page %d", n, pg.Number)
			}
		}
	}
	parts, _ := docxParts(t, "*Table 1:* Figures.\n\n| a | b |\n|---|---|\n| 1 | 2 |\n")
	if doc := parts["word/document.xml"]; !strings.Contains(doc, "<w:p><w:pPr><w:keepNext/></w:pPr>") || !strings.Contains(doc, "<w:pPr><w:keepNext/><w:spacing") {
		t.Error("the caption and header row are not kept with what follows")
	}
}
