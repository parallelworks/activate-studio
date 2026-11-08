package kb

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/parallelworks/activate-studio/server-go/internal/settings"
)

// A readable preview of Word, PowerPoint and Excel files and their
// OpenDocument counterparts without LibreOffice (officePreview.ts): the
// text of each slide, paragraph, table and sheet, and the pictures, read
// from the zipped XML. Only the members a preview needs are inflated.

// OutlineSuffixes are the formats with an outline.
var OutlineSuffixes = set(".pptx", ".docx", ".xlsx", ".odp", ".odt", ".ods")

// officeImageTypes are the pictures a browser shows in an <img>.
var officeImageTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".bmp": "image/bmp",
}

const (
	maxOfficeFile  = 512 * 1024 * 1024
	maxOfficeXML   = 64 * 1024 * 1024
	maxOfficeMedia = 32 * 1024 * 1024
	maxSlides      = 300
	maxBlocks      = 5000
	maxSheets      = 12
	maxRows        = 200
	maxCols        = 30
)

/* ---- zip ---- */

type zipEntry struct {
	name           string
	method         uint16
	csize, usize   uint32
	offset         uint32
	encrypted      bool
}

type officeZip struct {
	f       *os.File
	entries map[string]zipEntry
	order   []string
}

func openZip(file string) (*officeZip, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	z, err := readCentral(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return z, nil
}

func readAtFull(f *os.File, buf []byte, off int64) {
	// As fs.readSync into a zeroed buffer: what is past the end stays 0.
	if off < 0 {
		return
	}
	n, _ := f.ReadAt(buf, off)
	for i := n; i < len(buf); i++ {
		buf[i] = 0
	}
}

func readCentral(f *os.File) (*officeZip, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	if size > maxOfficeFile {
		return nil, Error(413, "This file is too large to preview here; use Download.")
	}
	tailLen := min(size, 65_557)
	tail := make([]byte, tailLen)
	readAtFull(f, tail, size-tailLen)
	eocd := -1
	for i := int(tailLen) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:]) == 0x06054b50 {
			eocd = i
			break
		}
	}
	if eocd < 0 {
		return nil, Error(422, "This file is not a readable Office package.")
	}
	count := int(binary.LittleEndian.Uint16(tail[eocd+10:]))
	cdSize := binary.LittleEndian.Uint32(tail[eocd+12:])
	cdOffset := binary.LittleEndian.Uint32(tail[eocd+16:])
	if cdSize == 0xffffffff || cdOffset == 0xffffffff {
		return nil, Error(413, "This file is too large to preview here; use Download.")
	}
	cd := make([]byte, cdSize)
	readAtFull(f, cd, int64(cdOffset))
	z := &officeZip{f: f, entries: map[string]zipEntry{}}
	p := 0
	for i := 0; i < count && p+46 <= len(cd); i++ {
		if binary.LittleEndian.Uint32(cd[p:]) != 0x02014b50 {
			break
		}
		flags := binary.LittleEndian.Uint16(cd[p+8:])
		nameLen := int(binary.LittleEndian.Uint16(cd[p+28:]))
		extraLen := int(binary.LittleEndian.Uint16(cd[p+30:]))
		commentLen := int(binary.LittleEndian.Uint16(cd[p+32:]))
		end := min(p+46+nameLen, len(cd))
		raw := cd[p+46 : end]
		var name string
		if flags&0x800 != 0 {
			name = DecodeUTF8(raw)
		} else {
			name = latin1(raw)
		}
		if _, ok := z.entries[name]; !ok {
			z.order = append(z.order, name)
		}
		z.entries[name] = zipEntry{
			name: name, method: binary.LittleEndian.Uint16(cd[p+10:]),
			csize: binary.LittleEndian.Uint32(cd[p+20:]), usize: binary.LittleEndian.Uint32(cd[p+24:]),
			offset: binary.LittleEndian.Uint32(cd[p+42:]), encrypted: flags&1 == 1,
		}
		p += 46 + nameLen + extraLen + commentLen
	}
	return z, nil
}

