package extract

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// todayTree is a knowledge base with every case that decides whether a
// file is read: dot folders (which preextract.ts leaves to enrich.py),
// folders both skip, lock files, symlinks, text, and each document
// format. Images are left out: with OCR off neither side reads them.
func todayTree(t *testing.T, kb string) {
	t.Helper()
	odt := zipOf(t, map[string]string{"content.xml": `<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"><office:body><office:text><text:p>open text</text:p></office:text></office:body></office:document-content>`})
	xls, err := os.ReadFile("xls/testdata/lo_cases.xls")
	if err != nil {
		t.Fatal(err)
	}
	ppt, err := os.ReadFile("ppt/testdata/basic_test_ppt_file.ppt")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"a.docx": docx(t, "top"), "sub/b.pdf": pdfDoc("page text that is long enough"), "sub/deep/c.xlsx": nil,
		".hidden/d.docx": docx(t, "hidden"), ".hidden/e.odt": odt, "sub/.dot/f.pdf": pdfDoc("dot pdf"),
		".hidden/g.xls": xls, ".hidden/.more/h.ppt": ppt, "i.ppt": ppt, "j.odt": odt,
		".git/k.docx": docx(t, "git"), "node_modules/l.docx": docx(t, "nm"),
		".github/n.docx": docx(t, "gh"), "build/o.docx": docx(t, "b"), "sub/__pycache__/p.docx": docx(t, "p"),
		"~$a.docx": []byte("lock"), ".~lock.a.docx#": []byte("lock"), "notes.md": []byte("# text"),
		"empty.docx": {}, "q.unknown": []byte("x"),
		// Dotfiles named like a suffix have none; "..pdf" has ".pdf".
		".pdf": pdfDoc("dotfile"), "sub/.docx": docx(t, "dotfile"), "r..pdf": pdfDoc("two dots pdf"), "s.odt.": odt,
	}
	files["sub/deep/c.xlsx"] = zipOf(t, map[string]string{
		"[Content_Types].xml":        `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`,
		"_rels/.rels":                `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
		"xl/workbook.xml":            `<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="S" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>cell</t></is></c></row></sheetData></worksheet>`,
	})
	for rel, b := range files {
		put(t, filepath.Join(kb, filepath.FromSlash(rel)), b)
	}
	if runtime.GOOS != "windows" {
		os.Symlink(filepath.Join(kb, "a.docx"), filepath.Join(kb, "link.docx"))
		os.Symlink(filepath.Join(kb, ".hidden"), filepath.Join(kb, "linkdir"))
	}
}

func entries(t *testing.T, cache string) []string {
	t.Helper()
	var got []string
	filepath.WalkDir(cache, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".txt") {
			rel, _ := filepath.Rel(cache, p)
			got = append(got, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(got)
	return got
}

// TestFileSetMatchesToday: one Go pass caches the same files as
// preextract.js followed by enrich.py. Run with PREEXTRACT_JS (server/dist's
// preextract.js, node on PATH), ENRICH_PY (indexer/enrich.py, its .deps
// beside it) and GUFI_DIR2INDEX set.
func TestFileSetMatchesToday(t *testing.T) {
	pre, enrich, gufi := os.Getenv("PREEXTRACT_JS"), os.Getenv("ENRICH_PY"), os.Getenv("GUFI_DIR2INDEX")
	if pre == "" || enrich == "" || gufi == "" {
		t.Skip("PREEXTRACT_JS, ENRICH_PY and GUFI_DIR2INDEX not set")
	}
	root := t.TempDir()
	kb := filepath.Join(root, "kb")
	todayTree(t, kb)
	today, goCache, index := filepath.Join(root, "today"), filepath.Join(root, "go"), filepath.Join(root, "index")
	run := func(name string, args ...string) {
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
	}
	run("node", pre, "--kb-root", kb, "--extract-cache", today, "--no-ocr", "--jobs", "1")
	skip := filepath.Join(root, "skip")
	// The skip file the server writes for gufi_dir2index: the same names.
	var names []string
	for n := range SkipDirs {
		names = append(names, n)
	}
	sort.Strings(names)
	os.WriteFile(skip, []byte(strings.Join(names, "\n")+"\n"), 0o644)
	os.MkdirAll(index, 0o755)
	run(gufi, "-x", "-n", "1", "--skip-file", skip, kb, index)
	run("python3", "-I", enrich, "--kb-root", kb, "--index", filepath.Join(index, "kb"), "--extract-cache", today, "--no-ocr", "--jobs", "1")

	tools := FindTools()
	x := newX(t, Options{Tools: &tools})
	if _, err := x.Run(context.Background(), PassOptions{KBRoot: kb, CacheRoot: goCache}); err != nil {
		t.Fatal(err)
	}
	want, got := entries(t, today), entries(t, goCache)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("cached files differ\n  go: %v\ntoday: %v", got, want)
	}
	t.Logf("%d files, the same on both sides: %v", len(got), got)
}
