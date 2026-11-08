package extract

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// docx is a minimal Word document with one paragraph per line.
func docx(t testing.TB, lines ...string) []byte {
	t.Helper()
	var body strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&body, `<w:p><w:r><w:t>%s</w:t></w:r></w:p>`, l)
	}
	return zipOf(t, map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"_rels/.rels":         `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/document.xml":   `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body.String() + `</w:body></w:document>`,
	})
}

func zipOf(t testing.TB, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range sortedKeys(files) {
		w, _ := zw.Create(name)
		w.Write([]byte(files[name]))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sortedKeys(m map[string]string) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	for i := range ks {
		for j := i + 1; j < len(ks); j++ {
			if ks[j] < ks[i] {
				ks[i], ks[j] = ks[j], ks[i]
			}
		}
	}
	return ks
}

// pdfDoc is a PDF with one page per string, each shown in Helvetica.
func pdfDoc(pages ...string) []byte {
	var b bytes.Buffer
	var offs []int
	obj := func(s string) {
		offs = append(offs, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offs), s)
	}
	b.WriteString("%PDF-1.4\n")
	n := len(pages)
	kids := ""
	for i := range pages {
		kids += fmt.Sprintf("%d 0 R ", 4+2*i)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, n))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	for i, text := range pages {
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", 5+2*i))
		content := ""
		if text != "" {
			content = fmt.Sprintf("BT /F1 12 Tf 72 700 Td (%s) Tj ET", text)
		}
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offs)+1, xref)
	return b.Bytes()
}

// fakeTools writes stand-ins for the helpers: pdfinfo reports pages,
// pdftoppm writes one PNG per page it is asked for, tesseract prints a
// line naming the image and logs each call.
func fakeTools(t *testing.T, pages int) (Tools, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stand-ins")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	return Tools{
		Tesseract: write("tesseract", fmt.Sprintf(`echo "tesseract $1 $OMP_THREAD_LIMIT" >> %s
[ "$1" = --version ] && exit 0
echo "  ink read from $(basename "$1")  "
`, log)),
		Pdftoppm: write("pdftoppm", fmt.Sprintf(`echo "pdftoppm $*" >> %s
last=$6; for a in "$@"; do prefix=$a; done
i=1; while [ $i -le $last ]; do printf 'png' > "$prefix-$i.png"; i=$((i+1)); done
`, log)),
		Pdfinfo: write("pdfinfo", fmt.Sprintf("echo 'Title: x'\necho 'Pages:          %d'\n", pages)),
	}, log
}