func latin1(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

// read is a member's bytes, or nil when it is missing, encrypted, of a
// method this does not read, or larger than max.
func (z *officeZip) read(name string, max int) []byte {
	e, ok := z.entries[name]
	if !ok || e.encrypted || int64(e.usize) > int64(max) || (e.method != 0 && e.method != 8) {
		return nil
	}
	head := make([]byte, 30)
	readAtFull(z.f, head, int64(e.offset))
	if binary.LittleEndian.Uint32(head) != 0x04034b50 {
		return nil
	}
	start := int64(e.offset) + 30 + int64(binary.LittleEndian.Uint16(head[26:])) + int64(binary.LittleEndian.Uint16(head[28:]))
	raw := make([]byte, e.csize)
	readAtFull(z.f, raw, start)
	if e.method == 0 {
		return raw
	}
	r := flate.NewReader(bytes.NewReader(raw))
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, int64(max)+1))
	if err != nil || len(out) > max {
		return nil
	}
	return out
}

func (z *officeZip) text(name string) (string, bool) {
	b := z.read(name, maxOfficeXML)
	if b == nil {
		return "", false
	}
	return DecodeUTF8(b), true
}

func (z *officeZip) close() { z.f.Close() }

/* ---- XML ---- */

// xel is an element; kids are *xel or string.
type xel struct {
	name  string
	attrs map[string]string
	kids  []any
}

func (e *xel) attr(k string) (string, bool) { v, ok := e.attrs[k]; return v, ok }

// jsWS is JavaScript's \s.
const jsWS = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var (
	xmlToken = regexp.MustCompile(`<!--[\s\S]*?-->|<!\[CDATA\[([\s\S]*?)\]\]>|<\?[\s\S]*?\?>|<!(?:[^>"']|"[^"]*"|'[^']*')*>|<(/?)([A-Za-z_][\w:.-]*)((?:[` + jsWS + `]+[^` + jsWS + `=/>]+[` + jsWS + `]*=[` + jsWS + `]*(?:"[^"]*"|'[^']*'))*)[` + jsWS + `]*(/?)>|([^<]+)`)
	xmlAttr  = regexp.MustCompile(`([^` + jsWS + `=/>]+)[` + jsWS + `]*=[` + jsWS + `]*(?:"([^"]*)"|'([^']*)')`)
	xmlEnt   = regexp.MustCompile(`&(#x[0-9a-fA-F]+|#\d+|\w+);`)
)

var xmlEntities = map[string]string{"amp": "&", "lt": "<", "gt": ">", "quot": `"`, "apos": "'"}

func xmlDecode(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	return xmlEnt.ReplaceAllStringFunc(s, func(m string) string {
		e := m[1 : len(m)-1]
		if e[0] == '#' {
			var code uint64
			var err error
			if e[1] == 'x' {
				code, err = strconv.ParseUint(e[2:], 16, 64)
			} else {
				code, err = strconv.ParseUint(e[1:], 10, 64)
			}
			if err != nil || code > 0x10ffff {
				return m
			}
			if code >= 0xD800 && code <= 0xDFFF {
				return string(utf8.RuneError) // a lone surrogate in JavaScript
			}
			return string(rune(code))
		}
		if v, ok := xmlEntities[e]; ok {
			return v
		}
		return m
	})
}

