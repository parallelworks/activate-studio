// Package docwrite renders what the assistant writes into binary office
// formats: Markdown into a PDF or a Word document, and rows (CSV, Markdown
// tables or JSON) into an Excel workbook. Models write text; these are the
// files people asked for.
package docwrite

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// Run is a stretch of inline text with one style.
type Run struct {
	Text                       string
	Bold, Italic, Code, Strike bool
	Link                       string
	Image                      string // an image's source, with its alt text as Text
	Sup                        bool   // raised and small: a footnote's number
}

// Block kinds.
const (
	KHeading = "h"
	KPara    = "p"
	KList    = "list"
	KTable   = "table"
	KCode    = "code"
	KQuote   = "quote"
	KRule    = "rule"
	KBreak   = "break" // a page break
	KImage   = "img"
)

// A task list item's box.
const (
	Unchecked int8 = 1
	Checked   int8 = 2
)

// Block is one block of a Markdown document.
type Block struct {
	Kind     string
	Level    int   // heading level
	Runs     []Run // heading, paragraph
	Ordered  bool  // list
	Start    int   // list
	Items    [][]Block
	Checks   []int8  // list: per item, Unchecked or Checked for a task, else 0
	Header   [][]Run // table
	Rows     [][][]Run
	Align    []string // table: "left", "right", "center" or ""
	Text     string   // code; an image's alt text
	Src      string   // image
	Children []Block  // quote
}

var md = goldmark.New(goldmark.WithExtensions(extension.GFM, extension.Footnote))

// Parse reads Markdown into blocks.
func Parse(src string) []Block {
	b := []byte(spacedImages(src))
	doc := md.Parser().Parse(text.NewReader(b))
	return blocksOf(doc, b)
}

func blocksOf(parent ast.Node, src []byte) []Block {
	var out []Block
	for n := parent.FirstChild(); n != nil; n = n.NextSibling() {
		switch v := n.(type) {
		case *ast.Heading:
			out = append(out, Block{Kind: KHeading, Level: v.Level, Runs: textOnly(runsOf(v, src))})
		case *ast.Paragraph, *ast.TextBlock:
			runs := runsOf(v, src)
			if len(runs) == 1 && pageBreakText.MatchString(runs[0].Text) {
				out = append(out, Block{Kind: KBreak})
				continue
			}
			out = append(out, splitImages(runs)...)
		case *ast.List:
			l := Block{Kind: KList, Ordered: v.IsOrdered(), Start: v.Start}
			if l.Start == 0 {
				l.Start = 1
			}
			tasks := false
			for it := v.FirstChild(); it != nil; it = it.NextSibling() {
				l.Items = append(l.Items, blocksOf(it, src))
				check := taskBox(it)
				if k := len(l.Items) - 1; check != 0 && len(l.Items[k]) > 0 && len(l.Items[k][0].Runs) > 0 {
					first := &l.Items[k][0].Runs[0]
					first.Text = strings.TrimLeft(first.Text, " ")
				}
				tasks = tasks || check != 0
				l.Checks = append(l.Checks, check)
			}
			if !tasks {
				l.Checks = nil
			}
			out = append(out, l)
		case *east.FootnoteList:
			// The notes, in the order they are first referred to, as a
			// numbered list under a heading at the end of the document.
			l := Block{Kind: KList, Ordered: true, Start: 1}
			for it := v.FirstChild(); it != nil; it = it.NextSibling() {
				l.Items = append(l.Items, blocksOf(it, src))
			}
			if len(l.Items) > 0 {
				out = append(out, Block{Kind: KHeading, Level: 2, Runs: []Run{{Text: NotesHeading}}}, l)
			}
		case *ast.FencedCodeBlock, *ast.CodeBlock, *ast.HTMLBlock:
			var sb strings.Builder
			lines := v.Lines()
			for i := 0; i < lines.Len(); i++ {
				seg := lines.At(i)
				sb.Write(seg.Value(src))
			}
			t := strings.TrimRight(sb.String(), "\n")
			if _, html := v.(*ast.HTMLBlock); html {
				if pageBreakHTML.MatchString(t) {
					out = append(out, Block{Kind: KBreak})
					continue
				}
				if t = stripTags(t); strings.TrimSpace(t) != "" {
					out = append(out, Block{Kind: KPara, Runs: []Run{{Text: strings.TrimSpace(t)}}})
				}
				continue
			}
			out = append(out, Block{Kind: KCode, Text: t})
		case *ast.Blockquote:
			out = append(out, Block{Kind: KQuote, Children: blocksOf(v, src)})
		case *ast.ThematicBreak:
			out = append(out, Block{Kind: KRule})
		case *east.Table:
			t := Block{Kind: KTable}
			for _, a := range v.Alignments {
				switch a {
				case east.AlignLeft:
					t.Align = append(t.Align, "left")
				case east.AlignRight:
					t.Align = append(t.Align, "right")
				case east.AlignCenter:
					t.Align = append(t.Align, "center")
				default:
					t.Align = append(t.Align, "")
				}
			}
			for row := v.FirstChild(); row != nil; row = row.NextSibling() {
				var cells [][]Run
				for c := row.FirstChild(); c != nil; c = c.NextSibling() {
					cells = append(cells, textOnly(runsOf(c, src)))
				}
				if _, head := row.(*east.TableHeader); head {
					t.Header = cells
				} else {
					t.Rows = append(t.Rows, cells)
				}
			}
			out = append(out, t)
		default:
			out = append(out, blocksOf(n, src)...)
		}
	}
	return out
}

