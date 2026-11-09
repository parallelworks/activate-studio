package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestLibraryIndexDirs(t *testing.T) {
	f := filepath.Join(t.TempDir(), "libraries.json")
	if err := os.WriteFile(f, []byte(`[{"id":"a","build":{"indexDir":"/x/idx"}},{"id":"b","build":{"indexDir":"rel"}},{"id":"c"},7]`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runLibraryIndexDirs([]string{f}, nil, &out, &errb); code != 0 || out.String() != "/x/idx\n" {
		t.Fatalf("%d %q %q", code, out.String(), errb.String())
	}
	out.Reset()
	if code := runLibraryIndexDirs([]string{filepath.Join(t.TempDir(), "none.json")}, nil, &out, &errb); code != 0 || out.Len() != 0 {
		t.Fatalf("missing file: %d %q", code, out.String())
	}
}

func TestJSONString(t *testing.T) {
	f := filepath.Join(t.TempDir(), ".studio-index.json")
	if err := os.WriteFile(f, []byte(`{"index": "/a b/it's", "n": 3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if runJSONString([]string{f, "index"}, nil, &out, &out); out.String() != "/a b/it's\n" {
		t.Fatalf("%q", out.String())
	}
	out.Reset()
	if runJSONString([]string{f, "n"}, nil, &out, &out); out.Len() != 0 {
		t.Fatalf("not a string: %q", out.String())
	}
}