func parseXML(xml string) *xel {
	root := &xel{name: "#root", attrs: map[string]string{}}
	stack := []*xel{root}
	for _, m := range xmlToken.FindAllStringSubmatchIndex(xml, -1) {
		top := stack[len(stack)-1]
		group := func(i int) (string, bool) {
			if m[2*i] < 0 {
				return "", false
			}
			return xml[m[2*i]:m[2*i+1]], true
		}
		if s, ok := group(1); ok {
			top.kids = append(top.kids, s)
			continue
		}
		if s, ok := group(6); ok {
			top.kids = append(top.kids, xmlDecode(s))
			continue
		}
		name, ok := group(3)
		if !ok || name == "" {
			continue
		}
		if slash, _ := group(2); slash == "/" {
			for i := len(stack) - 1; i > 0; i-- {
				if stack[i].name == name {
					stack = stack[:i]
					break
				}
			}
			continue
		}
		attrs := map[string]string{}
		rawAttrs, _ := group(4)
		for _, a := range xmlAttr.FindAllStringSubmatch(rawAttrs, -1) {
			v := a[2]
			if a[2] == "" && a[3] != "" {
				v = a[3]
			}
			attrs[a[1]] = xmlDecode(v)
		}
		el := &xel{name: name, attrs: attrs}
		top.kids = append(top.kids, el)
		if selfClose, _ := group(5); selfClose != "/" {
			stack = append(stack, el)
		}
	}
	return root
}

func kidsOf(e *xel) []*xel {
	out := []*xel{}
	for _, k := range e.kids {
		if el, ok := k.(*xel); ok {
			out = append(out, el)
		}
	}
	return out
}

// all is every descendant named name, in document order.
func all(e *xel, name string) []*xel {
	var out []*xel
	var walk func(*xel)
	walk = func(e *xel) {
		for _, k := range e.kids {
			el, ok := k.(*xel)
			if !ok {
				continue
			}
			if el.name == name {
				out = append(out, el)
			}
			walk(el)
		}
	}
	walk(e)
	return out
}

func first(e *xel, name string) *xel {
	var found *xel
	var walk func(*xel) bool
	walk = func(e *xel) bool {
		for _, k := range e.kids {
			el, ok := k.(*xel)
			if !ok {
				continue
			}
			if el.name == name {
				found = el
				return true
			}
			if walk(el) {
				return true
			}
		}
		return false
	}
	walk(e)
	return found
}

// strings of an element: its text children joined.
func ownText(e *xel) string {
	var b strings.Builder
	for _, k := range e.kids {
		if s, ok := k.(string); ok {
			b.WriteString(s)
		}
	}
	return b.String()
}

/* ---- relationships ---- */

func relsOf(z *officeZip, part string) map[string]string {
	dir := path.Dir(part)
	out := map[string]string{}
	xml, ok := z.text(path.Join(dir, "_rels", path.Base(part)+".rels"))
	if !ok {
		return out
	}
	for _, r := range all(parseXML(xml), "Relationship") {
		if r.attrs["TargetMode"] == "External" {
			continue
		}
		target := r.attrs["Target"]
		if strings.HasPrefix(target, "/") {
			out[r.attrs["Id"]] = target[1:]
		} else {
			out[r.attrs["Id"]] = posixNormalize(path.Join(dir, target))
		}
	}
	return out
}

// posixNormalize is path.posix.normalize of a joined path (path.Join has
// cleaned it; normalize differs only in keeping a trailing slash, which a
// member name does not have).
func posixNormalize(p string) string { return p }

func displayable(z *officeZip, member string) bool {
	if member == "" {
		return false
	}
	if _, ok := z.entries[member]; !ok {
		return false
	}
	_, ok := officeImageTypes[strings.ToLower(path.Ext(member))]
	return ok
}

/* ---- blocks ---- */

func hBlock(level float64, text string) *Obj { return O("t", "h", "level", level, "text", text) }
func pBlock(text string) *Obj               { return O("t", "p", "text", text) }
func tableBlock(rows [][]string) *Obj       { return O("t", "table", "rows", rows) }
func imgBlock(media string) *Obj            { return O("t", "img", "media", media) }

// jsNum is Number(v) || 0 for an attribute.
func jsNum(v string, ok bool, def float64) float64 {
	if !ok {
		return def
	}
	n := settings.JSNumber(v)
	if math.IsNaN(n) || n == 0 {
		return 0
	}
	return n
}

