package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// documentShape is everything a caller can observe about an extraction,
// for comparing a concurrent run with a sequential one.
type documentShape struct {
	text     string
	pages    int
	perPage  []int
	glyphs   []Glyph
	warnings []string
	numbers  []int
}

func shapeOf(doc *Document) documentShape {
	shape := documentShape{text: doc.Text(), pages: len(doc.Pages)}
	for _, p := range doc.Pages {
		shape.perPage = append(shape.perPage, len(p.Glyphs))
		shape.numbers = append(shape.numbers, p.Number)
		shape.glyphs = append(shape.glyphs, p.Glyphs...)
		for _, w := range p.Warnings {
			shape.warnings = append(shape.warnings, fmt.Sprintf("page %d: %v", p.Number, w))
		}
	}
	for _, w := range doc.Warnings {
		shape.warnings = append(shape.warnings, "doc: "+w.Error())
	}
	return shape
}

func extractShape(t *testing.T, data []byte, opts Options) documentShape {
	t.Helper()
	doc, err := ExtractWithOptions(t.Context(), bytes.NewReader(data), int64(len(data)), opts)
	if err != nil {
		t.Fatalf("Concurrency=%d: %v", opts.Concurrency, err)
	}
	return shapeOf(doc)
}

// TestConcurrentExtractionMatchesSequential: pages extracted in parallel
// must produce the same pages, glyphs, and warnings, in the same order,
// as pages extracted one at a time. Run with -race, this also covers the
// reader clones for data races.
func TestConcurrentExtractionMatchesSequential(t *testing.T) {
	files := corpusFiles(t, false)
	files = append(files, "") // "" marks the synthetic multi-page document below
	for _, path := range files {
		name := "synthetic-many-pages"
		var data []byte
		if path != "" {
			name = strings.TrimSuffix(filepath.Base(path), ".pdf")
			data = readCorpusFile(t, path)
		} else {
			data = manyPageDoc(t, 24)
		}
		t.Run(name, func(t *testing.T) {
			opts := Options{IncludeAnnotations: true}
			opts.Concurrency = 1
			want := extractShape(t, data, opts)
			for _, workers := range []int{2, 3, 8, 0} { // 0 selects a worker per processor
				opts.Concurrency = workers
				if got := extractShape(t, data, opts); !reflect.DeepEqual(got, want) {
					t.Errorf("Concurrency=%d differs from sequential:\n pages %d vs %d\n per-page %v vs %v\n warnings %q vs %q\n text equal: %v",
						workers, got.pages, want.pages, got.perPage, want.perPage, got.warnings, want.warnings, got.text == want.text)
				}
			}
		})
	}
}

// TestConcurrentExtractionHonoursOptions: page ranges, cancellation, and
// the options that force sequential extraction behave the same either way.
func TestConcurrentExtractionHonoursOptions(t *testing.T) {
	data := manyPageDoc(t, 12)

	t.Run("page range", func(t *testing.T) {
		opts := Options{Pages: []PageRange{{First: 3, Last: 7}}}
		opts.Concurrency = 1
		want := extractShape(t, data, opts)
		opts.Concurrency = 4
		if got := extractShape(t, data, opts); !reflect.DeepEqual(got, want) {
			t.Errorf("page range: %v vs %v", got.numbers, want.numbers)
		}
		if len(want.numbers) != 5 || want.numbers[0] != 3 {
			t.Fatalf("selected pages = %v, want 3..7", want.numbers)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := ExtractWithOptions(ctx, bytes.NewReader(data), int64(len(data)), Options{Concurrency: 4})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})

	t.Run("sequential-only options", func(t *testing.T) {
		if got := pageWorkers(Options{Concurrency: 8}, 10); got != 8 {
			t.Errorf("workers = %d, want 8", got)
		}
		if got := pageWorkers(Options{Concurrency: 8}, 1); got != 1 {
			t.Errorf("single page: workers = %d, want 1", got)
		}
		for name, opts := range map[string]Options{
			"strict":   {Concurrency: 8, Strict: true},
			"resolver": {Concurrency: 8, CMapResolver: func(string) ([]byte, error) { return nil, nil }},
		} {
			if got := pageWorkers(opts, 10); got != 1 {
				t.Errorf("%s: workers = %d, want 1", name, got)
			}
		}
		// OCR implementations are safe for concurrent use by contract.
		ocr := Options{Concurrency: 8, OCR: OCRFunc(func(context.Context, OCRRequest) ([]Glyph, error) { return nil, nil })}
		if got := pageWorkers(ocr, 10); got != 8 {
			t.Errorf("ocr: workers = %d, want 8", got)
		}
	})

	t.Run("negative concurrency rejected", func(t *testing.T) {
		_, err := ExtractWithOptions(t.Context(), bytes.NewReader(data), int64(len(data)), Options{Concurrency: -1})
		if err == nil {
			t.Fatal("negative Concurrency accepted")
		}
	})
}

// manyPageDoc builds a document of n pages, each with text unique to it.
func manyPageDoc(t *testing.T, n int) []byte {
	t.Helper()
	objs := []string{
		"<< /Type /Catalog /Pages 3 0 R >>", // 2
	}
	kids := make([]int, 0, n)
	first := 4 // object number of the first page
	for i := range n {
		kids = append(kids, first+2*i)
	}
	var pages strings.Builder
	pages.WriteString("<< /Type /Pages /Count " + itoa(n) + " /Kids [")
	for _, k := range kids {
		pages.WriteString(" " + itoa(k) + " 0 R")
	}
	pages.WriteString(" ] >>")
	objs = append(objs, pages.String()) // 3
	fontID := first + 2*n
	res := "<< /Font << /F1 " + itoa(fontID) + " 0 R >> >>"
	for i := range n {
		content := fmt.Sprintf("BT /F1 12 Tf 72 %d Td (page %d line one) Tj 0 -14 Td (page %d line two) Tj ET",
			700-i, i+1, i+1)
		objs = append(objs,
			"<< /Type /Page /Parent 3 0 R /Contents "+itoa(first+2*i+1)+" 0 R /Resources "+res+" /MediaBox [0 0 612 792] >>",
			streamObj(content))
	}
	objs = append(objs, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	return buildPDF(objs)
}

func streamObj(content string) string {
	return "<< /Length " + itoa(len(content)) + " >>\nstream\n" + content + "\nendstream"
}

// buildPDF assembles objects numbered from 2 with a classic xref table.
func buildPDF(objs []string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objs)+2)
	for i, obj := range objs {
		offsets[i+2] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+2, obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+2)
	for i := 1; i < len(objs)+2; i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 2 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+2, xref)
	return b.Bytes()
}

// TestDocumentTextMatchesPerPageText: Document.Text reconstructs long
// documents' pages in parallel; the result must equal the pages joined
// one at a time.
func TestDocumentTextMatchesPerPageText(t *testing.T) {
	for _, data := range [][]byte{manyPageDoc(t, 40), readCorpusFile(t, filepath.Join(corpusDir, "runc-security-audit.pdf"))} {
		doc, err := Extract(t.Context(), bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Pages) < 2*minPagesPerWorker {
			t.Fatalf("document has %d pages, too few to exercise the parallel path", len(doc.Pages))
		}
		var want strings.Builder
		for _, p := range doc.Pages {
			if text := p.Text(); text != "" {
				if want.Len() > 0 {
					want.WriteString("\n\n")
				}
				want.WriteString(text)
			}
		}
		if got := doc.Text(); got != want.String() {
			t.Errorf("Document.Text differs from the pages joined sequentially (%d vs %d bytes)", len(got), want.Len())
		}
	}
}
