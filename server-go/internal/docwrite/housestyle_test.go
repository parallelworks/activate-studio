package docwrite

import (
	"bytes"
	"context"
	"strings"
	"testing"

	gpdf "github.com/giraffesyo/pdf"
)

func TestLeaderPhrasesAreItalicWithAColon(t *testing.T) {
	cases := []struct {
		md, label, rest string
	}{
		{"**Why now.** The contract ends in March, and the work takes two months.", "Why now:", " The contract ends in March"},
		{"**Why now:** The contract ends in March.", "Why now:", " The contract ends in March."},
		{"**Why now**: The contract ends in March.", "Why now:", " The contract ends in March."},
		{"*Why now*. The contract ends in March.", "Why now:", " The contract ends in March."},
		{"- **Phase 1, discovery and dependency analysis.** Three weeks with the site's engineers.", "Phase 1, discovery and dependency analysis:", " Three weeks"},
		{"> **Impact Level posture:** The platform holds a provisional authorization.", "Impact Level posture:", " The platform"},
	}
	for _, c := range cases {
		bs := Parse(c.md)
		if n := italicLeaders(bs); n != 1 {
			t.Errorf("%q: %d labels set", c.md, n)
			continue
		}
		var rs []Run
		switch b := bs[0]; b.Kind {
		case KList:
			rs = b.Items[0][0].Runs
		case KQuote:
			rs = b.Children[0].Runs
		default:
			rs = b.Runs
		}
		if rs[0].Text != c.label || !rs[0].Italic || rs[0].Bold || !strings.HasPrefix(rs[1].Text, c.rest) || rs[1].Italic || rs[1].Bold {
			t.Errorf("%q: runs %+v", c.md, rs)
		}
	}
	// Left as written: keys and values, lines of them, numbers, captions,
	// a bold sentence, a label alone, and emphasis inside a sentence.
	for _, md := range []string{
		"**Date:** 14 November 2026",
		"**Prepared for:** Program Office  \n**Date:** 14 November 2026",
		"**1.** The first step is to read the logs on each node.",
		"**Figure 2.** The platform's boundary and the services inside it.",
		"*Table 1:* Figures for the year, by quarter and by site.",
		"**This is a bold sentence that runs on for far too many words to be any kind of label at all.** And more.",
		"**Summary**",
		"The **budget** is set. It holds for the year.",
		"**Note: read this first.** The file is large.",
	} {
		bs := Parse(md)
		before := Plain(bs[0].Runs)
		if n := italicLeaders(bs); n != 0 || Plain(bs[0].Runs) != before {
			t.Errorf("%q was changed: %+v", md, bs[0].Runs)
		}
	}
	// leaders: as-written keeps them.
	parts, _ := docxParts(t, "---\nleaders: as-written\n---\n**Why now.** The contract ends in March.\n")
	if doc := parts["word/document.xml"]; !strings.Contains(doc, "<w:b/>") || strings.Contains(doc, "Why now:") {
		t.Errorf("as-written: %s", doc)
	}
	parts, _ = docxParts(t, "**Why now.** The contract ends in March.\n")
	if doc := parts["word/document.xml"]; !strings.Contains(doc, `<w:r><w:rPr><w:i/></w:rPr><w:t xml:space="preserve">Why now:</w:t></w:r>`) {
		t.Errorf("italic: %s", doc)
	}
}

const covered = "---\ncover: true\nfooter: Internal\n---\n# Site Survey\n\n## Building 4\n\nPrepared for the facilities office.\n\n\\newpage\n\n# Summary\n\nThe survey found two air handlers past their service life.\n\n\\newpage\n\n## Plan\n\nReplace them in March.\n"