// NotesHeading is the heading over a document's footnotes.
const NotesHeading = "Notes"

// taskBox is a list item's box, when it is a task ("- [x] done").
func taskBox(item ast.Node) int8 {
	first := item.FirstChild()
	if first == nil {
		return 0
	}
	if box, ok := first.FirstChild().(*east.TaskCheckBox); ok {
		if box.IsChecked {
			return Checked
		}
		return Unchecked
	}
	return 0
}

func runsOf(n ast.Node, src []byte) []Run {
	var out []Run
	var walk func(n ast.Node, st Run)
	add := func(st Run, s string) {
		if s == "" {
			return
		}
		st.Text = ""
		if k := len(out) - 1; k >= 0 {
			p := out[k]
			p.Text = ""
			if p == st {
				out[k].Text += s
				return
			}
		}
		st.Text = s
		out = append(out, st)
	}
	walk = func(n ast.Node, st Run) {
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch v := c.(type) {
			case *ast.Text:
				add(st, string(v.Segment.Value(src)))
				if v.HardLineBreak() {
					add(st, "\n")
				} else if v.SoftLineBreak() {
					add(st, " ")
				}
			case *ast.String:
				add(st, string(v.Value))
			case *ast.CodeSpan:
				s := st
				s.Code = true
				var sb strings.Builder
				for t := v.FirstChild(); t != nil; t = t.NextSibling() {
					switch tv := t.(type) {
					case *ast.Text:
						sb.Write(tv.Segment.Value(src))
					case *ast.String:
						sb.Write(tv.Value)
					}
				}
				add(s, sb.String())
			case *ast.Emphasis:
				s := st
				if v.Level >= 2 {
					s.Bold = true
				} else {
					s.Italic = true
				}
				walk(v, s)
			case *east.Strikethrough:
				s := st
				s.Strike = true
				walk(v, s)
			case *ast.Link:
				s := st
				s.Link = externalLink(string(v.Destination))
				walk(v, s)
			case *ast.AutoLink:
				s := st
				s.Link = externalLink(string(v.URL(src)))
				add(s, string(v.Label(src)))
			case *ast.Image:
				var sb strings.Builder
				for t := v.FirstChild(); t != nil; t = t.NextSibling() {
					if tv, ok := t.(*ast.Text); ok {
						sb.Write(tv.Segment.Value(src))
					}
				}
				// Its own run, never merged: a paragraph is split around it.
				out = append(out, Run{Text: sb.String(), Image: string(v.Destination)})
			case *east.FootnoteLink:
				// Its own run, as an image is: a number set raised.
				out = append(out, Run{Text: strconv.Itoa(v.Index), Sup: true})
			case *east.FootnoteBacklink, *east.TaskCheckBox:
				// The way back from a note is a web page's; a task's box
				// is drawn by its list.
			case *ast.RawHTML:
				var sb strings.Builder
				for i := 0; i < v.Segments.Len(); i++ {
					seg := v.Segments.At(i)
					sb.Write(seg.Value(src))
				}
				if strings.HasPrefix(strings.ToLower(sb.String()), "<br") {
					add(st, "\n")
				}
			default:
				walk(c, st)
			}
		}
	}
	walk(n, Run{})
	return out
}

