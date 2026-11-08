package odf

import (
	"archive/zip"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const head = `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0" xmlns:draw="urn:oasis:names:tc:opendocument:xmlns:drawing:1.0" xmlns:presentation="urn:oasis:names:tc:opendocument:xmlns:presentation:1.0" xmlns:style="urn:oasis:names:tc:opendocument:xmlns:style:1.0"><office:body>`
const tail = `</office:body></office:document-content>`

const stylesHead = `<?xml version="1.0" encoding="UTF-8"?>
<office:document-styles xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" xmlns:style="urn:oasis:names:tc:opendocument:xmlns:style:1.0"><office:master-styles>`
const stylesTail = `</office:master-styles></office:document-styles>`

func pkg(t testing.TB, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"mimetype", "content.xml", "styles.xml"} {
		body, ok := files[name]
		if !ok {
			continue
		}
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type tcase struct {
	name    string
	files   map[string]string
	max     int
	want    string
	wantErr bool
}

// cases are checked against enrich.py's extract_odf by
// TestCasesAgainstPython; the wants are its output.
var cases = []tcase{
	{name: "paragraphs", files: map[string]string{"content.xml": head +
		`<office:text><text:h>Title</text:h><text:p>One<text:s/>two<text:s text:c="3"/>three<text:tab/>four<text:line-break/>five</text:p>` +
		`<text:p>  padded  </text:p><text:p/><text:p>with <office:annotation><text:p>a comment</text:p></office:annotation>tail</text:p>` +
		`<text:list><text:list-item><text:p>item <text:span>one</text:span></text:p></text:list-item></text:list></office:text>` + tail},
		want: "Title\nOne two   three\tfour\nfive\npadded\nwith tail\nitem one"},
	{name: "space counts", files: map[string]string{"content.xml": head +
		`<office:text><text:p>a<text:s text:c="0"/>b<text:s text:c="-2"/>c<text:s text:c=""/>d<text:s text:c=" 2 "/>e<text:s text:c="1_0"/>f</text:p></office:text>` + tail},
		want: "abc d  e          f"},
	{name: "table", files: map[string]string{"content.xml": head +
		`<office:text><table:table table:name="T1"><table:table-row><table:table-cell><text:p>a</text:p><text:p>b</text:p></table:table-cell>` +
		`<table:covered-table-cell/><table:table-cell><text:p>c</text:p></table:table-cell><table:table-cell/><table:table-cell/></table:table-row>` +
		`<table:table-row><table:table-cell/></table:table-row></table:table></office:text>` + tail},
		want: "a b\t\tc"},
	{name: "nested paragraph in cell twice", files: map[string]string{"content.xml": head +
		`<office:spreadsheet><table:table table:name="S"><table:table-row><table:table-cell><text:p>x<draw:frame><draw:text-box><text:p>inner</text:p></draw:text-box></draw:frame></text:p>` +
		`<office:annotation><text:p>note</text:p></office:annotation></table:table-cell></table:table-row></table:table></office:spreadsheet>` + tail},
		want: "Sheet: S\nxinner inner note"},
	{name: "spreadsheet", files: map[string]string{"content.xml": head +
		`<office:spreadsheet><table:table table:name="Sheet	One"><table:table-row><table:table-cell><text:p>1</text:p></table:table-cell><table:table-cell><text:p>2</text:p></table:table-cell></table:table-row></table:table>` +
		`<table:table table:name=""><table:table-row><table:table-cell><text:p>3</text:p></table:table-cell></table:table-row></table:table></office:spreadsheet>` + tail},
		want: "Sheet: Sheet One\n1\t2\n3"},
	{name: "slides", files: map[string]string{"content.xml": head +
		`<office:presentation><draw:page draw:name="page1"><draw:frame><draw:text-box><text:p>Title A</text:p></draw:text-box></draw:frame>` +
		`<presentation:notes><draw:frame><draw:text-box><text:p>speaker</text:p></draw:text-box></draw:frame></presentation:notes></draw:page>` +
		`<draw:page draw:name="Intro"><draw:frame><draw:text-box><text:p>Body</text:p><text:p>&lt;number&gt;</text:p></draw:text-box></draw:frame><presentation:notes/></draw:page>` +
		`<draw:page draw:name="Same"><draw:frame><draw:text-box><text:p>Same</text:p></draw:text-box></draw:frame></draw:page>` +
		`<draw:page draw:name="Empty"/></office:presentation>` + tail},
		want: "Slide 1\nTitle A\nNotes:\nspeaker\nSlide 2: Intro\nBody\nSlide 3\nSame\nSlide 4: Empty"},
	{name: "headers and footers", files: map[string]string{
		"content.xml": head + `<office:text><text:p>body</text:p></office:text>` + tail,
		"styles.xml": stylesHead + `<style:master-page style:name="Standard"><style:header><text:p>Head</text:p></style:header>` +
			`<style:footer-left><text:p>Left foot</text:p></style:footer-left><style:other><text:p>not this</text:p></style:other></style:master-page>` +
			`<style:master-page><style:footer><text:p>Foot 2</text:p></style:footer></style:master-page>` + stylesTail},
		want: "Head\nLeft foot\nFoot 2\nbody"},
	{name: "headers only for text documents", files: map[string]string{
		"content.xml": head + `<office:spreadsheet><table:table table:name="S"/></office:spreadsheet>` + tail,
		"styles.xml":  stylesHead + `<style:master-page><style:header><text:p>Head</text:p></style:header></style:master-page>` + stylesTail},
		want: "Sheet: S"},
	{name: "broken styles ignored", files: map[string]string{
		"content.xml": head + `<office:text><text:p>body</text:p></office:text>` + tail,
		"styles.xml":  stylesHead + `<style:master-page><style:header><text:p>Head</text:p></style:header></style:master-page>` + `</office:master-styles>`},
		want: "body"},
	{name: "malformed content", files: map[string]string{"content.xml": head + `<office:text><text:p>body</text:p></office:text>` + `</office:body>`},
		wantErr: true},
	{name: "second root", files: map[string]string{"content.xml": head + `<office:text/>` + tail + `<x/>`}, wantErr: true},
	{name: "no content", files: map[string]string{"styles.xml": stylesHead + stylesTail}, wantErr: true},
	{name: "entities and cdata", files: map[string]string{"content.xml": head +
		`<office:text><text:p>a &amp; b &#x263A; <![CDATA[<c>]]></text:p><!-- comment --><text:p>d<!-- x -->e</text:p></office:text>` + tail},
		want: "a & b \u263a <c>\nde"},
	{name: "carriage returns", files: map[string]string{"content.xml": head +
		"<office:text><text:p>a\r\nb\rc&#13;d</text:p></office:text>" + tail},
		want: "a\nb\nc\rd"},
	{name: "limit", max: 12, files: map[string]string{"content.xml": head +
		`<office:text><text:p>aaaaa</text:p><text:p>bbbbb</text:p><text:p>ccccc</text:p><text:p>ddddd</text:p></office:text>` + tail},
		want: "aaaaa\nbbbbb"},
	{name: "limit inside a slide keeps the name and notes", max: 20, files: map[string]string{"content.xml": head +
		`<office:presentation><draw:page draw:name="Named"><presentation:notes><text:p>n1 n1 n1 n1 n1 n1</text:p><text:p>n2</text:p></presentation:notes></draw:page>` +
		`<draw:page draw:name="Later"><text:p>never</text:p></draw:page></office:presentation>` + tail},
		want: "Slide 1: Named\nNotes:\nn1 n1 n1 n1 n1 n1"},
	{name: "limit inside the headers", max: 6, files: map[string]string{
		"content.xml": head + `<office:text><text:p>body</text:p></office:text>` + tail,
		"styles.xml":  stylesHead + `<style:master-page><style:header><text:p>Head1</text:p><text:p>Head2</text:p></style:header></style:master-page>` + stylesTail},
		want: "Head1"},
	{name: "byte order mark", files: map[string]string{"content.xml": "\ufeff" + head + `<office:text><text:p>bom</text:p></office:text>` + tail},
		want: "bom"},
	{name: "tracked deletions are read", files: map[string]string{"content.xml": head +
		`<office:text><text:tracked-changes><text:changed-region><text:deletion><text:p>gone</text:p></text:deletion></text:changed-region></text:tracked-changes><text:p>kept</text:p></office:text>` + tail},
		want: "gone\nkept"},
	{name: "drawing", files: map[string]string{"content.xml": head +
		`<office:drawing><draw:page draw:name="Seite1"><draw:custom-shape><text:p>shape text</text:p></draw:custom-shape></draw:page></office:drawing>` + tail},
		want: "Slide 1: Seite1\nshape text"},
	{name: "spreadsheet found after a named table", files: map[string]string{"content.xml": head +
		`<office:text><table:table table:name="T"><table:table-row><table:table-cell><text:p>x</text:p></table:table-cell></table:table-row></table:table></office:text><office:spreadsheet/>` + tail},
		want: "Sheet: T\nx"},
	{name: "text found after a slide", files: map[string]string{
		"content.xml": head + `<office:presentation><draw:page><text:p>body</text:p></draw:page></office:presentation><office:text/>` + tail,
		"styles.xml":  stylesHead + `<style:master-page><style:header><text:p>Head</text:p></style:header></style:master-page>` + stylesTail},
		want: "Head\nSlide 1\nbody"},
	{name: "text found inside notes", files: map[string]string{
		"content.xml": head + `<office:presentation><draw:page><presentation:notes><office:text><text:p>n</text:p></office:text></presentation:notes></draw:page></office:presentation>` + tail,
		"styles.xml":  stylesHead + `<style:master-page><style:footer><text:p>Foot</text:p></style:footer></style:master-page>` + stylesTail},
		want: "Foot\nSlide 1\nNotes:\nn"},
	{name: "spreadsheet found past the limit", max: 3, files: map[string]string{"content.xml": head +
		`<office:text><table:table table:name="T"><table:table-row><table:table-cell><text:p>aaaa</text:p></table:table-cell></table:table-row></table:table></office:text><office:spreadsheet/>` + tail},
		want: "Sheet: T"},
	{name: "malformed past the limit", max: 3, files: map[string]string{"content.xml": head +
		`<office:text><text:p>aaaa</text:p><text:p>b</text:p></office:text>` + tail + `<extra/>`},
		wantErr: true},
	{name: "text before the root", files: map[string]string{"content.xml": "junk" + head + `<office:text><text:p>a</text:p></office:text>` + tail},
		wantErr: true},
}

func run(t testing.TB, c tcase) (string, error) {
	b := pkg(t, c.files)
	max := c.max
	if max == 0 {
		max = 500_000
	}
	return Text(bytes.NewReader(b), int64(len(b)), max)
}

func TestCases(t *testing.T) {
	for _, c := range cases {
		got, err := run(t, c)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: no error, text %q", c.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestNotAZip(t *testing.T) {
	if _, err := Text(bytes.NewReader([]byte("not a zip")), 9, 0); err == nil {
		t.Fatal("no error")
	}
}

// TestCasesAgainstPython runs enrich.py's extract_odf on every case, with
// ENRICH_PY naming indexer/enrich.py, and checks its text equals Go's
// (the limit cases use enrich.py's own MAX_TEXT, patched per case).
func TestCasesAgainstPython(t *testing.T) {
	enrich := os.Getenv("ENRICH_PY")
	if enrich == "" {
		t.Skip("ENRICH_PY not set")
	}
	dir := t.TempDir()
	for i, c := range cases {
		p := filepath.Join(dir, "case.odt")
		if err := os.WriteFile(p, pkg(t, c.files), 0o644); err != nil {
			t.Fatal(err)
		}
		max := c.max
		if max == 0 {
			max = 500_000
		}
		script := `import importlib.util, sys
spec = importlib.util.spec_from_file_location('enrich', sys.argv[1]); m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
m.MAX_TEXT = int(sys.argv[3])
try:
    sys.stdout.buffer.write(('OK\n' + m.extract_odf(__import__('pathlib').Path(sys.argv[2]))).encode())
except Exception as e:
    sys.stdout.buffer.write(('RAISED ' + repr(e)).encode())`
		out, err := exec.Command("python3", "-I", "-c", script, enrich, p, strconv.Itoa(max)).Output()
		if err != nil {
			t.Fatalf("%s: python: %v", c.name, err)
		}
		py, ok := strings.CutPrefix(string(out), "OK\n")
		if !ok {
			t.Errorf("case %d %s: python %s", i, c.name, out)
			continue
		}
		got, gerr := run(t, c)
		if gerr != nil {
			got = "" // enrich.py's '' for a file it cannot read
		}
		if got != py {
			t.Errorf("case %d %s:\n    go %q\npython %q", i, c.name, got, py)
		}
	}
}

func FuzzText(f *testing.F) {
	for _, c := range cases {
		f.Add(pkg(f, c.files))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		Text(bytes.NewReader(b), int64(len(b)), 2000)
	})
}

// FuzzContent fuzzes content.xml inside a valid package, which reaches
// the walker far more often than fuzzing the zip bytes does.
func FuzzContent(f *testing.F) {
	for _, c := range cases {
		if s, ok := c.files["content.xml"]; ok {
			f.Add(s, c.files["styles.xml"])
		}
	}
	f.Fuzz(func(t *testing.T, content, styles string) {
		b := pkg(t, map[string]string{"content.xml": content, "styles.xml": styles})
		Text(bytes.NewReader(b), int64(len(b)), 300)
	})
}

// FuzzSinglePass checks the single pass against the two-pass reading
// (well-formedness and kind first), which follows enrich.py's order.
func FuzzSinglePass(f *testing.F) {
	for _, c := range cases {
		if s, ok := c.files["content.xml"]; ok {
			f.Add(s, c.files["styles.xml"], c.max)
		}
	}
	f.Fuzz(func(t *testing.T, content, styles string, max int) {
		files := map[string]string{"content.xml": content}
		if styles != "" {
			files["styles.xml"] = styles
		}
		b := pkg(t, files)
		max = max % 200
		one, err1 := Text(bytes.NewReader(b), int64(len(b)), max)
		twoPass = true
		two, err2 := Text(bytes.NewReader(b), int64(len(b)), max)
		twoPass = false
		if one != two || (err1 == nil) != (err2 == nil) {
			t.Fatalf("single %q %v\ntwo-pass %q %v", one, err1, two, err2)
		}
	})
}

func TestSinglePassCases(t *testing.T) {
	for _, c := range cases {
		one, err1 := run(t, c)
		twoPass = true
		two, err2 := run(t, c)
		twoPass = false
		if one != two || (err1 == nil) != (err2 == nil) {
			t.Errorf("%s: single %q %v, two-pass %q %v", c.name, one, err1, two, err2)
		}
	}
}
