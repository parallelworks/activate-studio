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
	// Problems lists front matter options that were not understood; their
	// defaults stood.
	Problems []string
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
		o, md, problems := SplitFrontMatter(content)
		blocks := Parse(md)
		pics, missing := pictures(blocks, src)
		data, pages, err := renderPDF(blocks, pics, o)
		if err != nil {
			return nil, fmt.Errorf("making the PDF: %v", err)
		}
		return &Rendered{data, fmt.Sprintf("a PDF of %d page%s, made from the Markdown%s%s", pages, plural(pages), withImages(len(pics)), o.describe()), missing, problems}, nil
	case ".docx":
		o, md, problems := SplitFrontMatter(content)
		blocks := Parse(md)
		pics, missing := pictures(blocks, src)
		data, err := renderDOCX(blocks, pics, o)
		if err != nil {
			return nil, fmt.Errorf("making the Word document: %v", err)
		}
		return &Rendered{data, "a Word document made from the Markdown" + withImages(len(pics)) + o.describe(), missing, problems}, nil
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
