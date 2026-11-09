package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClearReopenMark(t *testing.T) {
	data := t.TempDir()
	mark := filepath.Join(data, "updates", "reopen")
	os.MkdirAll(filepath.Dir(mark), 0o755)
	os.WriteFile(mark, []byte("v1.190\n"), 0o644)
	clearReopenMark(data)
	if _, err := os.Stat(mark); !os.IsNotExist(err) {
		t.Fatal("the reopen mark is still there")
	}
	clearReopenMark(data) // and none is no error
}
