package docwrite

import (
	"fmt"
	"strings"
)

// Rendered is a file made from text, with a line saying what was made.
type Rendered struct {
	Data []byte
	Note string
	// Missing lists the images that could not be embedded, each with why;
	// their alt text stands in their place.
	Missing []string
	// Problems lists what the author should know went wrong: front matter
	// options that were not understood, whose defaults stood; characters
	// the PDF's font does not have, which were replaced by a placeholder;
	// a table too wide for its page, whose words were broken; a footnote
	// nothing refers to, which was left out.
	Problems []string
	// Style is advice from a check of the writing (see StyleCheck), for the
	// author to weigh; the document is made either way.
	Style []string
}

// doc is a document's blocks: its cover, when it has one, and the rest.
type doc struct{ cover, body []Block }

func (d doc) all() []Block { return append(append([]Block(nil), d.cover...), d.body...) }

// document reads a PDF's or Word document's Markdown: its options, its
// blocks with the leading labels of paragraphs set (leaders.go), split
// into the cover and the rest when it has one, and what went wrong.
func document(content string) (doc, Options, []string) {
	o, md, problems := SplitFrontMatter(content)
	problems = append(problems, unusedNotes(md)...)
	blocks := Parse(md)
	if !o.AsWritten {
		italicLeaders(blocks)
	}
	if !o.Cover {
		return doc{body: blocks}, o, problems
	}
	for i, b := range blocks {
		if b.Kind != KBreak {
			continue
		}
		if i == 0 {
			break
		}
		return doc{cover: blocks[:i], body: blocks[i+1:]}, o, problems
	}
	o.Cover = false
	return doc{body: blocks}, o, append(problems, `cover: true makes what comes before the first \newpage the cover page, and nothing comes before one, so the document has no cover`)
}

// Formats lists the suffixes Render makes, for the tool's description.
var Formats = []string{".pdf", ".docx", ".xlsx"}

// Handles reports whether a path's suffix is one Render makes.
func Handles(suffix string) bool {
	for _, f := range Formats {
		if strings.EqualFold(suffix, f) {
			return true
		}
	}
	return false
}

// Unsupported says what to write instead for an office format Render does
// not make, or "" when the suffix is not one.
func Unsupported(suffix string) string {
	switch strings.ToLower(suffix) {
	case ".doc", ".rtf", ".odt":
		return "Write a .docx (Word) or .pdf instead; " + suffix + " files are not made here."
	case ".xls", ".ods":
		return "Write a .xlsx (Excel) instead; " + suffix + " files are not made here."
	case ".ppt", ".pptx", ".odp":
		return "Slides are not made here; write the deck as a .pdf or .docx, with a heading per slide."
	}
	return ""
}

// Render makes a .pdf or .docx from Markdown, with the images it refers
// to read through src, or a .xlsx from rows.
func Render(suffix, content string, src ImageSource) (*Rendered, error) {
	switch strings.ToLower(suffix) {
	case ".pdf":
		doc, o, problems := document(content)
		pics, missing := pictures(doc.all(), src)
		data, pages, unfit, err := renderPDF(doc, pics, o)
		if err != nil {
			return nil, fmt.Errorf("making the PDF: %v", err)
		}
		count := fmt.Sprintf("%d page%s", pages, plural(pages))
		if o.Cover {
			count = fmt.Sprintf("a cover page and %d page%s numbered from 1", pages-1, plural(pages-1))
		}
		return &Rendered{Data: data, Note: fmt.Sprintf("a PDF of %s, made from the Markdown%s%s", count, withImages(len(pics)), o.describe()), Missing: missing, Problems: append(problems, unfit...), Style: StyleCheck(content)}, nil
	case ".docx":
		doc, o, problems := document(content)
		pics, missing := pictures(doc.all(), src)
		data, crowded, err := renderDOCX(doc, pics, o)
		if err != nil {
			return nil, fmt.Errorf("making the Word document: %v", err)
		}
		return &Rendered{Data: data, Note: "a Word document made from the Markdown" + withImages(len(pics)) + o.describe(), Missing: missing, Problems: append(problems, crowded...), Style: StyleCheck(content)}, nil
	case ".xlsx":
		data, n, err := renderXLSX(content)
		if err != nil {
			return nil, fmt.Errorf("making the workbook: %v", err)
		}
		return &Rendered{Data: data, Note: fmt.Sprintf("an Excel workbook of %d sheet%s", n, plural(n))}, nil
	}
	return nil, fmt.Errorf("%s files are not made here", suffix)
}

func withImages(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(", with %d image%s", n, plural(n))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
