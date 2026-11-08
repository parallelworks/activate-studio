package cfb

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestNotCFB(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("plain text"), make([]byte, 600)} {
		if _, err := Open(bytes.NewReader(b), int64(len(b))); err != ErrNotCFB {
			t.Errorf("%d bytes: %v", len(b), err)
		}
	}
}

// TestFixtures opens the presentations in ../ppt/testdata (Apache POI's)
// and reads their document stream, by any case of its name.
func TestFixtures(t *testing.T) {
	files, _ := filepath.Glob("../ppt/testdata/*.ppt")
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	read := 0
	for _, p := range files {
		b, _ := os.ReadFile(p)
		f, err := Open(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			continue // some fixtures are damaged on purpose
		}
		s, err := f.Stream("powerpoint DOCUMENT")
		if err == nil && len(s) > 0 {
			read++
		}
		if _, err := f.Stream("no such stream"); err != ErrNotFound {
			t.Errorf("%s: missing stream: %v", p, err)
		}
	}
	if read < len(files)-2 {
		t.Fatalf("read %d of %d", read, len(files))
	}
}

func TestDigitLimit(t *testing.T) {
	b, _ := os.ReadFile("../ppt/testdata/basic_test_ppt_file.ppt")
	b = append([]byte(nil), b...)
	binary.LittleEndian.PutUint16(b[32:], pyDigitLimitShift)
	if _, err := Open(bytes.NewReader(b), int64(len(b))); err != ErrCorrupt {
		t.Fatalf("a mini sector shift Python cannot print: %v", err)
	}
	binary.LittleEndian.PutUint16(b[32:], pyDigitLimitShift-1)
	if _, err := Open(bytes.NewReader(b), int64(len(b))); err != nil {
		t.Fatalf("a mini sector shift Python prints: %v", err)
	}
}

func FuzzOpen(f *testing.F) {
	files, _ := filepath.Glob("../ppt/testdata/*.ppt")
	for _, p := range files {
		if b, err := os.ReadFile(p); err == nil && len(b) < 64<<10 {
			f.Add(b)
		}
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		c, err := Open(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return
		}
		c.Stream("PowerPoint Document")
		c.Stream("Workbook")
	})
}
