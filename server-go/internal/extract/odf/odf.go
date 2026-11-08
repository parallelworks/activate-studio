// Package odf extracts the text of OpenDocument files (.odt, .ods, .odp,
// .odg) for the indexer, in the layout of indexer/enrich.py's extract_odf:
// the package's content.xml in document order, paragraphs and headings one
// to a line, list items as their paragraphs, table rows as tab-separated
// cells, each spreadsheet table under a "Sheet: <name>" line, each slide
// or drawing page under "Slide <n>" (with its name when it has one of its
// own) followed by its speaker notes under "Notes:", and a text document's
// page headers and footers (styles.xml) first. Comments (office:annotation)
// are left out of paragraphs, as they are for Word documents.
//
// The reader streams the XML instead of building a tree of the whole
// document: content.xml is read twice, once to check that it is well
// formed (enrich.py's ElementTree parse rejects a document that is not,
// whatever text came before the fault) and to learn what kind of document
// it is, and once to emit lines, stopping as soon as the text reaches the
// limit. Only one paragraph or table cell at a time is held as a tree.
//
// Deliberate differences from enrich.py, all on documents no ODF writer
// produces: a text:c count that Python's int() rejects counts as one space
// (Python fails the whole file), a master page child without a namespace
// is ignored (Python fails the whole file), counts of more than a million
// spaces are capped there, attribute values have tabs and line breaks
// turned to spaces whether they were literal or character references
// (expat only does it for literal ones), and a content.xml or styles.xml
// larger than MaxPart is not read.
package odf

import (
	"archive/zip"
	"bufio"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"

	"github.com/parallelworks/activate-studio/server-go/internal/extract/pytext"
)

// MaxPart bounds the uncompressed size of content.xml and styles.xml.
// A spreadsheet of a million cells is a few hundred megabytes of XML.
const MaxPart = 2 << 30

// maxSpaces caps one text:s run. A line longer than the text limit is cut
// by the caller anyway, so the cap changes nothing a reader sees.
const maxSpaces = 1 << 20

var (
	// ErrNotODF means the input is not a zip package with a content.xml.
	ErrNotODF = errors.New("odf: not an OpenDocument package")
	// ErrMalformed means content.xml is not well-formed XML.
	ErrMalformed = errors.New("odf: content.xml is not well-formed XML")
)

const (
	nsOffice = "urn:oasis:names:tc:opendocument:xmlns:office:1.0"
	nsText   = "urn:oasis:names:tc:opendocument:xmlns:text:1.0"
	nsTable  = "urn:oasis:names:tc:opendocument:xmlns:table:1.0"
	nsDraw   = "urn:oasis:names:tc:opendocument:xmlns:drawing:1.0"
	nsPres   = "urn:oasis:names:tc:opendocument:xmlns:presentation:1.0"
	nsStyle  = "urn:oasis:names:tc:opendocument:xmlns:style:1.0"
)

// Text returns the document's text. It stops after the line that brings
// the text to maxText characters or more (code points, one more for each
// line break), as enrich.py does; maxText <= 0 means no limit. The caller
// cuts the result to its limit. It never panics; on input it cannot read
// it returns "" and an error.
func Text(r io.ReaderAt, size int64, maxText int) (text string, err error) {
	defer func() {
		if p := recover(); p != nil {
			text, err = "", fmt.Errorf("odf: %v", p)
		}
	}()
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return "", ErrNotODF
	}
	content := member(zr, "content.xml")
	if content == nil {
		return "", ErrNotODF
	}
	kind, err := scan(content)
	if err != nil {
		return "", err
	}
	w := &walker{max: maxText, spreadsheet: kind.spreadsheet}
	if kind.text {
		if styles := member(zr, "styles.xml"); styles != nil {
			w.headers(styles)
		}
	}
	if err := w.run(content, nil); err != nil {
		return "", err
	}
	return strings.Join(w.out, "\n"), nil
}

// member is the zip entry name, the last one when the name repeats (as
// Python's zipfile reads it), or nil.
func member(zr *zip.Reader, name string) *zip.File {
	var f *zip.File
	for _, e := range zr.File {
		if e.Name == name {
			f = e
		}
	}
	return f
}