/* ---- PowerPoint ---- */

func drawingText(p *xel) string {
	var b strings.Builder
	var visit func(*xel)
	visit = func(el *xel) {
		for _, k := range el.kids {
			e, ok := k.(*xel)
			if !ok {
				continue
			}
			switch e.name {
			case "a:t":
				b.WriteString(ownText(e))
			case "a:br":
				b.WriteByte('\n')
			case "a:tab":
				b.WriteByte('\t')
			default:
				visit(e)
			}
		}
	}
	visit(p)
	return b.String()
}

func drawingTable(tbl *xel) [][]string {
	rows := [][]string{}
	for _, tr := range all(tbl, "a:tr") {
		row := []string{}
		for _, tc := range kidsOf(tr) {
			if tc.name != "a:tc" {
				continue
			}
			var ps []string
			for _, p := range all(tc, "a:p") {
				ps = append(ps, drawingText(p))
			}
			row = append(row, JSTrim(strings.Join(ps, "\n")))
		}
		rows = append(rows, row)
	}
	return rows
}

var slideNum = regexp.MustCompile(`\d+`)
var slidePart = regexp.MustCompile(`^ppt/slides/slide\d+\.xml$`)
var titlePh = regexp.MustCompile(`^(title|ctrTitle)$`)

func pptxSlides(z *officeZip) *Obj {
	var parts []string
	if pres, ok := z.text("ppt/presentation.xml"); ok {
		presRels := relsOf(z, "ppt/presentation.xml")
		for _, s := range all(parseXML(pres), "p:sldId") {
			if t := presRels[s.attrs["r:id"]]; t != "" {
				parts = append(parts, t)
			}
		}
	}
	if len(parts) == 0 {
		for _, n := range z.order {
			if slidePart.MatchString(n) {
				parts = append(parts, n)
			}
		}
		num := func(s string) float64 { return settings.JSNumber(slideNum.FindString(s)) }
		sort.SliceStable(parts, func(i, j int) bool { return num(parts[i]) < num(parts[j]) })
	}
	slides := []*Obj{}
	for i, part := range parts[:min(len(parts), maxSlides)] {
		xml, ok := z.text(part)
		if !ok {
			continue
		}
		doc := parseXML(xml)
		rels := relsOf(z, part)
		title := ""
		blocks := []*Obj{}
		var visit func(*xel)
		visit = func(el *xel) {
			for _, k := range kidsOf(el) {
				switch k.name {
				case "p:sp":
					ph := first(k, "p:ph")
					isTitle := ph != nil && titlePh.MatchString(ph.attrs["type"])
					type para struct {
						text  string
						level float64
					}
					var paras []para
					for _, p := range all(k, "a:p") {
						lvl, ok := "", false
						if pPr := first(p, "a:pPr"); pPr != nil {
							lvl, ok = pPr.attr("lvl")
						}
						t := JSTrim(drawingText(p))
						if t != "" {
							paras = append(paras, para{t, jsNum(lvl, ok, 0)})
						}
					}
					if isTitle && title == "" {
						texts := make([]string, len(paras))
						for i, p := range paras {
							texts[i] = p.text
						}
						title = strings.Join(texts, " ")
					} else {
						for _, p := range paras {
							blocks = append(blocks, O("t", "p", "text", p.text, "level", p.level))
						}
					}
				case "p:graphicFrame":
					if tbl := first(k, "a:tbl"); tbl != nil {
						blocks = append(blocks, tableBlock(drawingTable(tbl)))
					}
				case "p:pic":
					embed := ""
					if b := first(k, "a:blip"); b != nil {
						embed = b.attrs["r:embed"]
					}
					if media := rels[embed]; displayable(z, media) {
						blocks = append(blocks, imgBlock(media))
					}
				default:
					visit(k)
				}
			}
		}
		if tree := first(doc, "p:spTree"); tree != nil {
			visit(tree)
		}
		slides = append(slides, O("n", i+1, "title", title, "blocks", blocks))
	}
	return O("kind", "slides", "slides", slides, "truncated", len(parts) > maxSlides)
}