func TestCoverPageIsNotNumbered(t *testing.T) {
	parts, r := docxParts(t, covered)
	doc := parts["word/document.xml"]
	if strings.Count(doc, "<w:sectPr>") != 2 || !strings.Contains(doc, `<w:footerReference w:type="default" r:id="rIdFtrCover"/>`) {
		t.Fatalf("no cover section: %s", doc)
	}
	cover, body, _ := strings.Cut(doc, "</w:sectPr>")
	if !strings.Contains(cover, `<w:pStyle w:val="Title"/><w:jc w:val="center"/>`) || !strings.Contains(cover, `<w:pStyle w:val="Subtitle"/><w:jc w:val="center"/>`) || strings.Contains(cover, "Heading1") || strings.Contains(cover, `w:type="page"`) {
		t.Errorf("cover: %s", cover)
	}
	if !strings.Contains(body, `<w:pgNumType w:start="1"/></w:sectPr>`) || !strings.Contains(body, "Heading1") || strings.Count(body, `w:type="page"`) != 1 {
		t.Errorf("body: %s", body)
	}
	if f := parts["word/footer2.xml"]; !strings.Contains(f, "Internal") || strings.Contains(f, "PAGE") {
		t.Errorf("cover footer: %s", f)
	}
	if f := parts["word/footer1.xml"]; !strings.Contains(f, "Internal") || !strings.Contains(f, "PAGE") {
		t.Errorf("footer: %s", f)
	}
	if !strings.Contains(parts["[Content_Types].xml"], "/word/footer2.xml") || !strings.Contains(r.Note, "with a cover page") {
		t.Errorf("parts or note: %q", r.Note)
	}

	pdf, err := Render(".pdf", covered, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pdf.Note != `a PDF of a cover page and 2 pages numbered from 1, made from the Markdown (with a cover page, footer "Internal")` {
		t.Errorf("note %q", pdf.Note)
	}
	d, err := gpdf.Extract(context.Background(), bytes.NewReader(pdf.Data), int64(len(pdf.Data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Pages) != 3 {
		t.Fatalf("%d pages", len(d.Pages))
	}
	foot := func(i int) string {
		f := strings.Fields(d.Pages[i].Text())
		return f[len(f)-1]
	}
	if got := []string{foot(0), foot(1), foot(2)}; got[0] != "Internal" || got[1] != "1" || got[2] != "2" {
		t.Errorf("page feet %q", got)
	}

	// Without a page break there is no cover, and the author is told.
	out, _ := Render(".docx", "---\ncover: true\n---\n# Title\n\nText.\n", nil)
	if len(out.Problems) != 1 || !strings.Contains(out.Problems[0], "no cover") || strings.Contains(out.Note, "cover") {
		t.Errorf("problems %q, note %q", out.Problems, out.Note)
	}
}

func TestNoDividerLinesAndBlackText(t *testing.T) {
	parts, _ := docxParts(t, "# One\n\nText.\n\n---\n\n# Two\n\n> A quote.\n\n[a link](https://example.org)\n")
	for name, part := range parts {
		for _, bad := range []string{"<w:pBdr><w:bottom", "808080", "555555", "1F4E9E"} {
			if strings.Contains(part, bad) {
				t.Errorf("%s has %s", name, bad)
			}
		}
	}
	if !strings.Contains(parts["word/styles.xml"], `<w:style w:type="character" w:styleId="Hyperlink"><w:name w:val="Hyperlink"/><w:rPr><w:color w:val="000000"/><w:u w:val="single"/>`) {
		t.Error("a link is not black and underlined")
	}
	pdf, err := Render(".pdf", "# One\n\nText.\n\n---\n\n# Two\n\nMore.\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	// fpdf draws a line with the l operator after an m; the page has none.
	if bytes.Contains(pdf.Data, []byte(" l\n")) || bytes.Contains(pdf.Data, []byte(" l S")) {
		t.Error("the PDF draws a line")
	}
}

func TestStyleCheckCounts(t *testing.T) {
	md := "---\nfont: Arial\n---\n# Plan\n\nThe platform is ready — it runs today. It is truly fast and really simple, rather than slow. We chose Slurm rather than a new scheduler.\n\nIt is operating infrastructure, not a design. The real-time feed and real world tests pass -- each night.\n\nEach site keeps its own index and answers queries from it locally.\n\n## Again\n\nEach site keeps its own index and answers queries from it locally.\n\n```\na -- b — c truly\n```\n\n| a | b |\n|---|---|\n| 1 | 2 |\n"
	got := strings.Join(StyleCheck(md), "\n")
	for _, want := range []string{
		"a dash used as punctuation (— or –)",
		"a double hyphen (--)",
		`intensifiers: "truly" 1, "really" 1`,
		`"rather than" 2 times`,
		`an "X, not Y" contrast`,
		`1 sentence written twice or more, such as "Each site keeps its own index and answers queries from it locally"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"real"`) || strings.Contains(got, "words, the longest") {
		t.Errorf("counted what it should not:\n%s", got)
	}
	if out := StyleCheck("# Plain\n\nThe index holds the file names. A query reads it.\n"); len(out) != 0 {
		t.Errorf("plain text: %q", out)
	}
	long := "# Report\n\n## Background\n\n" + strings.Repeat("word ", 300) + "\n\n## Findings\n\n" + strings.Repeat("word ", 900) + "\n"
	if out := strings.Join(StyleCheck(long), ""); !strings.Contains(out, `1200 words, the longest section "Findings" at 900`) {
		t.Errorf("length: %q", out)
	}
	if r, _ := Render(".pdf", md, nil); len(r.Style) == 0 {
		t.Error("a rendered document carries no style advice")
	}
}