func newX(t *testing.T, opt Options) *Extractor {
	t.Helper()
	if opt.Tools == nil {
		opt.Tools = &Tools{}
	}
	x, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func put(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDownmarkVersionMatchesNPM(t *testing.T) {
	b, err := os.ReadFile("../../../server/package.json")
	if err != nil {
		t.Skip("no server/package.json")
	}
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		t.Fatal(err)
	}
	want := pkg.Dependencies["@giraffesyo/downmark"]
	if got := DownmarkVersion(); got != want {
		t.Fatalf("downmark is %s in go.mod and %s in server/package.json: entries one writes would be reconverted by the other", got, want)
	}
}

func TestDownmarkEntryAndReuse(t *testing.T) {
	kb, cache := t.TempDir(), t.TempDir()
	put(t, filepath.Join(kb, "a.docx"), docx(t, "Hello", "World"))
	x := newX(t, Options{})
	r := x.Update(context.Background(), filepath.Join(kb, "a.docx"), "a.docx", cache)
	if r.Action != Written || !r.Converted {
		t.Fatalf("first pass: %+v", r)
	}
	entry := read(t, CacheFile(cache, "a.docx"))
	if want := "<!-- extracted by downmark " + x.Version() + " -->\n\n"; !strings.HasPrefix(entry, want) {
		t.Fatalf("entry starts %q", entry[:min(60, len(entry))])
	}
	if !strings.Contains(entry, "Hello") || IndexText(entry) == entry {
		t.Fatalf("entry %q", entry)
	}
	if r := x.Update(context.Background(), filepath.Join(kb, "a.docx"), "a.docx", cache); r.Action != Reused || r.Converted {
		t.Fatalf("second pass: %+v", r)
	}
	// An entry from another downmark is converted again.
	put(t, CacheFile(cache, "a.docx"), []byte("<!-- extracted by downmark 0.0.1 -->\n\nold"))
	if r := x.Update(context.Background(), filepath.Join(kb, "a.docx"), "a.docx", cache); !r.Converted {
		t.Fatalf("old version not reconverted: %+v", r)
	}
	// So is an entry another reader wrote, current or not.
	put(t, CacheFile(cache, "a.docx"), []byte("flat text"))
	if r := x.Update(context.Background(), filepath.Join(kb, "a.docx"), "a.docx", cache); !r.Converted {
		t.Fatalf("flat entry not reconverted: %+v", r)
	}
}

func TestNoOCRPDFEntries(t *testing.T) {
	kb, cache := t.TempDir(), t.TempDir()
	text := strings.Repeat("plenty of text on this page ", 10)
	put(t, filepath.Join(kb, "p.pdf"), pdfDoc(text))
	noOCR := newX(t, Options{OCR: false})
	if r := noOCR.Update(context.Background(), filepath.Join(kb, "p.pdf"), "p.pdf", cache); r.Action != Written {
		t.Fatalf("%+v", r)
	}
	if v := EntryVersion(CacheFile(cache, "p.pdf")); v != noOCR.Version()+" (no OCR)" {
		t.Fatalf("version %q", v)
	}
	// Still current without OCR...
	if r := noOCR.Update(context.Background(), filepath.Join(kb, "p.pdf"), "p.pdf", cache); r.Converted {
		t.Fatalf("reconverted without OCR: %+v", r)
	}
	// ...and converted again once OCR is on.
	tools, _ := fakeTools(t, 1)
	withOCR := newX(t, Options{OCR: true, Tools: &tools})
	if r := withOCR.Update(context.Background(), filepath.Join(kb, "p.pdf"), "p.pdf", cache); !r.Converted {
		t.Fatalf("not reconverted with OCR: %+v", r)
	}
	if v := EntryVersion(CacheFile(cache, "p.pdf")); v != withOCR.Version() {
		t.Fatalf("version %q", v)
	}
}

func TestFallbackWhenDownmarkFails(t *testing.T) {
	kb, cache := t.TempDir(), t.TempDir()
	// Broken XML: downmark rejects it, enrich.py's flat reader takes the
	// text from word/document.xml anyway.
	put(t, filepath.Join(kb, "bare.docx"), zipOf(t, map[string]string{
		"word/document.xml": `<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>Tom &amp;amp; Jerry</w:t></w:r></w:p><w:p><w:t>  two  </w:t></w:p></w:body>`,
	}))
	x := newX(t, Options{})
	r := x.Update(context.Background(), filepath.Join(kb, "bare.docx"), "bare.docx", cache)
	if r.Action != Written || r.Converted {
		t.Fatalf("%+v", r)
	}
	if got := read(t, CacheFile(cache, "bare.docx")); got != "Tom &amp; Jerry\ntwo" {
		t.Fatalf("flat text %q", got)
	}
}

func TestEmptyDocumentNotCached(t *testing.T) {
	kb, cache := t.TempDir(), t.TempDir()
	put(t, filepath.Join(kb, "e.odt"), zipOf(t, map[string]string{"content.xml": `<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"/>`}))
	put(t, filepath.Join(kb, "junk.ppt"), []byte("not ole"))
	x := newX(t, Options{})
	for _, rel := range []string{"e.odt", "junk.ppt"} {
		if r := x.Update(context.Background(), filepath.Join(kb, rel), rel, cache); r.Action != Failed {
			t.Errorf("%s: %+v", rel, r)
		}
		if _, err := os.Stat(CacheFile(cache, rel)); err == nil {
			t.Errorf("%s: an empty entry was written", rel)
		}
	}
}

func TestThinPDFRecheck(t *testing.T) {
	kb, cache := t.TempDir(), t.TempDir()
	tools, log := fakeTools(t, 2)
	// Two pages and a few words: thin (under 120 characters a page), and
	// no page paints an image, so downmark's OCR asks nothing.
	put(t, filepath.Join(kb, "thin.pdf"), pdfDoc("Header line", ""))
	x := newX(t, Options{OCR: true, Tools: &tools})
	r := x.Update(context.Background(), filepath.Join(kb, "thin.pdf"), "thin.pdf", cache)
	if r.Action != Written {
		t.Fatalf("%+v", r)
	}
	got := read(t, CacheFile(cache, "thin.pdf"))
	// downmark's Markdown ends in a newline, and enrich.py joins with
	// two more.
	want := "<!-- extracted by downmark " + x.Version() + " -->\n\nHeader line\n\n\n" + OCRHeader + "\nink read from page-1.png\n\nink read from page-2.png"
	if got != want {
		t.Fatalf("entry\n got %q\nwant %q", got, want)
	}
	calls := read(t, log)
	if !strings.Contains(calls, "pdftoppm -r 200 -f 1 -l 2 -png") || !strings.Contains(calls, "page-1.png 1") {
		t.Fatalf("calls:\n%s", calls)
	}
	// The OCR header marks the entry as read: a later pass leaves it.
	os.Remove(log)
	if r := x.Update(context.Background(), filepath.Join(kb, "thin.pdf"), "thin.pdf", cache); r.Action != Reused {
		t.Fatalf("%+v", r)
	}
	if b, err := os.ReadFile(log); err == nil && strings.Contains(string(b), "pdftoppm") {
		t.Fatalf("rendered again:\n%s", b)
	}
}

func TestPDFPageCount(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "three.pdf"), pdfDoc("a", "b", "c"))
	n, err := PDFPageCount(context.Background(), filepath.Join(dir, "three.pdf"))
	if err != nil || n != 3 {
		t.Fatalf("%d, %v", n, err)
	}
}

