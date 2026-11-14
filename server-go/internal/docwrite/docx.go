package docwrite

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

// A Word document from Markdown blocks, written as WordprocessingML with
// the parts Word, Google Docs and LibreOffice need: styles for headings,
// code and quotes, numbering for bulleted and numbered lists (each
// numbered list restarts at its own start), tables with a fixed grid and
// a repeating header row, web links as hyperlinks, and settings that make
// Word, Google Docs and LibreOffice lay the document out alike.

type docxw struct {
	o        Options
	tw, th   int // the text area's width and height, in twips
	body     strings.Builder
	rels     []string // hyperlink targets, rId 100+
	nums     []int    // per numbered list: its start
	depth    int
	pics     map[string]*picture
	media    []string       // embedded images' sources, in order: word/media/imageN
	ids      map[string]int // source to N
	drawn    int            // drawings placed, for their ids
	check    int8           // the box of the task list item being written
	meas     *pdfw          // measures table text; made for the first table
	problems []string
}

// page is the page's width and height and its margin, in twips.
func (o Options) page() (w, h, m int) {
	w, h = 12240, 15840
	if o.Landscape {
		w, h = h, w
	}
	return w, h, int(o.marginPt()*20 + 0.5)
}

// bodyJc is the alignment a body paragraph carries.
func (d *docxw) bodyJc() string {
	if d.o.Justify {
		return `<w:jc w:val="both"/>`
	}
	return ""
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// runs is inline text as Word runs. bold sets it all bold, and size,
// when not 0, sets its size in half points (a table's text).
func (d *docxw) runs(rs []Run, bold bool, size int) string {
	var sb strings.Builder
	for _, r := range rs {
		props := ""
		if r.Bold || bold {
			props += "<w:b/>"
		}
		if r.Italic {
			props += "<w:i/>"
		}
		if r.Strike {
			props += "<w:strike/>"
		}
		switch {
		case r.Code && size > 0:
			props = `<w:rFonts w:ascii="Consolas" w:hAnsi="Consolas" w:cs="Consolas"/>` + props + fmt.Sprintf(`<w:sz w:val="%d"/><w:szCs w:val="%d"/>`, size-2, size-2)
		case r.Code:
			props = `<w:rFonts w:ascii="Consolas" w:hAnsi="Consolas" w:cs="Consolas"/>` + props + `<w:sz w:val="19"/>`
		case size > 0:
			props += fmt.Sprintf(`<w:sz w:val="%d"/><w:szCs w:val="%d"/>`, size, size)
		}
		if r.Sup {
			props += `<w:vertAlign w:val="superscript"/>`
		}
		if r.Link != "" {
			props = `<w:rStyle w:val="Hyperlink"/>` + props
		}
		var t strings.Builder
		for i, part := range strings.Split(r.Text, "\n") {
			if i > 0 {
				t.WriteString("<w:br/>")
			}
			if part != "" {
				t.WriteString(`<w:t xml:space="preserve">` + esc(part) + `</w:t>`)
			}
		}
		run := "<w:r>"
		if props != "" {
			run += "<w:rPr>" + props + "</w:rPr>"
		}
		run += t.String() + "</w:r>"
		if r.Link != "" {
			d.rels = append(d.rels, r.Link)
			run = fmt.Sprintf(`<w:hyperlink r:id="rId%d">%s</w:hyperlink>`, 99+len(d.rels), run)
		}
		sb.WriteString(run)
	}
	return sb.String()
}

func (d *docxw) para(style, props string, rs []Run) {
	d.paraWith(style, props, "", rs)
}

// paraWith is a paragraph whose text follows lead, which is runs already
// written out.
func (d *docxw) paraWith(style, props, lead string, rs []Run) {
	d.body.WriteString("<w:p>")
	if style != "" || props != "" {
		d.body.WriteString("<w:pPr>")
		if style != "" {
			d.body.WriteString(`<w:pStyle w:val="` + style + `"/>`)
		}
		d.body.WriteString(props)
		d.body.WriteString("</w:pPr>")
	}
	d.body.WriteString(lead)
	d.body.WriteString(d.runs(rs, false, 0))
	d.body.WriteString("</w:p>")
}

func (d *docxw) blocks(bs []Block, list string, level int) {
	for i, b := range bs {
		// The first paragraph of a list item carries its bullet or number;
		// the item's later paragraphs sit at its indent.
		numPr := ""
		if list != "" {
			if i == 0 {
				numPr = fmt.Sprintf(`<w:numPr><w:ilvl w:val="%d"/><w:numId w:val="%s"/></w:numPr>`, level, list)
			} else {
				numPr = fmt.Sprintf(`<w:ind w:left="%d"/>`, 720*(level+1))
			}
		}
		check := int8(0)
		if i == 0 {
			check, d.check = d.check, 0
		}
		switch b.Kind {
		case KHeading:
			d.para(fmt.Sprintf("Heading%d", min(b.Level, 4)), "", b.Runs)
		case KPara:
			style := ""
			if list != "" {
				style = "ListParagraph"
			}
			if check != 0 && list != "" {
				// A task: a box in place of the bullet, at the bullet's
				// hanging indent. The boxes are the pair Word's own check
				// box control uses; the box with a check mark comes out as
				// a colored emoji where the font falls back.
				box := "\u2610"
				if check == Checked {
					box = "\u2612"
				}
				lead := `<w:r><w:rPr><w:rFonts w:ascii="Segoe UI Symbol" w:hAnsi="Segoe UI Symbol" w:eastAsia="Segoe UI Symbol" w:cs="Segoe UI Symbol"/></w:rPr><w:t>` + box + `</w:t></w:r><w:r><w:tab/></w:r>`
				d.paraWith(style, fmt.Sprintf(`<w:ind w:left="%d" w:hanging="360"/>`, 720*(level+1))+d.bodyJc(), lead, b.Runs)
				continue
			}
			keep := ""
			if isCaption(b) && i+1 < len(bs) && bs[i+1].Kind == KTable {
				keep = "<w:keepNext/>"
			}
			d.para(style, keep+numPr+d.bodyJc(), b.Runs)
		case KBreak:
			d.body.WriteString(`<w:p><w:r><w:br w:type="page"/></w:r></w:p>`)
		case KList:
			id := "1"
			if b.Ordered {
				d.nums = append(d.nums, b.Start)
				id = fmt.Sprint(1 + len(d.nums))
			}
			lvl := 0
			if list != "" {
				lvl = level + 1
			}
			for k, item := range b.Items {
				if len(item) == 0 {
					item = []Block{{Kind: KPara}}
				}
				d.check = 0
				if k < len(b.Checks) {
					d.check = b.Checks[k]
				}
				d.blocks(item, id, lvl)
			}
			d.check = 0
		case KCode:
			d.para("Code", "", []Run{{Text: b.Text}})
		case KQuote:
			for _, c := range b.Children {
				if c.Kind == KPara {
					d.para("Quote", d.bodyJc(), c.Runs)
				} else {
					d.blocks([]Block{c}, "", 0)
				}
			}
		case KRule:
			d.body.WriteString(`<w:p><w:pPr><w:pBdr><w:bottom w:val="single" w:sz="6" w:space="1" w:color="BFBFBF"/></w:pBdr></w:pPr></w:p>`)
		case KTable:
			d.table(b)
		case KImage:
			d.image(b, numPr, i+1 < len(bs) && isCaption(bs[i+1]))
		}
	}
}

// EMU per pixel at 96 pixels an inch.
const emuPerPx = 9525

// image places a picture inline in a paragraph of its own, at its own size
// or smaller to fit the page; one that could not be embedded shows its
// alt text. One with a caption under it leaves the caption room on its
// page.
func (d *docxw) image(b Block, props string, captioned bool) {
	pic := d.pics[b.Src]
	if pic == nil {
		if b.Text != "" {
			d.para("", props, []Run{{Text: "[" + b.Text + "]", Italic: true}})
		}
		return
	}
	n, ok := d.ids[b.Src]
	if !ok {
		d.media = append(d.media, b.Src)
		n = len(d.media)
		d.ids[b.Src] = n
	}
	d.drawn++
	maxH := float64(d.th) / 20
	if captioned {
		maxH -= 40
	}
	w, h := pic.size(float64(d.tw)/20, maxH)
	cx, cy := int(w*96/72*emuPerPx), int(h*96/72*emuPerPx)
	alt := esc(b.Text)
	d.body.WriteString(fmt.Sprintf(`<w:p><w:pPr><w:keepNext/>%s</w:pPr><w:r><w:drawing><wp:inline distT="0" distB="0" distL="0" distR="0"><wp:extent cx="%d" cy="%d"/><wp:docPr id="%d" name="Picture %d" descr="%s"/><wp:cNvGraphicFramePr><a:graphicFrameLocks noChangeAspect="1"/></wp:cNvGraphicFramePr><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:pic><pic:nvPicPr><pic:cNvPr id="%d" name="image%d.%s" descr="%s"/><pic:cNvPicPr/></pic:nvPicPr><pic:blipFill><a:blip r:embed="rIdImg%d"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill><pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr></pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing></w:r></w:p>`,
		props, cx, cy, d.drawn, d.drawn, alt, d.drawn, n, pic.ext(), alt, n, cx, cy))
}

// wordWidth is how wide a Word font sets text, against Arial as 1. The
// table's text is measured with the PDF's font and scaled by this, since
// the font the document names is the reader's and not this host's. A
// font that is not known is taken as a wide one.
func wordWidth(font string, serif bool) float64 {
	l := strings.ToLower(font)
	has := func(names ...string) bool {
		for _, n := range names {
			if strings.Contains(l, n) {
				return true
			}
		}
		return false
	}
	switch {
	case has("mono", "courier", "consolas"):
		return 1.25
	case l == "" || has("calibri", "carlito"):
		return 0.9
	case has("times", "liberation serif", "garamond"):
		return 0.9
	case has("aptos", "cambria", "segoe"):
		return 0.96
	case has("arial", "helvetica", "liberation"):
		return 1
	case serif:
		return 1.03
	}
	return 1.14
}

// grow scales text measured with the PDF's font to the Word font, with a
// little over for the differences between a font and its stand-ins.
func (d *docxw) grow() float64 {
	if d.meas == nil {
		d.meas = newPDFWriter(nil, d.o)
	}
	return wordWidth(d.o.Font, d.o.Serif) / d.meas.wide * 1.03
}

// tableGrid is a table's text size in points, whether its cells take
// narrow side margins to fit, and its column widths in twips, which add
// up to the text area's width exactly.
func (d *docxw) tableGrid(b Block, cols int) (float64, bool, []int) {
	if d.meas == nil {
		d.meas = newPDFWriter(nil, d.o)
	}
	body := 11.0
	if d.o.Size > 0 {
		body = d.o.Size
	}
	// Smaller in a table of many columns, as in the PDF.
	base := body
	if cols > 5 {
		base = body - 2
	}
	// A cell's side margins are 100 twips, or 60 in a crowded table, and
	// its border lies inside it.
	total := float64(d.tw) / 20
	size, pad, pts, fits := d.meas.tableLayout(b, cols, base, min(base, 7), total, 5.4, 3.4, d.grow())
	if !fits {
		d.problems = append(d.problems, crowded(cols))
	}
	widths := make([]int, cols)
	left := d.tw
	for c, p := range pts {
		widths[c] = int(p * 20)
		left -= widths[c]
	}
	for c := 0; left > 0; c = (c + 1) % cols {
		widths[c]++
		left--
	}
	return size, pad < 5, widths
}

func (d *docxw) table(b Block) {
	cols := len(b.Header)
	for _, r := range b.Rows {
		cols = max(cols, len(r))
	}
	if cols == 0 {
		return
	}
	size, tight, widths := d.tableGrid(b, cols)
	cellMar := ""
	if tight {
		cellMar = `<w:tblCellMar><w:left w:w="60" w:type="dxa"/><w:right w:w="60" w:type="dxa"/></w:tblCellMar>`
	}
	sz := 0
	if d.o.Size > 0 && size != d.o.Size || d.o.Size == 0 && size != 11 {
		sz = int(size*2 + 0.5)
	}
	// Lines in a table stay as close as the default when the body's are
	// spread out.
	line := ""
	if d.o.Spacing > 0 {
		line = ` w:line="276" w:lineRule="auto"`
	}
	d.body.WriteString(`<w:tbl><w:tblPr><w:tblStyle w:val="TableGrid"/><w:tblW w:w="` + fmt.Sprint(sumInts(widths)) + `" w:type="dxa"/><w:tblInd w:w="0" w:type="dxa"/><w:tblLayout w:type="fixed"/>` + cellMar + `<w:tblLook w:val="04A0" w:firstRow="1" w:lastRow="0" w:firstColumn="0" w:lastColumn="0" w:noHBand="0" w:noVBand="1"/></w:tblPr><w:tblGrid>`)
	for _, w := range widths {
		d.body.WriteString(fmt.Sprintf(`<w:gridCol w:w="%d"/>`, w))
	}
	d.body.WriteString("</w:tblGrid>")
	row := func(cells [][]Run, head bool) {
		// A row stays whole on its page, and the header row repeats on
		// each page and stays with the row under it.
		d.body.WriteString("<w:tr><w:trPr><w:cantSplit/>")
		if head {
			d.body.WriteString("<w:tblHeader/>")
		}
		d.body.WriteString("</w:trPr>")
		for c := 0; c < cols; c++ {
			var rs []Run
			if c < len(cells) {
				rs = cellRuns(cells[c], false)
			}
			shade, keep := "", ""
			if head {
				shade = `<w:shd w:val="clear" w:color="auto" w:fill="F0F2F5"/>`
				keep = "<w:keepNext/>"
			}
			jc := ""
			if c < len(b.Align) && b.Align[c] != "" {
				jc = `<w:jc w:val="` + map[string]string{"left": "left", "right": "right", "center": "center"}[b.Align[c]] + `"/>`
			}
			d.body.WriteString(fmt.Sprintf(`<w:tc><w:tcPr><w:tcW w:w="%d" w:type="dxa"/>%s</w:tcPr><w:p><w:pPr>%s<w:spacing w:before="0" w:after="0"%s/>%s</w:pPr>%s</w:p></w:tc>`, widths[c], shade, keep, line, jc, d.runs(rs, head, sz)))
		}
		d.body.WriteString("</w:tr>")
	}
	if len(b.Header) > 0 {
		row(b.Header, true)
	}
	for _, r := range b.Rows {
		row(r, false)
	}
	d.body.WriteString("</w:tbl><w:p/>")
}

func sumInts(xs []int) int {
	n := 0
	for _, x := range xs {
		n += x
	}
	return n
}

// docxStyles is the styles part, with the body font and size the options
// ask for.
func docxStyles(o Options) string {
	font := o.Font
	if font == "" {
		font = "Calibri"
	}
	f := esc(font)
	sz := 22
	if o.Size > 0 {
		sz = int(o.Size*2 + 0.5)
	}
	line := 276
	if o.Spacing > 0 {
		line = int(o.Spacing*240 + 0.5)
	}
	return strings.NewReplacer("FONT", f, `"SZ"`, fmt.Sprintf(`"%d"`, sz), `"LINE"`, fmt.Sprintf(`"%d"`, line)).Replace(docxStylesTmpl)
}

const docxStylesTmpl = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="FONT" w:hAnsi="FONT" w:eastAsia="FONT" w:cs="FONT"/><w:sz w:val="SZ"/><w:szCs w:val="SZ"/><w:lang w:val="en-US"/></w:rPr></w:rPrDefault><w:pPrDefault><w:pPr><w:spacing w:after="120" w:line="LINE" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults>
<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/><w:qFormat/></w:style>
<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:keepNext/><w:spacing w:before="240" w:after="120"/><w:outlineLvl w:val="0"/></w:pPr><w:rPr><w:b/><w:sz w:val="32"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:keepNext/><w:spacing w:before="200" w:after="100"/><w:outlineLvl w:val="1"/></w:pPr><w:rPr><w:b/><w:sz w:val="27"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading3"><w:name w:val="heading 3"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:keepNext/><w:spacing w:before="160" w:after="80"/><w:outlineLvl w:val="2"/></w:pPr><w:rPr><w:b/><w:sz w:val="24"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading4"><w:name w:val="heading 4"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:keepNext/><w:spacing w:before="120" w:after="60"/><w:outlineLvl w:val="3"/></w:pPr><w:rPr><w:b/><w:i/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="ListParagraph"><w:name w:val="List Paragraph"/><w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:spacing w:after="60"/><w:ind w:left="720"/><w:contextualSpacing/></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="Code"><w:name w:val="Code"/><w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:shd w:val="clear" w:color="auto" w:fill="F4F5F7"/><w:spacing w:after="120" w:line="240" w:lineRule="auto"/></w:pPr><w:rPr><w:rFonts w:ascii="Consolas" w:hAnsi="Consolas" w:cs="Consolas"/><w:sz w:val="18"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Quote"><w:name w:val="Quote"/><w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:pBdr><w:left w:val="single" w:sz="18" w:space="8" w:color="BFBFBF"/></w:pBdr><w:ind w:left="360"/></w:pPr><w:rPr><w:color w:val="555555"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Header"><w:name w:val="header"/><w:basedOn w:val="Normal"/><w:pPr><w:spacing w:after="0" w:line="240" w:lineRule="auto"/><w:jc w:val="center"/></w:pPr><w:rPr><w:color w:val="808080"/><w:sz w:val="18"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Footer"><w:name w:val="footer"/><w:basedOn w:val="Normal"/><w:pPr><w:spacing w:after="0" w:line="240" w:lineRule="auto"/><w:jc w:val="center"/></w:pPr><w:rPr><w:color w:val="808080"/><w:sz w:val="18"/></w:rPr></w:style>
<w:style w:type="character" w:styleId="Hyperlink"><w:name w:val="Hyperlink"/><w:rPr><w:color w:val="1F4E9E"/><w:u w:val="single"/></w:rPr></w:style>
<w:style w:type="table" w:styleId="TableGrid"><w:name w:val="Table Grid"/><w:tblPr><w:tblBorders><w:top w:val="single" w:sz="4" w:space="0" w:color="C8C8C8"/><w:left w:val="single" w:sz="4" w:space="0" w:color="C8C8C8"/><w:bottom w:val="single" w:sz="4" w:space="0" w:color="C8C8C8"/><w:right w:val="single" w:sz="4" w:space="0" w:color="C8C8C8"/><w:insideH w:val="single" w:sz="4" w:space="0" w:color="C8C8C8"/><w:insideV w:val="single" w:sz="4" w:space="0" w:color="C8C8C8"/></w:tblBorders><w:tblCellMar><w:top w:w="40" w:type="dxa"/><w:left w:w="100" w:type="dxa"/><w:bottom w:w="40" w:type="dxa"/><w:right w:w="100" w:type="dxa"/></w:tblCellMar></w:tblPr></w:style>
</w:styles>`

func numberingXML(starts []int) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">`)
	bullets := []string{"•", "◦", "▪"}
	sb.WriteString(`<w:abstractNum w:abstractNumId="0"><w:multiLevelType w:val="hybridMultilevel"/>`)
	for l := 0; l < 9; l++ {
		sb.WriteString(fmt.Sprintf(`<w:lvl w:ilvl="%d"><w:start w:val="1"/><w:numFmt w:val="bullet"/><w:lvlText w:val="%s"/><w:lvlJc w:val="left"/><w:pPr><w:ind w:left="%d" w:hanging="360"/></w:pPr></w:lvl>`, l, bullets[l%3], 720*(l+1)))
	}
	sb.WriteString(`</w:abstractNum><w:abstractNum w:abstractNumId="1"><w:multiLevelType w:val="hybridMultilevel"/>`)
	fmts := []string{"decimal", "lowerLetter", "lowerRoman"}
	for l := 0; l < 9; l++ {
		sb.WriteString(fmt.Sprintf(`<w:lvl w:ilvl="%d"><w:start w:val="1"/><w:numFmt w:val="%s"/><w:lvlText w:val="%%%d."/><w:lvlJc w:val="left"/><w:pPr><w:ind w:left="%d" w:hanging="360"/></w:pPr></w:lvl>`, l, fmts[l%3], l+1, 720*(l+1)))
	}
	sb.WriteString(`</w:abstractNum><w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>`)
	for i, s := range starts {
		sb.WriteString(fmt.Sprintf(`<w:num w:numId="%d"><w:abstractNumId w:val="1"/><w:lvlOverride w:ilvl="0"><w:startOverride w:val="%d"/></w:lvlOverride></w:num>`, i+2, s))
	}
	sb.WriteString(`</w:numbering>`)
	return sb.String()
}

const wNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`

// docxFooter numbers the pages, under the footer's line of text when the
// document has one.
func docxFooter(text string) string {
	line := ""
	if text != "" {
		line = `<w:p><w:pPr><w:pStyle w:val="Footer"/></w:pPr><w:r><w:t xml:space="preserve">` + esc(text) + `</w:t></w:r></w:p>`
	}
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:ftr ` + wNS + `>` + line + `<w:p><w:pPr><w:pStyle w:val="Footer"/></w:pPr><w:r><w:fldChar w:fldCharType="begin"/></w:r><w:r><w:instrText xml:space="preserve"> PAGE </w:instrText></w:r><w:r><w:fldChar w:fldCharType="separate"/></w:r><w:r><w:t>1</w:t></w:r><w:r><w:fldChar w:fldCharType="end"/></w:r></w:p></w:ftr>`
}

// docxHeader is the line of text at the top of every page.
func docxHeader(text string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:hdr ` + wNS + `><w:p><w:pPr><w:pStyle w:val="Header"/></w:pPr><w:r><w:t xml:space="preserve">` + esc(text) + `</w:t></w:r></w:p></w:hdr>`
}

// docxSettings makes the programs that open the document lay it out
// alike: a justified line that ends at a line break is not stretched
// across the page, and a table's edge, not its text, stands at the margin
// (Word's current layout rules).
const docxSettings = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:settings ` + wNS + `><w:compat><w:doNotExpandShiftReturn/><w:compatSetting w:name="compatibilityMode" w:uri="http://schemas.microsoft.com/office/word" w:val="15"/></w:compat></w:settings>`

func renderDOCX(blocks []Block, pics map[string]*picture, o Options) ([]byte, []string, error) {
	pw, ph, m := o.page()
	d := &docxw{o: o, tw: pw - 2*m, th: ph - 2*m - 360, pics: pics, ids: map[string]int{}}
	d.blocks(blocks, "", 0)
	orient := ""
	if o.Landscape {
		orient = ` w:orient="landscape"`
	}
	headerRef, headerRel, headerType := "", "", ""
	if o.Header != "" {
		headerRef = `<w:headerReference w:type="default" r:id="rIdHdr"/>`
		headerRel = `<Relationship Id="rIdHdr" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/header" Target="header1.xml"/>`
		headerType = `<Override PartName="/word/header1.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.header+xml"/>`
	}
	doc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"><w:body>` +
		d.body.String() +
		fmt.Sprintf(`<w:sectPr>`+headerRef+`<w:footerReference w:type="default" r:id="rIdFtr"/><w:pgSz w:w="%d" w:h="%d"%s/><w:pgMar w:top="%d" w:right="%d" w:bottom="%d" w:left="%d" w:header="%d" w:footer="%d" w:gutter="0"/></w:sectPr></w:body></w:document>`,
			pw, ph, orient, m, m, m, m, m/2, m/2)
	var rels strings.Builder
	rels.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering" Target="numbering.xml"/><Relationship Id="rIdFtr" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/footer" Target="footer1.xml"/><Relationship Id="rIdSet" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/settings" Target="settings.xml"/>` + headerRel)
	for i, t := range d.rels {
		rels.WriteString(fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="%s" TargetMode="External"/>`, 100+i, esc(t)))
	}
	for i, src := range d.media {
		rels.WriteString(fmt.Sprintf(`<Relationship Id="rIdImg%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/image%d.%s"/>`, i+1, i+1, d.pics[src].ext()))
	}
	rels.WriteString(`</Relationships>`)
	now := time.Now().UTC().Format(time.RFC3339)
	core := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><dc:title>` + esc(Title(blocks)) + `</dc:title><dc:creator>ACTIVATE Studio</dc:creator><dcterms:created xsi:type="dcterms:W3CDTF">` + now + `</dcterms:created><dcterms:modified xsi:type="dcterms:W3CDTF">` + now + `</dcterms:modified></cp:coreProperties>`
	parts := []struct{ name, body string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Default Extension="png" ContentType="image/png"/><Default Extension="jpeg" ContentType="image/jpeg"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/><Override PartName="/word/numbering.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.numbering+xml"/><Override PartName="/word/footer1.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.footer+xml"/><Override PartName="/word/settings.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.settings+xml"/>` + headerType + `<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/></Relationships>`},
		{"word/document.xml", doc},
		{"word/styles.xml", docxStyles(o)},
		{"word/settings.xml", docxSettings},
		{"word/footer1.xml", docxFooter(o.Footer)},
		{"word/numbering.xml", numberingXML(d.nums)},
		{"word/_rels/document.xml.rels", rels.String()},
		{"docProps/core.xml", core},
	}
	if o.Header != "" {
		parts = append(parts, struct{ name, body string }{"word/header1.xml", docxHeader(o.Header)})
	}
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for _, p := range parts {
		f, err := z.Create(p.name)
		if err != nil {
			return nil, nil, err
		}
		if _, err := f.Write([]byte(p.body)); err != nil {
			return nil, nil, err
		}
	}
	for i, src := range d.media {
		pic := d.pics[src]
		// Deflated like the rest: LibreOffice refuses a document whose
		// stored entries carry a data descriptor, as Go's writer adds.
		f, err := z.Create(fmt.Sprintf("word/media/image%d.%s", i+1, pic.ext()))
		if err != nil {
			return nil, nil, err
		}
		if _, err := f.Write(pic.data); err != nil {
			return nil, nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), d.problems, nil
}