// open returns a decoder over a zip member, bounded by MaxPart.
func open(f *zip.File) (*xml.Decoder, io.Closer, error) {
	if f.Flags&0x1 != 0 {
		return nil, nil, errors.New("odf: encrypted member")
	}
	rc, err := f.Open()
	if err != nil {
		return nil, nil, err
	}
	br := bufio.NewReader(&limitedReader{r: rc, n: MaxPart})
	// expat skips a UTF-8 byte order mark; encoding/xml does not.
	if b, err := br.Peek(3); err == nil && string(b) == "\xef\xbb\xbf" {
		br.Discard(3)
	}
	d := xml.NewDecoder(br)
	d.Strict = true
	d.CharsetReader = charsetReader
	return d, rc, nil
}

// limitedReader fails, where io.LimitReader would end quietly, once more
// than n bytes have been read, so a part over the bound is an error and
// not a truncated document.
type limitedReader struct {
	r io.Reader
	n int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.n -= int64(n)
	if l.n < 0 {
		return n, errors.New("odf: part too large")
	}
	return n, err
}

// charsetReader takes the encodings expat reads on its own (UTF-8 needs
// no reader): UTF-16, ISO-8859-1 and US-ASCII.
func charsetReader(label string, in io.Reader) (io.Reader, error) {
	switch strings.ToLower(label) {
	case "utf-16":
		return unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder().Reader(in), nil
	case "utf-16le":
		return unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder().Reader(in), nil
	case "utf-16be":
		return unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM).NewDecoder().Reader(in), nil
	case "iso-8859-1", "latin-1", "latin1":
		return charmap.ISO8859_1.NewDecoder().Reader(in), nil
	case "us-ascii", "ascii":
		return in, nil
	}
	return nil, fmt.Errorf("odf: unsupported encoding %q", label)
}

// tokens yields the element and text tokens of d, dropping comments,
// processing instructions and directives as ElementTree does.
func next(d *xml.Decoder) (xml.Token, error) {
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch tok.(type) {
		case xml.StartElement, xml.EndElement, xml.CharData:
			return tok, nil
		}
	}
}

type docKind struct{ text, spreadsheet bool }

// scan reads content.xml through once: whether it is well formed, and
// whether an office:text or office:spreadsheet element is in it below
// the root (ElementTree's root.find('.//...')).
func scan(f *zip.File) (docKind, error) {
	var k docKind
	d, c, err := open(f)
	if err != nil {
		return k, ErrNotODF
	}
	defer c.Close()
	depth := 0
	sawRoot := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			if !sawRoot {
				return k, ErrMalformed
			}
			return k, nil
		}
		if err != nil {
			return k, ErrMalformed
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 && sawRoot {
				return k, ErrMalformed // a second root element
			}
			sawRoot = true
			if depth > 0 && t.Name.Space == nsOffice {
				switch t.Name.Local {
				case "text":
					k.text = true
				case "spreadsheet":
					k.spreadsheet = true
				}
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimLeft(string(t), " \t\r\n") != "" {
				return k, ErrMalformed // text outside the root
			}
		}
	}
}

// walker emits lines as enrich.py's walk() does.
type walker struct {
	max         int
	spreadsheet bool
	out         []string
	size        int
	slides      int
	stop        bool // the text reached the limit: every walk returns
}

func (w *walker) full() bool { return w.max > 0 && w.size >= w.max }

func (w *walker) emit(line string) {
	line = pytext.Strip(line)
	// LibreOffice's stand-ins for fields on slides and notes pages.
	switch line {
	case "<number>", "<date/time>", "<header>", "<footer>":
		return
	}
	if line != "" && !w.full() {
		w.out = append(w.out, line)
		w.size += pytext.Len(line) + 1
	}
}

// run walks the children of the root element of the part f. With parts
// set, it walks instead the children of each header and footer of each
// master page (styles.xml).
func (w *walker) run(f *zip.File, parts func(*xml.Decoder) error) error {
	d, c, err := open(f)
	if err != nil {
		return ErrNotODF
	}
	defer c.Close()
	for {
		tok, err := next(d)
		if err != nil {
			return ErrMalformed
		}
		if _, ok := tok.(xml.StartElement); ok {
			break
		}
	}
	if parts != nil {
		return parts(d)
	}
	err = w.walk(d)
	if w.stop {
		return nil // the rest of the part is not read; scan checked it
	}
	return err
}

// errStopped ends a pass over styles.xml once the text is full.
var errStopped = errors.New("odf: text limit reached")