/* ---- Word ---- */

var (
	reTitle    = regexp.MustCompile(`(?i)^title$`)
	reSubtitle = regexp.MustCompile(`(?i)^subtitle$`)
	reHeading  = regexp.MustCompile(`(?i)heading[` + jsWS + `]*(\d)`)
)

func headingLevel(style string) float64 {
	if reTitle.MatchString(style) {
		return 1
	}
	if reSubtitle.MatchString(style) {
		return 2
	}
	if m := reHeading.FindStringSubmatch(style); m != nil {
		n, _ := strconv.Atoi(m[1])
		return float64(min(6, max(1, n)))
	}
	return 0
}

func docxBlocks(z *officeZip) (*Obj, error) {
	xml, ok := z.text("word/document.xml")
	if !ok {
		return nil, Error(422, "This file is not a readable Word document.")
	}
	rels := relsOf(z, "word/document.xml")
	body := first(parseXML(xml), "w:body")
	blocks := []*Obj{}
	paraText := func(p *xel, images *[]string) string {
		var b strings.Builder
		var visit func(*xel)
		visit = func(el *xel) {
			for _, k := range kidsOf(el) {
				switch k.name {
				case "w:t":
					b.WriteString(ownText(k))
				case "w:tab":
					b.WriteByte('\t')
				case "w:br", "w:cr":
					b.WriteByte('\n')
				case "w:pPr", "w:rPr", "w:instrText":
				case "a:blip", "v:imagedata":
					id, ok := k.attr("r:embed")
					if !ok {
						id = k.attrs["r:id"]
					}
					if media := rels[id]; images != nil && displayable(z, media) {
						*images = append(*images, media)
					}
				default:
					visit(k)
				}
			}
		}
		visit(p)
		return b.String()
	}
	var visit func(*xel)
	visit = func(el *xel) {
		for _, k := range kidsOf(el) {
			if len(blocks) >= maxBlocks {
				return
			}
			switch k.name {
			case "w:p":
				var images []string
				text := JSTrim(paraText(k, &images))
				pPr := first(k, "w:pPr")
				style := ""
				var numPr *xel
				if pPr != nil {
					if s := first(pPr, "w:pStyle"); s != nil {
						style = s.attrs["w:val"]
					}
					numPr = first(pPr, "w:numPr")
				}
				level := headingLevel(style)
				if text != "" {
					switch {
					case level != 0:
						blocks = append(blocks, hBlock(level, text))
					case numPr != nil:
						v, ok := "", false
						if il := first(numPr, "w:ilvl"); il != nil {
							v, ok = il.attr("w:val")
						}
						blocks = append(blocks, O("t", "p", "text", text, "list", true, "level", jsNum(v, ok, 0)))
					default:
						blocks = append(blocks, pBlock(text))
					}
				}
				for _, media := range images {
					blocks = append(blocks, imgBlock(media))
				}
			case "w:tbl":
				rows := [][]string{}
				for _, tr := range kidsOf(k) {
					if tr.name != "w:tr" {
						continue
					}
					row := []string{}
					for _, tc := range kidsOf(tr) {
						if tc.name != "w:tc" {
							continue
						}
						var ps []string
						for _, p := range all(tc, "w:p") {
							ps = append(ps, paraText(p, nil))
						}
						row = append(row, JSTrim(strings.Join(ps, "\n")))
					}
					rows = append(rows, row)
				}
				blocks = append(blocks, tableBlock(rows))
			default:
				visit(k)
			}
		}
	}
	if body != nil {
		visit(body)
	}
	return O("kind", "document", "blocks", blocks, "truncated", len(blocks) >= maxBlocks), nil
}

