package odf

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCorpusSinglePass reads every OpenDocument file under ODF_CORPUS both
// ways and compares them; with ODF_CACHE (an extract cache enrich.py
// wrote for that tree) it also lists the files whose text is not the
// entry's: those the pass cuts at its limit or hands to LibreOffice
// (empty text, not a zip) differ here and not in the cache.
func TestCorpusSinglePass(t *testing.T) {
	root, cache := os.Getenv("ODF_CORPUS"), os.Getenv("ODF_CACHE")
	if root == "" {
		t.Skip("ODF_CORPUS not set")
	}
	n, same := 0, 0
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		ext := strings.ToLower(filepath.Ext(p))
		if err != nil || d.IsDir() || (ext != ".odt" && ext != ".ods" && ext != ".odp" && ext != ".odg") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		n++
		r := strings.NewReader(string(b))
		one, err1 := Text(r, int64(len(b)), 500_000)
		twoPass = true
		two, err2 := Text(r, int64(len(b)), 500_000)
		twoPass = false
		if one != two || (err1 == nil) != (err2 == nil) {
			t.Errorf("%s: single and two-pass differ", p)
		}
		if cache != "" {
			rel, _ := filepath.Rel(root, p)
			want, err := os.ReadFile(filepath.Join(cache, rel+".txt"))
			if err == nil && string(want) == one || err != nil && one == "" {
				same++
			} else {
				t.Logf("differs from the cache: %s (go %d bytes, err %v; cache %d bytes)", rel, len(one), err1, len(want))
			}
		}
		return nil
	})
	t.Logf("%d files; %d equal to the cache", n, same)
}