var (
	// A page break: \newpage or \pagebreak on a line of its own, or an
	// HTML comment or element that asks for one.
	pageBreakText = regexp.MustCompile(`^\s*\\(newpage|pagebreak|clearpage)\s*$`)
	pageBreakHTML = regexp.MustCompile(`(?i)<!--\s*page-?break\s*-->|page-break-(before|after)\s*:\s*always|break-(before|after)\s*:\s*page`)
	imageRef      = regexp.MustCompile(`!\[([^\]\n]*)\]\(((?:[^()<>\n]|\([^()<>\n]*\))*\s(?:[^()<>\n]|\([^()<>\n]*\))*)\)`)
	titled        = regexp.MustCompile(`^\S+\s+("[^"]*"|'[^']*')$`)
	fence         = regexp.MustCompile("^ {0,3}(```|~~~)")
)

// spacedImages lets an image's path hold spaces, as a screenshot's name
// does ("Screenshot 2026-11-13 at 9.01.28 AM.png"): Markdown ends a bare
// destination at the first space, so ![alt](a b.png) is not an image
// at all. Such a destination is wrapped in angle brackets, which Markdown
// takes whole. One with a title ("a.png \"Title\"") and text in code
// fences are left as written.
func spacedImages(src string) string {
	if !strings.Contains(src, "![") {
		return src
	}
	lines := strings.Split(src, "\n")
	inFence := false
	for i, l := range lines {
		if fence.MatchString(l) {
			inFence = !inFence
			continue
		}
		if inFence || !strings.Contains(l, "![") {
			continue
		}
		lines[i] = imageRef.ReplaceAllStringFunc(l, func(m string) string {
			sm := imageRef.FindStringSubmatch(m)
			dest := strings.TrimSpace(sm[2])
			if titled.MatchString(dest) {
				return m
			}
			return "![" + sm[1] + "](<" + dest + ">)"
		})
	}
	return strings.Join(lines, "\n")
}

// splitImages makes a paragraph's runs into blocks: its text, and each
// image as a block of its own where it stands. Whitespace left between
// images is dropped.
func splitImages(runs []Run) []Block {
	var out []Block
	var text []Run
	flush := func() {
		if strings.TrimSpace(Plain(text)) != "" {
			out = append(out, Block{Kind: KPara, Runs: text})
		}
		text = nil
	}
	for _, r := range runs {
		if r.Image != "" {
			flush()
			out = append(out, Block{Kind: KImage, Src: r.Image, Text: r.Text})
			continue
		}
		text = append(text, r)
	}
	flush()
	return out
}

// textOnly turns images into their alt text in brackets, where an image
// cannot stand (a heading, a table cell).
func textOnly(runs []Run) []Run {
	out := runs[:0:0]
	for _, r := range runs {
		if r.Image != "" {
			if r.Text == "" {
				continue
			}
			r = Run{Text: "[" + r.Text + "]"}
		}
		out = append(out, r)
	}
	return out
}

// Images lists the sources of the images in blocks, each once, in order.
func Images(blocks []Block) []string {
	var out []string
	seen := map[string]bool{}
	var walk func([]Block)
	walk = func(bs []Block) {
		for _, b := range bs {
			if b.Kind == KImage && !seen[b.Src] {
				seen[b.Src] = true
				out = append(out, b.Src)
			}
			for _, it := range b.Items {
				walk(it)
			}
			walk(b.Children)
		}
	}
	walk(blocks)
	return out
}

// externalLink keeps web and mail links; a Studio link (#open=...) or a
// relative path means nothing outside the Studio, so its text stands alone.
func externalLink(dest string) string {
	l := strings.ToLower(dest)
	if strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "mailto:") {
		return dest
	}
	return ""
}

func stripTags(s string) string {
	var sb strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// Plain is the runs' text.
func Plain(runs []Run) string {
	var sb strings.Builder
	for _, r := range runs {
		sb.WriteString(r.Text)
	}
	return sb.String()
}

// Title is the document's first heading, for the file's metadata.
func Title(blocks []Block) string {
	for _, b := range blocks {
		if b.Kind == KHeading {
			return strings.TrimSpace(Plain(b.Runs))
		}
	}
	return ""
}