/* ---- Excel ---- */

var colLetters = regexp.MustCompile(`^[A-Z]+`)

func colIndex(ref string) int {
	n := 0
	for _, ch := range colLetters.FindString(ref) {
		n = n*26 + int(ch-64)
	}
	return n - 1
}

func trimRow(r []string) []string {
	e := len(r)
	for e > 0 && r[e-1] == "" {
		e--
	}
	return r[:e]
}

func xlsxSheets(z *officeZip) (*Obj, error) {
	wb, ok := z.text("xl/workbook.xml")
	if !ok {
		return nil, Error(422, "This file is not a readable Excel workbook.")
	}
	rels := relsOf(z, "xl/workbook.xml")
	var shared []string
	if sst, ok := z.text("xl/sharedStrings.xml"); ok {
		for _, si := range all(parseXML(sst), "si") {
			var b strings.Builder
			for _, t := range all(si, "t") {
				b.WriteString(ownText(t))
			}
			shared = append(shared, b.String())
		}
	}
	defs := all(parseXML(wb), "sheet")
	sheets := []*Obj{}
	for _, s := range defs[:min(len(defs), maxSheets)] {
		part := rels[s.attrs["r:id"]]
		if part == "" {
			continue
		}
		xml, ok := z.text(part)
		if !ok {
			continue
		}
		// Only the first rows are wanted: cut the XML after them.
		cut := xml
		truncated := false
		at := -1
		for i := 0; i <= maxRows; i++ {
			next := strings.Index(xml[at+1:], "</row>")
			if next < 0 {
				break
			}
			at = at + 1 + next
			if i == maxRows-1 {
				cut = xml[:at+6]
				truncated = strings.Contains(xml[at:], "<row")
				break
			}
		}
		rows := [][]string{}
		for _, r := range all(parseXML(cut), "row") {
			if len(rows) >= maxRows {
				truncated = true
				break
			}
			row := []string{}
			for _, c := range kidsOf(r) {
				if c.name != "c" {
					continue
				}
				col := len(row)
				if ref := c.attrs["r"]; ref != "" {
					col = colIndex(ref)
				}
				if col >= maxCols || col < 0 {
					continue
				}
				v := ""
				if ve := first(c, "v"); ve != nil {
					v = ownText(ve)
				}
				var value string
				switch c.attrs["t"] {
				case "s":
					n := settings.JSNumber(v)
					if n >= 0 && n == math.Trunc(n) && int(n) < len(shared) {
						value = shared[int(n)]
					}
				case "inlineStr":
					var b strings.Builder
					for _, t := range all(c, "t") {
						b.WriteString(ownText(t))
					}
					value = b.String()
				case "b":
					value = "FALSE"
					if v == "1" {
						value = "TRUE"
					}
				default:
					value = v
				}
				for len(row) < col {
					row = append(row, "")
				}
				if col < len(row) {
					row[col] = value
				} else {
					row = append(row, value)
				}
			}
			rows = append(rows, row)
		}
		for i := range rows {
			rows[i] = trimRow(rows[i])
		}
		name, ok := s.attr("name")
		if !ok {
			name = fmt.Sprintf("Sheet %d", len(sheets)+1)
		}
		sheets = append(sheets, O("name", name, "rows", rows, "truncated", truncated))
	}
	return O("kind", "sheets", "sheets", sheets, "truncated", len(defs) > maxSheets), nil
}

/* ---- OpenDocument ---- */

type odfError struct{}

func (odfError) Error() string { return "Invalid count value" }