func TestImages(t *testing.T) {
	kb, cache := t.TempDir(), t.TempDir()
	tools, _ := fakeTools(t, 1)
	small := bytes.Repeat([]byte{1}, 100)
	big := bytes.Repeat([]byte{1}, MinImageBytes+1)
	put(t, filepath.Join(kb, "icon.png"), small)
	put(t, filepath.Join(kb, "uploads", "chat", "pasted.png"), small)
	put(t, filepath.Join(kb, "photo.jpg"), big)
	x := newX(t, Options{OCR: true, Tools: &tools})
	for rel, want := range map[string]string{
		"icon.png":                "", // too small: cached empty
		"uploads/chat/pasted.png": "Text found in image (OCR):\nink read from pasted.png",
		"photo.jpg":               "Text found in image (OCR):\nink read from photo.jpg",
	} {
		r := x.Update(context.Background(), filepath.Join(kb, filepath.FromSlash(rel)), rel, cache)
		if r.Action != Written {
			t.Errorf("%s: %+v", rel, r)
			continue
		}
		if got := read(t, CacheFile(cache, rel)); got != want {
			t.Errorf("%s: %q", rel, got)
		}
	}
	// With neither OCR nor captions an image is not read at all.
	off := newX(t, Options{})
	put(t, filepath.Join(kb, "other.png"), big)
	if r := off.Update(context.Background(), filepath.Join(kb, "other.png"), "other.png", cache); r.Action != Skipped {
		t.Errorf("%+v", r)
	}
}

