package gufitrace

import "testing"

// A directory's external database in one schema fails the statements of
// the other: those warnings are expected, for either schema's tables.
func TestSplitStderrExternalSchemas(t *testing.T) {
	text := "Warning: Could not copy external database data \"/ix/a/ext_all.db\" using \"INSERT ...\": no such table: extdb.ext_bm25_by_file\n" +
		"Warning: Could not copy external database data \"/ix/b/old.db\" using \"INSERT ...\": no such table: extdb.ext_bm25\n" +
		"Warning: Could not copy external database data \"/ix/b/old.db\" using \"INSERT ...\": no such table: extdb.ext_vectors\n" +
		"Error: no such table: ext_bm25_extra\n"
	other, n, _ := SplitStderr(text)
	if n != 3 {
		t.Errorf("want 3 expected lines, got %d", n)
	}
	if other != "Error: no such table: ext_bm25_extra" {
		t.Errorf("want the other line kept, got %q", other)
	}
}
