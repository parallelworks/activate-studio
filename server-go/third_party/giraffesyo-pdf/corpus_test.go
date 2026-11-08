package pdf

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The real-document corpus under testdata/corpus complements the synthetic
// fixtures: it pins Document.Text for files produced by real generators
// (LaTeX, Word, scanning pipelines) with embedded Type1, TrueType, and CFF
// fonts across multiple pages, so a layout, decoding, or performance change
// shows up as a reviewable diff rather than a surprise downstream.
// Provenance and licenses are in testdata/corpus/NOTICE.md.

var updateGolden = flag.Bool("update", false, "rewrite testdata/corpus/golden from current output")

const corpusDir = "testdata/corpus"

// corpusFiles lists the committed corpus. With extra set, files from the
// PDF_CORPUS_DIR environment variable — documents that cannot be
// redistributed — are appended for local runs.
func corpusFiles(tb testing.TB, extra bool) []string {
	tb.Helper()
	files, err := filepath.Glob(filepath.Join(corpusDir, "*.pdf"))
	if err != nil {
		tb.Fatal(err)
	}
	if len(files) == 0 {
		tb.Fatalf("no corpus files in %s", corpusDir)
	}
	if dir := os.Getenv("PDF_CORPUS_DIR"); extra && dir != "" {
		more, err := filepath.Glob(filepath.Join(dir, "*.pdf"))
		if err != nil {
			tb.Fatal(err)
		}
		files = append(files, more...)
	}
	return files
}

func readCorpusFile(tb testing.TB, path string) []byte {
	tb.Helper()
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		tb.Fatal(err)
	}
	return data
}

func TestCorpusGolden(t *testing.T) {
	for _, path := range corpusFiles(t, false) {
		name := strings.TrimSuffix(filepath.Base(path), ".pdf")
		t.Run(name, func(t *testing.T) {
			data := readCorpusFile(t, path)
			doc, err := Extract(t.Context(), bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if len(doc.Pages) != doc.PageCount || doc.PageCount == 0 {
				t.Fatalf("pages = %d, PageCount = %d", len(doc.Pages), doc.PageCount)
			}
			checkGolden(t, filepath.Join(corpusDir, "golden", name+".txt"), doc.Text())
			// Warnings are part of the contract too: a decoder change that
			// starts (or stops) flagging a font should be visible.
			var warnings strings.Builder
			for _, w := range doc.Warnings {
				warnings.WriteString(w.Error())
				warnings.WriteByte('\n')
			}
			checkGolden(t, filepath.Join(corpusDir, "golden", name+".warnings.txt"), warnings.String())
		})
	}
}

// checkGolden compares got with the golden file at path, or rewrites it
// under -update. An empty got means the file must not exist.
func checkGolden(t *testing.T, path, got string) {
	t.Helper()
	path = filepath.Clean(path)
	if *updateGolden {
		if got == "" {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			return
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if got != "" {
			t.Errorf("%s: got output but no golden file (run: go test -run TestCorpusGolden -update):\n%s", path, got)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs (run with -update to accept):\n%s", path, firstDifference(got, string(want)))
	}
}

// firstDifference reports the first line where got and want diverge.
func firstDifference(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := range max(len(g), len(w)) {
		var gl, wl string
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if gl != wl {
			return "line " + itoa(i+1) + ":\n  got:  " + gl + "\n  want: " + wl
		}
	}
	return "(no line differs; trailing newline or length mismatch)"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// BenchmarkCorpus measures Extract plus Document.Text over the corpus —
// the whole path a text consumer runs. CI compares it between a pull
// request and its base.
func BenchmarkCorpus(b *testing.B) {
	ctx := context.Background()
	for _, path := range corpusFiles(b, true) {
		data := readCorpusFile(b, path)
		b.Run(strings.TrimSuffix(filepath.Base(path), ".pdf"), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				doc, err := Extract(ctx, bytes.NewReader(data), int64(len(data)))
				if err != nil {
					b.Fatal(err)
				}
				if doc.Text() == "" {
					b.Fatal("empty extraction")
				}
			}
		})
	}
}