// headers emits the page headers and footers of a text document's master
// pages, which come before its body. A styles.xml that is not well formed
// contributes nothing, as enrich.py ignores one ElementTree cannot parse,
// so the lines are kept only once the whole part has parsed.
func (w *walker) headers(f *zip.File) {
	saved := *w
	saved.out = append([]string(nil), w.out...)
	err := w.run(f, func(d *xml.Decoder) error {
		if err := w.stylesWalk(d); err != nil {
			return err
		}
		return trailer(d)
	})
	if err == errStopped {
		// The limit was reached inside the headers: the rest of the part
		// still has to parse for enrich.py to have used any of it.
		if wellFormed(f) {
			w.stop = true
			return
		}
		err = ErrMalformed
	}
	if err != nil {
		*w = saved
	}
}

// wellFormed reports whether the whole part parses as one document.
func wellFormed(f *zip.File) bool {
	d, c, err := open(f)
	if err != nil {
		return false
	}
	defer c.Close()
	for {
		tok, err := next(d)
		if err != nil {
			return false
		}
		if _, ok := tok.(xml.StartElement); ok {
			break
		}
	}
	if err := d.Skip(); err != nil {
		return false
	}
	return trailer(d) == nil
}

// trailer checks that nothing but whitespace follows the root element.
func trailer(d *xml.Decoder) error {
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return ErrMalformed
		case xml.CharData:
			if strings.TrimLeft(string(t), " \t\r\n") != "" {
				return ErrMalformed
			}
		}
	}
}

var headerParts = map[string]bool{
	"header": true, "footer": true, "header-left": true, "footer-left": true,
	"header-first": true, "footer-first": true,
}

// stylesWalk consumes the children of the current element through its
// end, walking the header and footer children of each master page in it
// (ElementTree's styles.iter('style:master-page')).
func (w *walker) stylesWalk(d *xml.Decoder) error {
	for {
		tok, err := next(d)
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.EndElement:
			return nil
		case xml.StartElement:
			if t.Name.Space == nsStyle && t.Name.Local == "master-page" {
				err = w.masterPage(d)
			} else {
				err = w.stylesWalk(d)
			}
			if err != nil {
				return err
			}
		}
	}
}

func (w *walker) masterPage(d *xml.Decoder) error {
	for {
		tok, err := next(d)
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.EndElement:
			return nil
		case xml.StartElement:
			if t.Name.Space != "" && headerParts[t.Name.Local] {
				if err := w.walk(d); err != nil {
					return err
				}
				if w.stop {
					return errStopped
				}
			} else if err := w.stylesWalk(d); err != nil {
				return err
			}
		}
	}
}

// walk consumes the children of the element whose start was just read,
// through its end, emitting lines.
func (w *walker) walk(d *xml.Decoder) error {
	for {
		tok, err := next(d)
		if err != nil {
			return err
		}
		var start xml.StartElement
		switch t := tok.(type) {
		case xml.EndElement:
			return nil
		case xml.CharData:
			continue
		case xml.StartElement:
			start = t
		}
		if w.full() {
			w.stop = true
			return nil
		}
		n := start.Name
		switch {
		case n.Space == nsText && (n.Local == "p" || n.Local == "h"):
			el, err := tree(d, start)
			if err != nil {
				return err
			}
			w.emit(inline(el))
		case n.Space == nsOffice && n.Local == "annotation":
			if err := d.Skip(); err != nil {
				return err
			}
		case n.Space == nsTable && n.Local == "table":
			if name := attr(start, nsTable, "name"); w.spreadsheet && name != "" {
				w.emit("Sheet: " + name)
			}
			if err := w.walk(d); err != nil || w.stop {
				return err
			}
		case n.Space == nsTable && n.Local == "table-row":
			cells, err := row(d)
			if err != nil {
				return err
			}
			for len(cells) > 0 && cells[len(cells)-1] == "" {
				cells = cells[:len(cells)-1]
			}
			w.emit(strings.Join(cells, "\t"))
		case n.Space == nsDraw && n.Local == "page":
			w.slides++
			at := len(w.out)
			w.emit(fmt.Sprintf("Slide %d", w.slides))
			err := w.walk(d)
			// A slide's own name, unless it is the default (page1) or
			// repeats the slide's first line, its title.
			name := attr(start, nsDraw, "name")
			if name != "" && !strings.HasPrefix(name, "page") && at < len(w.out) &&
				(len(w.out) <= at+1 || w.out[at+1] != name) {
				w.out[at] = fmt.Sprintf("Slide %d: %s", w.slides, name)
			}
			if err != nil || w.stop {
				return err
			}
		case n.Space == nsPres && n.Local == "notes":
			before := len(w.out)
			err := w.walk(d)
			if len(w.out) > before {
				w.out = append(w.out, "")
				copy(w.out[before+1:], w.out[before:])
				w.out[before] = "Notes:"
			}
			if err != nil || w.stop {
				return err
			}
		default:
			if err := w.walk(d); err != nil || w.stop {
				return err
			}
		}
	}
}

