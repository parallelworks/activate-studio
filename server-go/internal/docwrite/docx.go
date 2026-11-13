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
// a repeating header row, and web links as hyperlinks.

const textWidth = 9360 // twips: 6.5 inches between one inch margins

type docxw struct {
	body  strings.Builder
	rels  []string // hyperlink targets, rId 100+
	nums  []int    // per numbered list: its start
	depth int
	pics  map[string]*picture
	media []string       // embedded images' sources, in order: word/media/imageN
	ids   map[string]int // source to N
	drawn int            // drawings placed, for their ids
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func (d *docxw) runs(rs []Run, extra string) string {
	var sb strings.Builder
	for _, r := range rs {
		props := extra
		if r.Bold {
			props += "<w:b/>"
		}
		if r.Italic {
			props += "<w:i/>"
		}
		if r.Strike {
			props += "<w:strike/>"
		}
		if r.Code {
			props += `<w:rFonts w:ascii="Consolas" w:hAnsi="Consolas" w:cs="Consolas"/><w:sz w:val="19"/>`
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
	d.body.WriteString("<w:p>")
	if style != "" || props != "" {
		d.body.WriteString("<w:pPr>")
		if style != "" {
			d.body.WriteString(`<w:pStyle w:val="` + style + `"/>`)
		}
		d.body.WriteString(props)
		d.body.WriteString("</w:pPr>")
	}
	d.body.WriteString(d.runs(rs, ""))
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
		switch b.Kind {
		case KHeading:
			d.para(fmt.Sprintf("Heading%d", min(b.Level, 4)), "", b.Runs)
		case KPara:
			style := ""
			if list != "" {
				style = "ListParagraph"
			}
			d.para(style, numPr, b.Runs)
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
			for _, item := range b.Items {
				if len(item) == 0 {
					item = []Block{{Kind: KPara}}
				}
				d.blocks(item, id, lvl)
			}
		case KCode:
			d.para("Code", "", []Run{{Text: b.Text}})
		case KQuote:
			for _, c := range b.Children {
				if c.Kind == KPara {
					d.para("Quote", "", c.Runs)
				} else {
					d.blocks([]Block{c}, "", 0)
				}
			}
		case KRule:
			d.body.WriteString(`<w:p><w:pPr><w:pBdr><w:bottom w:val="single" w:sz="6" w:space="1" w:color="BFBFBF"/></w:pBdr></w:pPr></w:p>`)
		case KTable:
			d.table(b)
		case KImage:
			d.image(b, numPr)
		}
	}
}

// EMU per pixel at 96 pixels an inch, and the text area's size in EMU.
const (
	emuPerPx  = 9525
	emuWidth  = 6.5 * 914400
	emuHeight = 9 * 914400
)

// image places a picture inline in a paragraph of its own, at its own size
// or smaller to fit the page; one that could not be embedded shows its
// alt text.
func (d *docxw) image(b Block, props string) {
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
	w, h := pic.size(emuWidth/emuPerPx*72/96, emuHeight/emuPerPx*72/96)
	cx, cy := int(w*96/72*emuPerPx), int(h*96/72*emuPerPx)
	alt := esc(b.Text)
	d.body.WriteString(fmt.Sprintf(`<w:p><w:pPr><w:keepNext/>%s</w:pPr><w:r><w:drawing><wp:inline distT="0" distB="0" distL="0" distR="0"><wp:extent cx="%d" cy="%d"/><wp:docPr id="%d" name="Picture %d" descr="%s"/><wp:cNvGraphicFramePr><a:graphicFrameLocks noChangeAspect="1"/></wp:cNvGraphicFramePr><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:pic><pic:nvPicPr><pic:cNvPr id="%d" name="image%d.%s" descr="%s"/><pic:cNvPicPr/></pic:nvPicPr><pic:blipFill><a:blip r:embed="rIdImg%d"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill><pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr></pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing></w:r></w:p>`,
		props, cx, cy, d.drawn, d.drawn, alt, d.drawn, n, pic.ext(), alt, n, cx, cy))
}

func (d *docxw) table(b Block) {
	cols := len(b.Header)
	for _, r := range b.Rows {
		cols = max(cols, len(r))
	}
	if cols == 0 {
		return
	}
	// Column widths by the length of what they hold, at least 0.6 inch.
	want := make([]float64, cols)
	all := append([][][]Run{b.Header}, b.Rows...)
	for _, row := range all {
		for c := 0; c < cols && c < len(row); c++ {
			want[c] = max(want[c], float64(min(len([]rune(Plain(row[c]))), 60)))
		}
	}
	sum := 0.0
	for c := range want {
		want[c] = max(want[c], 6)
		sum += want[c]
	}
	widths := make([]int, cols)
	for c := range want {
		widths[c] = max(864, int(float64(textWidth)*want[c]/sum))
	}
	d.body.WriteString(`<w:tbl><w:tblPr><w:tblStyle w:val="TableGrid"/><w:tblW w:w="` + fmt.Sprint(sumInts(widths)) + `" w:type="dxa"/><w:tblLayout w:type="fixed"/><w:tblInd w:w="115" w:type="dxa"/><w:tblLook w:val="04A0" w:firstRow="1" w:lastRow="0" w:firstColumn="0" w:lastColumn="0" w:noHBand="0" w:noVBand="1"/></w:tblPr><w:tblGrid>`)
	for _, w := range widths {
		d.body.WriteString(fmt.Sprintf(`<w:gridCol w:w="%d"/>`, w))
	}
	d.body.WriteString("</w:tblGrid>")
	row := func(cells [][]Run, head bool) {
		d.body.WriteString("<w:tr>")
		if head {
			d.body.WriteString("<w:trPr><w:tblHeader/></w:trPr>")
		}
		for c := 0; c < cols; c++ {
			var rs []Run
			if c < len(cells) {
				rs = cells[c]
			}
			shade := ""
			if head {
				shade = `<w:shd w:val="clear" w:color="auto" w:fill="F0F2F5"/>`
			}
			jc := ""
			if c < len(b.Align) && b.Align[c] != "" {
				jc = `<w:jc w:val="` + map[string]string{"left": "left", "right": "right", "center": "center"}[b.Align[c]] + `"/>`
			}
			extra := ""
			if head {
				extra = "<w:b/>"
			}
			d.body.WriteString(fmt.Sprintf(`<w:tc><w:tcPr><w:tcW w:w="%d" w:type="dxa"/>%s</w:tcPr><w:p><w:pPr><w:spacing w:before="0" w:after="0"/>%s</w:pPr>%s</w:p></w:tc>`, widths[c], shade, jc, d.runs(rs, extra)))
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

const docxStyles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:eastAsia="Calibri" w:cs="Calibri"/><w:sz w:val="22"/><w:szCs w:val="22"/><w:lang w:val="en-US"/></w:rPr></w:rPrDefault><w:pPrDefault><w:pPr><w:spacing w:after="120" w:line="276" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults>
<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/><w:qFormat/></w:style>
<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:keepNext/><w:spacing w:before="240" w:after="120"/><w:outlineLvl w:val="0"/></w:pPr><w:rPr><w:b/><w:sz w:val="32"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:keepNext/><w:spacing w:before="200" w:after="100"/><w:outlineLvl w:val="1"/></w:pPr><w:rPr><w:b/><w:sz w:val="27"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading3"><w:name w:val="heading 3"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:keepNext/><w:spacing w:before="160" w:after="80"/><w:outlineLvl w:val="2"/></w:pPr><w:rPr><w:b/><w:sz w:val="24"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading4"><w:name w:val="heading 4"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:keepNext/><w:spacing w:before="120" w:after="60"/><w:outlineLvl w:val="3"/></w:pPr><w:rPr><w:b/><w:i/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="ListParagraph"><w:name w:val="List Paragraph"/><w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:spacing w:after="60"/><w:ind w:left="720"/><w:contextualSpacing/></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="Code"><w:name w:val="Code"/><w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:shd w:val="clear" w:color="auto" w:fill="F4F5F7"/><w:spacing w:after="120" w:line="240" w:lineRule="auto"/></w:pPr><w:rPr><w:rFonts w:ascii="Consolas" w:hAnsi="Consolas" w:cs="Consolas"/><w:sz w:val="18"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Quote"><w:name w:val="Quote"/><w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:pBdr><w:left w:val="single" w:sz="18" w:space="8" w:color="BFBFBF"/></w:pBdr><w:ind w:left="360"/></w:pPr><w:rPr><w:color w:val="555555"/></w:rPr></w:style>
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

func renderDOCX(blocks []Block, pics map[string]*picture) ([]byte, error) {
	d := &docxw{pics: pics, ids: map[string]int{}}
	d.blocks(blocks, "", 0)
	doc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"><w:body>` +
		d.body.String() +
		`<w:sectPr><w:pgSz w:w="12240" w:h="15840"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" w:header="720" w:footer="720" w:gutter="0"/></w:sectPr></w:body></w:document>`
	var rels strings.Builder
	rels.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering" Target="numbering.xml"/>`)
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
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Default Extension="png" ContentType="image/png"/><Default Extension="jpeg" ContentType="image/jpeg"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/><Override PartName="/word/numbering.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.numbering+xml"/><Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/></Relationships>`},
		{"word/document.xml", doc},
		{"word/styles.xml", docxStyles},
		{"word/numbering.xml", numberingXML(d.nums)},
		{"word/_rels/document.xml.rels", rels.String()},
		{"docProps/core.xml", core},
	}
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for _, p := range parts {
		f, err := z.Create(p.name)
		if err != nil {
			return nil, err
		}
		if _, err := f.Write([]byte(p.body)); err != nil {
			return nil, err
		}
	}
	for i, src := range d.media {
		pic := d.pics[src]
		// Deflated like the rest: LibreOffice refuses a document whose
		// stored entries carry a data descriptor, as Go's writer adds.
		f, err := z.Create(fmt.Sprintf("word/media/image%d.%s", i+1, pic.ext()))
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(pic.data); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
