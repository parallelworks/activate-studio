package docwrite

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestSplitFrontMatter(t *testing.T) {
	o, body, problems := SplitFrontMatter("---\nalign: justify\norientation: Landscape\nfont: \"Times New Roman\"\nsize: 11pt\nmargin: 0.75in\ntitle: Ignored\n---\n# Report\n\nText.\n")
	if !o.Justify || !o.Landscape || !o.Serif || o.Font != "Times New Roman" || o.Size != 11 || o.Margin != 0.75 {
		t.Errorf("options = %+v", o)
	}
	if body != "# Report\n\nText.\n" {
		t.Errorf("body = %q", body)
	}
	if len(problems) != 0 {
		t.Errorf("problems = %v", problems)
	}

	o, _, problems = SplitFrontMatter("---\r\nalign: centre\r\nsize: 30\r\ncolour: red\r\nfont: Arial\r\n---\r\nText")
	if o.Justify || o.Size != 0 || o.Serif || o.Font != "Arial" {
		t.Errorf("options = %+v", o)
	}
	if len(problems) != 3 {
		t.Errorf("problems = %v", problems)
	}

	// Not front matter: no closing line, or a rule after a line of text.
	for _, src := range []string{"---\nalign: justify\n\nText", "---\nSome words\n---\nMore", "Text\n---\n"} {
		if o, body, _ := SplitFrontMatter(src); o != (Options{}) || body != src {
			t.Errorf("%q: options %+v body %q", src, o, body)
		}
	}

	for in, want := range map[string]bool{"serif": true, "Georgia": true, "sans-serif": false, "Calibri": false, "DejaVu Serif": true, "PT Sans": false} {
		if _, serif := fontChoice(in); serif != want {
			t.Errorf("fontChoice(%q) serif = %v", in, serif)
		}
	}
}

func TestPageBreak(t *testing.T) {
	bs := Parse("One\n\n\\newpage\n\nTwo\n\n<!-- pagebreak -->\n\nThree\n")
	kinds := []string{}
	for _, b := range bs {
		kinds = append(kinds, b.Kind)
	}
	if got := strings.Join(kinds, " "); got != "p break p break p" {
		t.Fatalf("kinds = %s", got)
	}
	r, err := Render(".pdf", "One\n\n\\newpage\n\nTwo\n\n\\pagebreak\n\nThree\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(r.Note, "a PDF of 3 pages") {
		t.Errorf("note %q", r.Note)
	}
	// A break at the top of a page adds no empty page.
	r, _ = Render(".pdf", "\\newpage\n\nOne\n", nil)
	if !strings.HasPrefix(r.Note, "a PDF of 1 page,") {
		t.Errorf("note %q", r.Note)
	}
}

const optioned = "---\nalign: justify\norientation: landscape\nfont: Times New Roman\nsize: 12\nmargin: 0.75\n---\n"

func TestPDFOptions(t *testing.T) {
	long := strings.Repeat("Justified text spreads each line to the right margin, with a **bold** word and a [link](https://example.org) among the plain ones. ", 40)
	r, err := Render(".pdf", optioned+sample+"\n"+long+"\n\nA_very_long_identifier_"+strings.Repeat("x", 300)+" ends.\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Note, "(justified, landscape, Times New Roman, 12 pt, 0.75 inch margins)") {
		t.Errorf("note %q", r.Note)
	}
	if !bytes.Contains(r.Data, []byte("/MediaBox [0 0 792.00 612.00]")) {
		t.Error("pages are not landscape Letter")
	}
	if os.Getenv("DOCWRITE_OUT") != "" {
		_ = os.WriteFile(os.Getenv("DOCWRITE_OUT")+"/options.pdf", r.Data, 0o644)
	}
}

func TestDOCXOptions(t *testing.T) {
	r, err := Render(".docx", optioned+sample+"\n\\newpage\n\nLast page.\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	parts := map[string]string{}
	z, err := zip.NewReader(bytes.NewReader(r.Data), int64(len(r.Data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range z.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		parts[f.Name] = string(b)
	}
	doc := parts["word/document.xml"]
	for _, want := range []string{`<w:jc w:val="both"/>`, `<w:pgSz w:w="15840" w:h="12240" w:orient="landscape"/>`, `w:top="1080"`, `<w:br w:type="page"/>`, `<w:footerReference w:type="default" r:id="rIdFtr"/>`} {
		if !strings.Contains(doc, want) {
			t.Errorf("document.xml lacks %s", want)
		}
	}
	if !strings.Contains(parts["word/styles.xml"], `w:ascii="Times New Roman"`) || !strings.Contains(parts["word/styles.xml"], `<w:sz w:val="24"/>`) {
		t.Error("styles.xml lacks the font or size")
	}
	if !strings.Contains(parts["word/footer1.xml"], " PAGE ") || !strings.Contains(parts["[Content_Types].xml"], "/word/footer1.xml") || !strings.Contains(parts["word/_rels/document.xml.rels"], "footer1.xml") {
		t.Error("page number footer missing")
	}
	// Headings are not justified.
	if strings.Contains(doc, `<w:pStyle w:val="Heading1"/><w:jc`) {
		t.Error("a heading is justified")
	}
	if os.Getenv("DOCWRITE_OUT") != "" {
		_ = os.WriteFile(os.Getenv("DOCWRITE_OUT")+"/options.docx", r.Data, 0o644)
	}
}