func odfText(el *xel) string {
	var b strings.Builder
	for _, k := range el.kids {
		s, isText := k.(string)
		if isText {
			b.WriteString(s)
			continue
		}
		e := k.(*xel)
		switch e.name {
		case "text:s":
			v, ok := e.attr("text:c")
			n := 1.0
			if ok {
				n = settings.JSNumber(v)
				if math.IsNaN(n) || n == 0 {
					n = 1
				}
			}
			n = math.Min(n, 64)
			if n < 0 {
				// ' '.repeat of a negative count throws in Node.
				panic(odfError{})
			}
			b.WriteString(strings.Repeat(" ", int(n)))
		case "text:tab":
			b.WriteByte('\t')
		case "text:line-break":
			b.WriteByte('\n')
		case "office:annotation", "text:note":
		default:
			b.WriteString(odfText(e))
		}
	}
	return b.String()
}

func odfNum(e *xel, k string) float64 {
	v, ok := e.attr(k)
	if !ok {
		return 1
	}
	n := settings.JSNumber(v)
	if math.IsNaN(n) || n == 0 {
		return 1
	}
	return n
}

func odfTable(t *xel) [][]string {
	rows := [][]string{}
	for _, r := range all(t, "table:table-row") {
		if len(rows) >= maxRows {
			break
		}
		reps := math.Min(odfNum(r, "table:number-rows-repeated"), float64(maxRows-len(rows)))
		row := []string{}
		for _, c := range kidsOf(r) {
			if c.name != "table:table-cell" && c.name != "table:covered-table-cell" {
				continue
			}
			n := odfNum(c, "table:number-columns-repeated")
			var ps []string
			for _, p := range kidsOf(c) {
				if p.name == "text:p" || p.name == "text:h" {
					ps = append(ps, odfText(p))
				}
			}
			v := JSTrim(strings.Join(ps, "\n"))
			for i := 0.0; i < n && len(row) < maxCols; i++ {
				row = append(row, v)
			}
		}
		trimmed := trimRow(row)
		if len(trimmed) == 0 && reps > 1 {
			continue
		}
		for i := 0.0; i < reps; i++ {
			rows = append(rows, trimmed)
		}
	}
	for len(rows) > 0 && len(rows[len(rows)-1]) == 0 {
		rows = rows[:len(rows)-1]
	}
	return rows
}

func odfBlocks(z *officeZip, container *xel, blocks *[]*Obj) {
	for _, k := range kidsOf(container) {
		if len(*blocks) >= maxBlocks {
			return
		}
		switch k.name {
		case "text:h":
			if text := JSTrim(odfText(k)); text != "" {
				lvl := odfNum(k, "text:outline-level")
				*blocks = append(*blocks, hBlock(math.Min(6, lvl), text))
			}
		case "text:p":
			if text := JSTrim(odfText(k)); text != "" {
				*blocks = append(*blocks, pBlock(text))
			}
			for _, img := range all(k, "draw:image") {
				if media := img.attrs["xlink:href"]; displayable(z, media) {
					*blocks = append(*blocks, imgBlock(media))
				}
			}
		case "text:list":
			for _, item := range all(k, "text:list-item") {
				var ps []string
				for _, x := range kidsOf(item) {
					if x.name == "text:p" || x.name == "text:h" {
						ps = append(ps, odfText(x))
					}
				}
				if text := JSTrim(strings.Join(ps, " ")); text != "" {
					*blocks = append(*blocks, O("t", "p", "text", text, "list", true))
				}
			}
		case "table:table":
			*blocks = append(*blocks, tableBlock(odfTable(k)))
		case "draw:image":
			if media := k.attrs["xlink:href"]; displayable(z, media) {
				*blocks = append(*blocks, imgBlock(media))
			}
		default:
			odfBlocks(z, k, blocks)
		}
	}
}