func TestCaptionFailureNotCached(t *testing.T) {
	kb, cache := t.TempDir(), t.TempDir()
	put(t, filepath.Join(kb, "p.png"), bytes.Repeat([]byte{1}, MinImageBytes+1))
	x := newX(t, Options{Vision: Vision{Model: "m", Key: "k", BaseURL: "http://127.0.0.1:1"}})
	r := x.Update(context.Background(), filepath.Join(kb, "p.png"), "p.png", cache)
	if r.Action != Failed {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(CacheFile(cache, "p.png")); err == nil {
		t.Fatal("a failed caption was cached")
	}
	// An empty entry is not current while a vision model is configured.
	put(t, CacheFile(cache, "p.png"), nil)
	future := time.Now().Add(time.Hour)
	os.Chtimes(CacheFile(cache, "p.png"), future, future)
	if r := x.Update(context.Background(), filepath.Join(kb, "p.png"), "p.png", cache); r.Action == Reused {
		t.Fatalf("empty entry reused: %+v", r)
	}
}

func TestLockFilesSkipped(t *testing.T) {
	kb, cache := t.TempDir(), t.TempDir()
	put(t, filepath.Join(kb, "~$a.docx"), docx(t, "x"))
	put(t, filepath.Join(kb, ".~lock.a.odt#"), []byte("x"))
	x := newX(t, Options{})
	if r := x.Update(context.Background(), filepath.Join(kb, "~$a.docx"), "~$a.docx", cache); r.Action != Skipped {
		t.Fatalf("%+v", r)
	}
}

func TestExternal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell commands")
	}
	kb, cache := t.TempDir(), t.TempDir()
	put(t, filepath.Join(kb, "a.dwg"), []byte("DWG"))
	put(t, filepath.Join(kb, "b.cad"), []byte("CAD"))
	put(t, filepath.Join(kb, "c.bad"), []byte("x"))
	put(t, filepath.Join(kb, "d.slow"), []byte("x"))
	put(t, filepath.Join(kb, "e.none"), []byte("x"))
	x := newX(t, Options{External: []External{
		{Name: "dwg", Suffixes: []string{"DWG"}, Command: []string{"sh", "-c", `printf 'drawing\r\nof %s in %s' "$(cat "$1")" "$(basename "$PWD")"`, "sh", "{path}"}},
		{Name: "cad", Suffixes: []string{".cad"}, Command: []string{"cat"}},
		{Name: "bad", Suffixes: []string{".bad"}, Command: []string{"sh", "-c", "echo broken >&2; exit 3"}},
		{Name: "slow", Suffixes: []string{".slow"}, Command: []string{"sh", "-c", "sleep 30"}, TimeoutSeconds: 1},
		{Name: "none", Suffixes: []string{".none"}, Command: []string{"true"}},
	}})
	ctx := context.Background()
	if r := x.Update(ctx, filepath.Join(kb, "a.dwg"), "a.dwg", cache); r.Action != Written {
		t.Fatalf("%+v", r)
	}
	if got := read(t, CacheFile(cache, "a.dwg")); !strings.HasPrefix(got, "drawing\nof DWG in studio-extractor-") {
		t.Fatalf("%q", got)
	}
	if r := x.Update(ctx, filepath.Join(kb, "b.cad"), "b.cad", cache); r.Action != Written || read(t, CacheFile(cache, "b.cad")) != "CAD" {
		t.Fatalf("path appended: %+v", r)
	}
	if r := x.Update(ctx, filepath.Join(kb, "c.bad"), "c.bad", cache); r.Action != Failed || !strings.Contains(fmt.Sprint(r.Err), "broken") {
		t.Fatalf("%+v", r)
	}
	start := time.Now()
	if r := x.Update(ctx, filepath.Join(kb, "d.slow"), "d.slow", cache); r.Action != Failed || time.Since(start) > 10*time.Second {
		t.Fatalf("%+v after %s", r, time.Since(start))
	}
	if r := x.Update(ctx, filepath.Join(kb, "e.none"), "e.none", cache); r.Action != Failed {
		t.Fatalf("%+v", r)
	}
	if r := x.Update(ctx, filepath.Join(kb, "a.dwg"), "a.dwg", cache); r.Action != Reused {
		t.Fatalf("%+v", r)
	}
}

