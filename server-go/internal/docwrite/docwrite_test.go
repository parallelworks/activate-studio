package docwrite

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

const sample = "# Site survey summary\n\nThe **north** site needs *two* visits; see [the guide](https://example.org/guide) and [notes](#open=file:a.md).\n\n" +
	"## Findings\n\n- Access road ≥ 4 m wide\n- Power: `480 V` three phase\n  - backup generator\n\n1. Book the crane\n2. Confirm permits\n\n" +
	"| Item | Qty | Cost |\n| --- | ---: | ---: |\n| Steel beam, 6 m | 12 | 1,240.50 |\n| Anchor bolts | 200 | 310 |\n\n" +
	"> Survey data is preliminary.\n\n```\nlat,lon\n41.88,-87.63\n```\n\n---\n\nEnd.\n"

func TestParse(t *testing.T) {
	bs := Parse(sample)
	kinds := []string{}
	for _, b := range bs {
		kinds = append(kinds, b.Kind)
	}
	want := "h p h list list table quote code rule p"
	if got := strings.Join(kinds, " "); got != want {
		t.Fatalf("kinds = %s, want %s", got, want)
	}
	if Title(bs) != "Site survey summary" {
		t.Errorf("title = %q", Title(bs))
	}
	p := bs[1].Runs
	var bold, link, plain string
	for _, r := range p {
		if r.Bold {
			bold += r.Text
		}
		if r.Link != "" {
			link = r.Link
		}
		plain += r.Text
	}
	if bold != "north" || link != "https://example.org/guide" || !strings.Contains(plain, "and notes.") {
		t.Errorf("runs: bold %q link %q text %q", bold, link, plain)
	}
	if l := bs[3]; len(l.Items) != 2 || len(l.Items[1]) != 2 || l.Items[1][1].Kind != KList {
		t.Errorf("nested list not kept: %#v", l.Items)
	}
	if l := bs[4]; !l.Ordered || l.Start != 1 {
		t.Errorf("ordered list: %#v", l)
	}
	if tb := bs[5]; len(tb.Header) != 3 || len(tb.Rows) != 2 || tb.Align[1] != "right" {
		t.Errorf("table: %#v", tb)
	}
}

func TestPDF(t *testing.T) {
	r, err := Render(".pdf", sample+strings.Repeat("\nA long paragraph that fills the page with words. ", 120))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(r.Data, []byte("%PDF-")) {
		t.Fatalf("not a PDF: %q", r.Data[:8])
	}
	if !strings.Contains(r.Note, "page") {
		t.Errorf("note %q", r.Note)
	}
	if os.Getenv("DOCWRITE_OUT") != "" {
		_ = os.WriteFile(os.Getenv("DOCWRITE_OUT")+"/sample.pdf", r.Data, 0o644)
	}
}

func TestDOCX(t *testing.T) {
	r, err := Render(".docx", sample)
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(r.Data), int64(len(r.Data)))
	if err != nil {
		t.Fatal(err)
	}
	doc := ""
	for _, f := range z.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		// Every part is well formed XML.
		d := xml.NewDecoder(bytes.NewReader(b))
		for {
			if _, err := d.Token(); err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
		}
		if f.Name == "word/document.xml" {
			doc = string(b)
		}
	}
	for _, want := range []string{`<w:pStyle w:val="Heading1"/>`, "Site survey summary", `<w:hyperlink r:id="rId100">`, `<w:tblHeader/>`, `<w:numId w:val="2"/>`, "≥ 4 m"} {
		if !strings.Contains(doc, want) {
			t.Errorf("document.xml lacks %s", want)
		}
	}
	if os.Getenv("DOCWRITE_OUT") != "" {
		_ = os.WriteFile(os.Getenv("DOCWRITE_OUT")+"/sample.docx", r.Data, 0o644)
	}
}

func TestXLSX(t *testing.T) {
	cases := []struct {
		name, content string
		sheets        []string
		check         func(f *excelize.File)
	}{
		{"csv", "part,qty,code\nbolt,12,00123\nnut,\"1,500\",7\n", []string{"Sheet1"}, func(f *excelize.File) {
			v, _ := f.GetCellValue("Sheet1", "B3")
			if v != "1500" {
				t.Errorf("grouped number: %q", v)
			}
			if typ, _ := f.GetCellType("Sheet1", "C2"); typ == excelize.CellTypeNumber {
				t.Error("a code with a leading zero became a number")
			}
		}},
		{"markdown", "## Costs\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n## Costs\n\n| c |\n|---|\n| x |\n", []string{"Costs", "Costs (2)"}, nil},
		{"json", `{"sheets":[{"name":"Plan: Q1/Q2","rows":[["item","n"],["a",3],["total","=SUM(B2:B2)"]]}]}`, []string{"Plan  Q1 Q2"}, func(f *excelize.File) {
			if fm, _ := f.GetCellFormula("Plan  Q1 Q2", "B3"); fm != "SUM(B2:B2)" {
				t.Errorf("formula %q", fm)
			}
		}},
		{"objects", `[{"name":"a","count":2},{"name":"b","count":5}]`, []string{"Sheet1"}, func(f *excelize.File) {
			rows, _ := f.GetRows("Sheet1")
			if len(rows) != 3 || rows[0][0] != "count" || rows[0][1] != "name" {
				t.Errorf("rows %v", rows)
			}
		}},
	}
	for _, c := range cases {
		r, err := Render(".xlsx", c.content)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		f, err := excelize.OpenReader(bytes.NewReader(r.Data))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := f.GetSheetList(); strings.Join(got, "|") != strings.Join(c.sheets, "|") {
			t.Errorf("%s: sheets %v, want %v", c.name, got, c.sheets)
		}
		if c.check != nil {
			c.check(f)
		}
		f.Close()
	}
	if _, err := Render(".xlsx", `{"rows":1}`); err == nil {
		t.Error("a JSON object without sheets was accepted")
	}
}

func TestUnsupported(t *testing.T) {
	if Unsupported(".pptx") == "" || Unsupported(".xls") == "" || Unsupported(".md") != "" {
		t.Error("unsupported formats")
	}
	if !Handles(".PDF") || Handles(".md") {
		t.Error("handles")
	}
}
