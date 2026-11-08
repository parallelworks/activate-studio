package extract

import (
	"archive/zip"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/extract/pytext"
)

// The flat OOXML readers are enrich.py's _ooxml_text: the text of a docx,
// pptx or xlsx with its XML tags dropped, for a document downmark could
// not convert and for the files LibreOffice converts legacy formats to.
// They are rough on purpose (no numbers formatted, no shared strings
// resolved in place), and reproduce enrich.py's output exactly so the
// cache entries they write are the same.

var (
	docxMembers = regexp.MustCompile(`^word/document\.xml$`)
	pptxMembers = regexp.MustCompile(`^ppt/slides/slide[0-9]+\.xml$`)
	xlsxMembers = regexp.MustCompile(`^xl/(sharedStrings|worksheets/sheet[0-9]+)\.xml$`)
	xmlTag      = regexp.MustCompile(`<[^>]+>`)
	xmlEntities = []struct{ from, to string }{
		// In enrich.py's order: "&amp;lt;" becomes "<", as it does there.
		{"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"}, {"&quot;", `"`}, {"&apos;", "'"},
	}
)

// maxOOXMLMember bounds one member read into memory.
const maxOOXMLMember = 1 << 30

func ooxmlDocx(r io.ReaderAt, size int64) string {
	return ooxmlText(r, size, docxMembers, []string{"</w:p>", "</w:tr>", "</w:tc>"})
}

func ooxmlPptx(r io.ReaderAt, size int64) string {
	return ooxmlText(r, size, pptxMembers, []string{"</a:p>", "</a:t>"})
}

func ooxmlXlsx(r io.ReaderAt, size int64) string {
	// Shared strings hold most cell text; sheet XML holds the rest.
	return ooxmlText(r, size, xlsxMembers, []string{"</si>", "</row>", "</c>"})
}

func ooxmlText(r io.ReaderAt, size int64, members *regexp.Regexp, breaks []string) (out string) {
	defer func() {
		if recover() != nil {
			out = ""
		}
	}()
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return ""
	}
	// Python's namelist() keeps duplicates and z.read(name) reads the last
	// entry of a name, so a repeated name reads that entry once per copy.
	last := map[string]*zip.File{}
	var names []string
	for _, f := range zr.File {
		if members.MatchString(f.Name) {
			names = append(names, f.Name)
			last[f.Name] = f
		}
	}
	sort.Strings(names)
	var parts []string
	for _, name := range names {
		f := last[name]
		if f.Flags&0x1 != 0 || f.UncompressedSize64 > maxOOXMLMember {
			return ""
		}
		rc, err := f.Open()
		if err != nil {
			return ""
		}
		b, err := io.ReadAll(io.LimitReader(rc, maxOOXMLMember+1))
		rc.Close()
		if err != nil || len(b) > maxOOXMLMember {
			return ""
		}
		xml := pytext.Decode(b)
		for _, tag := range breaks {
			xml = strings.ReplaceAll(xml, tag, "\n")
		}
		text := xmlTag.ReplaceAllString(xml, "")
		for _, e := range xmlEntities {
			text = strings.ReplaceAll(text, e.from, e.to)
		}
		var lines []string
		for _, line := range strings.Split(text, "\n") {
			if s := pytext.Strip(line); s != "" {
				lines = append(lines, s)
			}
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, "\n")
}