func TestExternalConfig(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	put(t, good, []byte(`{"extractors": [{"name": "dwg", "suffixes": [".dwg"], "command": ["dwg2txt", "{path}"], "timeoutSeconds": 60}]}`))
	ext, err := LoadExternal(good)
	if err != nil || len(ext) != 1 || ext[0].timeout() != time.Minute {
		t.Fatalf("%+v, %v", ext, err)
	}
	for _, bad := range []string{
		`{"extractors": [{"suffixes": [".dwg"], "command": []}]}`,
		`{"extractors": [{"suffixes": [], "command": ["x"]}]}`,
		`{"extractors": [{"suffixes": ["a/b"], "command": ["x"]}]}`,
		`{"extractors": [{"suffixes": [".x"], "command": ["x"], "timeoutSeconds": 99999}]}`,
		`{"extractors": [{"suffixes": [".x"], "command": ["x"], "shell": true}]}`,
	} {
		p := filepath.Join(dir, "bad.json")
		put(t, p, []byte(bad))
		if _, err := LoadExternal(p); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if _, err := New(Options{Tools: &Tools{}, External: []External{{Suffixes: []string{".md"}, Command: []string{"x"}}}}); err == nil {
		t.Error("a text suffix was given an extractor")
	}
	if _, err := New(Options{Tools: &Tools{}, External: []External{{Suffixes: []string{".x"}, Command: []string{"a"}}, {Suffixes: []string{"X"}, Command: []string{"b"}}}}); err == nil {
		t.Error("two extractors for one suffix")
	}
}

func TestPlan(t *testing.T) {
	kb := t.TempDir()
	for _, rel := range []string{"a.docx", "b.md", "c.unknown", "sub/d.pdf", "sub/deeper/e.odt", ".git/f.docx", "node_modules/g.pdf",
		".hidden/h.xls", "sub/.github/i.pdf", "img.png"} {
		put(t, filepath.Join(kb, filepath.FromSlash(rel)), []byte("x"))
	}
	if runtime.GOOS != "windows" {
		os.Symlink(filepath.Join(kb, "a.docx"), filepath.Join(kb, "link.docx"))
		os.Symlink(filepath.Join(kb, "sub"), filepath.Join(kb, "linkdir"))
	}
	x := newX(t, Options{})
	got, err := x.Plan(PassOptions{KBRoot: kb})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".hidden/h.xls", "a.docx", "img.png", "sub/d.pdf", "sub/deeper/e.odt"}
	if runtime.GOOS != "windows" {
		want = []string{".hidden/h.xls", "a.docx", "img.png", "link.docx", "sub/d.pdf", "sub/deeper/e.odt"}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("plan %v, want %v", got, want)
	}
	got, _ = x.Plan(PassOptions{KBRoot: kb, Subdir: "sub", NoRecurse: true})
	if strings.Join(got, ",") != "sub/d.pdf" {
		t.Fatalf("no-recurse plan %v", got)
	}
	if _, err := x.Plan(PassOptions{KBRoot: kb, Subdir: "../x"}); err == nil {
		t.Fatal("subdir escaped")
	}
	docs := newX(t, Options{NoDocuments: true})
	got, _ = docs.Plan(PassOptions{KBRoot: kb})
	if strings.Join(got, ",") != "img.png" {
		t.Fatalf("no-documents plan %v", got)
	}
}

func TestRunSummary(t *testing.T) {
	kb, cache := t.TempDir(), t.TempDir()
	put(t, filepath.Join(kb, "a.docx"), docx(t, "one"))
	// Not text and not a document: downmark (whose plain-text converter
	// takes anything that reads as text) and the flat reader both fail.
	put(t, filepath.Join(kb, "b.docx"), []byte("PK\x03\x04\x00\x00garbage\x00\x01\x02"))
	put(t, filepath.Join(kb, "c.ods"), zipOf(t, map[string]string{"content.xml": `<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"><office:body><office:text><text:p>cell</text:p></office:text></office:body></office:document-content>`}))
	x := newX(t, Options{Jobs: 2})
	var last [2]int
	s, err := x.Run(context.Background(), PassOptions{KBRoot: kb, CacheRoot: cache, Progress: func(d, n int) { last = [2]int{d, n} }})
	if err != nil {
		t.Fatal(err)
	}
	if s.Scanned != 2 || s.Converted != 1 || s.Failed != 1 || s.Files != 3 || s.Written != 2 || last != [2]int{3, 3} {
		t.Fatalf("%+v, progress %v", s, last)
	}
	s, _ = x.Run(context.Background(), PassOptions{KBRoot: kb, CacheRoot: cache})
	if s.Reused != 1 || s.Converted != 0 || s.Kinds[".ods"].Reused != 1 {
		t.Fatalf("second pass %+v", s)
	}
}

