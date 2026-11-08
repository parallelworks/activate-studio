package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerify(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a"), []byte("abc"), 0o644)
	const abc = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	var errb bytes.Buffer
	if code := run([]string{"verify", dir, "a=" + abc}, &errb, &errb); code != 0 {
		t.Fatalf("intact: %d %s", code, errb.String())
	}
	for _, c := range []struct {
		arg, says string
		code      int
	}{
		{"a=" + strings.Repeat("0", 64), "has SHA-256", 1},
		{"b=" + abc, "is missing", 1},
		{"../a=" + abc, "is not NAME=SHA256", 2},
	} {
		errb.Reset()
		if code := run([]string{"verify", dir, c.arg}, &errb, &errb); code != c.code || !strings.Contains(errb.String(), c.says) {
			t.Errorf("%s: %d %q", c.arg, code, errb.String())
		}
	}
}