func odfOutline(z *officeZip, suffix string) (*Obj, error) {
	xml, ok := z.text("content.xml")
	if !ok {
		return nil, Error(422, "This file is not a readable OpenDocument file.")
	}
	doc := parseXML(xml)
	switch suffix {
	case ".odp":
		pages := all(doc, "draw:page")
		slides := []*Obj{}
		for i, page := range pages[:min(len(pages), maxSlides)] {
			title := ""
			blocks := []*Obj{}
			for _, frame := range kidsOf(page) {
				if frame.attrs["presentation:class"] == "title" && title == "" {
					var ps []string
					for _, p := range all(frame, "text:p") {
						ps = append(ps, odfText(p))
					}
					title = JSTrim(strings.Join(ps, " "))
					continue
				}
				odfBlocks(z, frame, &blocks)
			}
			slides = append(slides, O("n", i+1, "title", title, "blocks", blocks))
		}
		return O("kind", "slides", "slides", slides, "truncated", len(pages) > maxSlides), nil
	case ".ods":
		tables := all(doc, "table:table")
		sheets := []*Obj{}
		for i, t := range tables[:min(len(tables), maxSheets)] {
			name, ok := t.attr("table:name")
			if !ok {
				name = fmt.Sprintf("Sheet %d", i+1)
			}
			sheets = append(sheets, O("name", name, "rows", odfTable(t), "truncated", false))
		}
		return O("kind", "sheets", "sheets", sheets, "truncated", len(tables) > maxSheets), nil
	}
	blocks := []*Obj{}
	if body := first(doc, "office:text"); body != nil {
		odfBlocks(z, body, &blocks)
	}
	return O("kind", "document", "blocks", blocks, "truncated", len(blocks) >= maxBlocks), nil
}

/* ---- entry points ---- */

// Office reads outlines and pictures, keeping the last 16 outlines by
// path, size and modification time.
type Office struct {
	mu    sync.Mutex
	cache map[string]*Obj
	order []string
}

// Outline is officeOutline.
func (o *Office) Outline(abs string) (out *Obj, err error) {
	suffix := strings.ToLower(ExtName(abs))
	if !OutlineSuffixes[suffix] {
		return nil, Error(415, "This file type has no preview here; its indexed text is shown instead.")
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%s\x00%d\x00%v", abs, st.Size(), mtimeMs(st))
	o.mu.Lock()
	hit := o.cache[key]
	o.mu.Unlock()
	if hit != nil {
		return hit, nil
	}
	z, err := openZip(abs)
	if err != nil {
		return nil, err
	}
	defer z.close()
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(odfError); !ok {
				panic(p)
			}
			out, err = nil, odfError{}
		}
	}()
	switch suffix {
	case ".pptx":
		out = pptxSlides(z)
	case ".docx":
		out, err = docxBlocks(z)
	case ".xlsx":
		out, err = xlsxSheets(z)
	default:
		out, err = odfOutline(z, suffix)
	}
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	if o.cache == nil {
		o.cache = map[string]*Obj{}
	}
	if _, ok := o.cache[key]; !ok {
		o.order = append(o.order, key)
	}
	o.cache[key] = out
	if len(o.cache) > 16 {
		delete(o.cache, o.order[0])
		o.order = o.order[1:]
	}
	o.mu.Unlock()
	return out, nil
}

var mediaMember = regexp.MustCompile(`^(ppt|word|xl)/media/[^/]+$|^Pictures/[^/]+$`)

// Media is officeMedia: a picture in an Office file, by its name in the
// package; only the pictures an outline can point at.
func (o *Office) Media(abs, member string) ([]byte, string, error) {
	suffix := strings.ToLower(ExtName(abs))
	if !OutlineSuffixes[suffix] {
		return nil, "", Error(415, "no pictures for this file type")
	}
	ext := strings.ToLower(path.Ext(member))
	typ, ok := officeImageTypes[ext]
	if !ok || !mediaMember.MatchString(member) {
		return nil, "", Error(400, "not a picture in this file")
	}
	z, err := openZip(abs)
	if err != nil {
		return nil, "", err
	}
	defer z.close()
	body := z.read(member, maxOfficeMedia)
	if body == nil {
		return nil, "", Error(404, "no such picture in this file")
	}
	return body, typ, nil
}