func TestOOXMLAgainstPython(t *testing.T) {
	enrich := os.Getenv("ENRICH_PY")
	if enrich == "" {
		t.Skip("ENRICH_PY not set")
	}
	dir := t.TempDir()
	cases := map[string]map[string]string{
		"a.docx": {"word/document.xml": "<w:p><w:r><w:t>a &amp;lt; b</w:t></w:r></w:p><w:tbl><w:tr><w:tc><w:p>x</w:p></w:tc><w:tc>y</w:tc></w:tr></w:tbl>\xff\r\n z \x1c"},
		"b.pptx": {"ppt/slides/slide10.xml": "<a:p><a:t>ten</a:t><a:t>more</a:t></a:p>", "ppt/slides/slide2.xml": "<a:p><a:t>two</a:t></a:p>", "ppt/slides/slideX.xml": "<a:t>no</a:t>"},
		"c.xlsx": {"xl/sharedStrings.xml": "<si><t>s1</t></si><si><t>s2</t></si>", "xl/worksheets/sheet1.xml": "<row><c><v>1</v></c><c><v>2.5</v></c></row>"},
	}
	for name, files := range cases {
		p := filepath.Join(dir, name)
		put(t, p, zipOf(t, files))
		script := `import importlib.util, sys, pathlib
spec = importlib.util.spec_from_file_location('enrich', sys.argv[1]); m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
f = {'.docx': m.extract_docx, '.pptx': m.extract_pptx, '.xlsx': m.extract_xlsx}[pathlib.Path(sys.argv[2]).suffix]
sys.stdout.buffer.write(f(pathlib.Path(sys.argv[2])).encode())`
		out, err := exec.Command("python3", "-I", "-c", script, enrich, p).Output()
		if err != nil {
			t.Fatal(err)
		}
		f, _ := os.Open(p)
		st, _ := f.Stat()
		var got string
		switch filepath.Ext(name) {
		case ".docx":
			got = ooxmlDocx(f, st.Size())
		case ".pptx":
			got = ooxmlPptx(f, st.Size())
		default:
			got = ooxmlXlsx(f, st.Size())
		}
		f.Close()
		if got != string(out) {
			t.Errorf("%s:\n    go %q\npython %q", name, got, out)
		}
	}
}

// TestWindowsLineEnds: entries enrich.py wrote on Windows end lines in
// "\r\n"; they stay current and read as "\n".
func TestWindowsLineEnds(t *testing.T) {
	kb, cache := t.TempDir(), t.TempDir()
	put(t, filepath.Join(kb, "a.docx"), docx(t, "Hello"))
	put(t, filepath.Join(kb, "b.odt"), []byte("x"))
	x := newX(t, Options{})
	crlf := "<!-- extracted by downmark " + x.Version() + " -->\r\n\r\nHello\r\nWorld\r\n"
	put(t, CacheFile(cache, "a.docx"), []byte(crlf))
	put(t, CacheFile(cache, "b.odt"), []byte("plain\r\ntext"))
	for _, rel := range []string{"a.docx", "b.odt"} {
		if r := x.Update(context.Background(), filepath.Join(kb, rel), rel, cache); r.Action != Reused {
			t.Errorf("%s: %+v", rel, r)
		}
	}
	got, err := ReadEntry(CacheFile(cache, "a.docx"))
	if err != nil || got != strings.ReplaceAll(crlf, "\r\n", "\n") {
		t.Fatalf("ReadEntry %q %v", got, err)
	}
	if IndexText(crlf) != "Hello\nWorld\n" || IndexText(got) != "Hello\nWorld\n" {
		t.Fatalf("IndexText %q", IndexText(crlf))
	}
	if read(t, CacheFile(cache, "a.docx")) != crlf {
		t.Fatal("a reused entry was rewritten")
	}
}

// TestReaderText: with no pdftotext, a PDF downmark did not convert is
// read by the built-in reader, as enrich.py reads it with pypdf, unless
// --pdf-reader pdftotext asks for pdftotext only.
func TestReaderText(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.pdf")
	put(t, p, pdfDoc("first page words", "second page words"))
	x := newX(t, Options{})
	got := x.pdfText(context.Background(), p)
	if !strings.Contains(got, "first page words") || !strings.Contains(got, "second page words") {
		t.Fatalf("auto: %q", got)
	}
	if got := newX(t, Options{PDFReader: "pdftotext"}).pdfText(context.Background(), p); got != "" {
		t.Fatalf("pdftotext only: %q", got)
	}
	if got := newX(t, Options{PDFReader: "pypdf"}).pdfText(context.Background(), p); !strings.Contains(got, "second page words") {
		t.Fatalf("pypdf: %q", got)
	}
}