// attr is an attribute's value with literal tabs and line breaks turned
// to spaces, as expat normalizes attribute values.
func attr(t xml.StartElement, space, local string) string {
	for _, a := range t.Attr {
		if a.Name.Space == space && a.Name.Local == local {
			return strings.Map(func(r rune) rune {
				if r == '\t' || r == '\n' || r == '\r' {
					return ' '
				}
				return r
			}, a.Value)
		}
	}
	return ""
}

// row reads a table row through its end and returns its cells' text.
func row(d *xml.Decoder) ([]string, error) {
	var cells []string
	for {
		tok, err := next(d)
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.EndElement:
			return cells, nil
		case xml.StartElement:
			if t.Name.Space == nsTable && (t.Name.Local == "table-cell" || t.Name.Local == "covered-table-cell") {
				el, err := tree(d, t)
				if err != nil {
					return nil, err
				}
				cells = append(cells, cellText(el))
				continue
			}
			if err := d.Skip(); err != nil {
				return nil, err
			}
		}
	}
}

// node is an element in ElementTree's model: the text before its first
// child, its children, and the text after it (its tail).
type node struct {
	name     xml.Name
	start    xml.StartElement
	text     string
	tail     string
	children []*node
}

func (n *node) is(space, local string) bool {
	return n.name.Space == space && n.name.Local == local
}

// tree reads the element whose start was just read into a node.
func tree(d *xml.Decoder, start xml.StartElement) (*node, error) {
	root := &node{name: start.Name, start: start.Copy()}
	stack := []*node{root}
	var last *node // the child that just ended, whose tail text follows
	var text, tail strings.Builder
	flush := func() {
		if last != nil {
			last.tail += tail.String()
		} else {
			stack[len(stack)-1].text += text.String()
		}
		text.Reset()
		tail.Reset()
	}
	for {
		tok, err := next(d)
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.CharData:
			if last != nil {
				tail.Write(t)
			} else {
				text.Write(t)
			}
		case xml.StartElement:
			flush()
			n := &node{name: t.Name, start: t.Copy()}
			p := stack[len(stack)-1]
			p.children = append(p.children, n)
			stack = append(stack, n)
			last = nil
		case xml.EndElement:
			flush()
			last = stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return root, nil
			}
		}
	}
}

// inline is a paragraph's text: spaces, tabs and line breaks for their
// elements, and every other child's text in place, comments left out.
func inline(el *node) string {
	var b strings.Builder
	inlineTo(&b, el)
	return b.String()
}

func inlineTo(b *strings.Builder, el *node) {
	b.WriteString(el.text)
	for _, c := range el.children {
		switch {
		case c.is(nsText, "s"):
			k := 1
			if v := attr(c.start, nsText, "c"); v != "" {
				if n, ok := pytext.ParseInt(v); ok {
					k = n
				}
			}
			if k > 0 {
				b.WriteString(strings.Repeat(" ", min(k, maxSpaces)))
			}
		case c.is(nsText, "tab"):
			b.WriteByte('\t')
		case c.is(nsText, "line-break"):
			b.WriteByte('\n')
		case c.is(nsOffice, "annotation"):
		default:
			inlineTo(b, c)
		}
		b.WriteString(c.tail)
	}
}

// cellText is a cell's paragraphs, each stripped, joined by spaces: every
// paragraph or heading in the cell's subtree in document order, so a
// paragraph inside another (in a frame, or a comment) is there twice.
func cellText(cell *node) string {
	var parts []string
	var visit func(*node)
	visit = func(n *node) {
		if n.is(nsText, "p") || n.is(nsText, "h") {
			if t := pytext.Strip(inline(n)); t != "" {
				parts = append(parts, t)
			}
		}
		for _, c := range n.children {
			visit(c)
		}
	}
	visit(cell)
	return strings.Join(parts, " ")
}
