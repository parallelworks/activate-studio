package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/giraffesyo/pdf/pdftest"
)

func extractOptions(t *testing.T, data []byte, options Options) (*Document, error) {
	t.Helper()
	return ExtractWithOptions(context.Background(), bytes.NewReader(data), int64(len(data)), options)
}

func TestExtractionWarningsAndStrictMode(t *testing.T) {
	t.Run("stream filter", func(t *testing.T) {
		data := pdftest.Build(1,
			pdftest.Catalog(2),
			pdftest.Pages(3),
			pdftest.Page(2, 4, "<< >>"),
			pdftest.Stream("/Filter /DCTDecode", "not a JPEG"),
		)
		doc, err := extractOptions(t, data, Options{})
		if err != nil {
			t.Fatalf("permissive extraction: %v", err)
		}
		if len(doc.Warnings) != 1 || doc.Warnings[0].Code != WarningStream {
			t.Fatalf("warnings = %#v", doc.Warnings)
		}
		doc, err = extractOptions(t, data, Options{Strict: true})
		var strict *StrictError
		if !errors.As(err, &strict) || doc == nil || strict.Warning.Code != WarningStream {
			t.Fatalf("strict result: doc=%#v err=%v", doc, err)
		}
	})

	t.Run("decoded size", func(t *testing.T) {
		data := simpleDoc("BT /F1 12 Tf 72 700 Td (long text) Tj ET")
		doc, err := extractOptions(t, data, Options{Limits: Limits{MaxStreamBytes: 12}})
		if err != nil {
			t.Fatal(err)
		}
		if !hasWarning(doc.Warnings, WarningStreamLimit) {
			t.Fatalf("warnings = %#v", doc.Warnings)
		}
	})

	t.Run("operator and glyph budgets", func(t *testing.T) {
		data := simpleDoc("BT /F1 12 Tf 72 700 Td (ab) Tj ET")
		for _, limits := range []Limits{
			{MaxOperatorsPerPage: 1},
			{MaxGlyphsPerPage: 1},
		} {
			doc, err := extractOptions(t, data, Options{Limits: limits})
			if err != nil {
				t.Fatal(err)
			}
			if !hasWarning(doc.Warnings, WarningWorkLimit) {
				t.Fatalf("limits %#v warnings = %#v", limits, doc.Warnings)
			}
		}
	})

	t.Run("malformed content", func(t *testing.T) {
		data := simpleDoc("BT /F1 12 Tf (unterminated")
		doc, err := extractOptions(t, data, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !hasWarning(doc.Warnings, WarningMalformedPage) {
			t.Fatalf("warnings = %#v", doc.Warnings)
		}
	})

	t.Run("invalid operator operands", func(t *testing.T) {
		data := simpleDoc("BT /F1 12 Tf 10 10 Td (before) Tj /F1 Tf (after) Tj ET")
		doc, err := extractOptions(t, data, Options{})
		if err != nil || !hasWarning(doc.Warnings, WarningMalformedPage) ||
			!strings.Contains(doc.Text(), "before") || !strings.Contains(doc.Text(), "after") {
			t.Fatalf("permissive operator result: text=%q warnings=%#v err=%v", doc.Text(), doc.Warnings, err)
		}
		doc, err = extractOptions(t, data, Options{Strict: true})
		var strict *StrictError
		if !errors.As(err, &strict) || doc == nil {
			t.Fatalf("strict operator result: doc=%#v err=%v", doc, err)
		}
		if doc.Text() != "before" {
			t.Fatalf("strict partial text = %q", doc.Text())
		}
	})

	t.Run("malformed document extras", func(t *testing.T) {
		data := pdftest.Build(1,
			"<< /Type /Catalog /Pages 2 0 R /Outlines << /First 6 0 R >> >>",
			pdftest.Pages(3),
			pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
			pdftest.Stream("", "BT /F1 12 Tf 10 10 Td (page) Tj ET"),
			pdftest.Helvetica(),
			"<< /Title (cycle) /Next 6 0 R >>",
		)
		doc, err := extractOptions(t, data, Options{IncludeOutlines: true})
		if err != nil || !hasWarning(doc.Warnings, WarningMalformedDocument) {
			t.Fatalf("permissive document warning: warnings=%#v err=%v", doc.Warnings, err)
		}
		doc, err = extractOptions(t, data, Options{IncludeOutlines: true, Strict: true})
		var strict *StrictError
		if !errors.As(err, &strict) || doc == nil ||
			strict.Warning.Code != WarningMalformedDocument {
			t.Fatalf("strict document result: doc=%#v err=%v", doc, err)
		}
	})
}

func hasWarning(warnings []Warning, code WarningCode) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func TestPasswordOptionsAndExportedError(t *testing.T) {
	data := pdftest.BuildEncrypted(1, pdftest.EncryptSpec{
		R:             4,
		AES:           true,
		UserPassword:  "secret",
		OwnerPassword: "owner",
	},
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
		pdftest.Stream("", "BT /F1 12 Tf 72 700 Td (protected) Tj ET"),
		pdftest.Helvetica(),
	)
	if _, err := extractOptions(t, data, Options{}); !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("empty password error = %v", err)
	}
	doc, err := extractOptions(t, data, Options{Password: "secret"})
	if err != nil {
		t.Fatalf("user password: %v", err)
	}
	if got := doc.Text(); got != "protected" {
		t.Fatalf("text = %q", got)
	}
	doc, err = extractOptions(t, data, Options{Password: "owner"})
	if err != nil || doc.Text() != "protected" {
		t.Fatalf("owner password: text=%q err=%v", doc.Text(), err)
	}
}

func TestPageRangesStreamingAndPageTreeCounts(t *testing.T) {
	data := pdftest.Build(1,
		pdftest.Catalog(2),
		// Deliberately understated /Count: extraction must walk actual leaves.
		"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 1 >>",
		pdftest.Page(2, 5, "<< /Font << /F1 7 0 R >> >>"),
		pdftest.Page(2, 6, "<< /Font << /F1 7 0 R >> >>"),
		pdftest.Stream("", "BT /F1 12 Tf 10 10 Td (one) Tj ET"),
		pdftest.Stream("", "BT /F1 12 Tf 10 10 Td (two) Tj ET"),
		pdftest.Helvetica(),
	)
	doc, err := extractOptions(t, data, Options{Pages: []PageRange{{First: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	if doc.PageCount != 2 || len(doc.Pages) != 1 || doc.Pages[0].Number != 2 || doc.Text() != "two" {
		t.Fatalf("ranged document = %#v text=%q", doc, doc.Text())
	}

	var streamed []Page
	summary, err := ExtractPages(
		context.Background(),
		bytes.NewReader(data),
		int64(len(data)),
		Options{},
		func(page Page) error {
			streamed = append(streamed, page)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if summary.PageCount != 2 || len(summary.Pages) != 0 || len(streamed) != 2 {
		t.Fatalf("summary=%#v streamed=%d", summary, len(streamed))
	}
}

func TestCancellationInsideContentProcessing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	encodingCMap := []byte(`
begincmap
1 begincodespacerange
<0000> <FFFF>
endcodespacerange
endcmap`)
	content := "BT /F1 12 Tf " + strings.Repeat("q ", 1500) + "ET"
	data := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
		pdftest.Stream("", content),
		"<< /Type /Font /Subtype /Type0 /Encoding /Cancel-H /DescendantFonts [6 0 R] >>",
		pdftest.CIDFont(""),
	)
	_, err := ExtractWithOptions(ctx, bytes.NewReader(data), int64(len(data)), Options{
		CMapResolver: func(_ string) ([]byte, error) {
			cancel()
			return encodingCMap, nil
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
}

func TestMetadataOutlinesAnnotationsAndForms(t *testing.T) {
	data := pdftest.Build(1,
		"<< /Type /Catalog /Pages 2 0 R /Metadata 8 0 R /Outlines 9 0 R /AcroForm 12 0 R >>",
		pdftest.Pages(3),
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 400] /CropBox [5 6 290 390] /Rotate 90 "+
			"/Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R /Annots [6 0 R] >>",
		pdftest.Stream("", "BT /F1 12 Tf 10 10 Td (page) Tj ET"),
		pdftest.Helvetica(),
		"<< /Type /Annot /Subtype /Link /Rect [10 20 30 40] /Contents (annotation) /T (author) "+
			"/Dest [3 0 R /XYZ 11 22 1] /A << /S /URI /URI (https://example.test) >> >>",
		"<< /Title (Document title) /Author (Ada) /CustomKey (custom value) >>",
		pdftest.Stream("/Type /Metadata /Subtype /XML", "<x:xmpmeta>sample</x:xmpmeta>"),
		"<< /Type /Outlines /First 10 0 R /Last 10 0 R >>",
		"<< /Title (Chapter one) /Dest [3 0 R /XYZ 1 2 1] >>",
		"<< /FT /Tx /T (customer) /TU (Customer name) /V (Grace) /P 3 0 R >>",
		"<< /Fields [11 0 R] >>",
	)
	data = []byte(strings.Replace(
		string(data),
		"/Root 1 0 R >>\nstartxref",
		"/Root 1 0 R /Info 7 0 R >>\nstartxref",
		1,
	))
	doc, err := extractOptions(t, data, Options{
		IncludeMetadata:    true,
		IncludeOutlines:    true,
		IncludeAnnotations: true,
		IncludeFormValues:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Metadata.Title != "Document title" || doc.Metadata.Author != "Ada" ||
		doc.Metadata.Custom["CustomKey"] != "custom value" ||
		!bytes.Contains(doc.Metadata.XMP, []byte("sample")) {
		t.Fatalf("metadata = %#v", doc.Metadata)
	}
	if len(doc.Outlines) != 1 || doc.Outlines[0].Title != "Chapter one" ||
		doc.Outlines[0].Destination.Page != 1 {
		t.Fatalf("outlines = %#v", doc.Outlines)
	}
	if len(doc.Pages) != 1 || doc.Pages[0].MediaBox.MaxY != 400 || doc.Pages[0].CropBox.MinX != 5 ||
		doc.Pages[0].Rotation != 90 || len(doc.Pages[0].Annotations) != 1 {
		t.Fatalf("page extras = %#v", doc.Pages)
	}
	annotation := doc.Pages[0].Annotations[0]
	if annotation.URL != "https://example.test" || annotation.Destination.Page != 1 ||
		annotation.Contents != "annotation" {
		t.Fatalf("annotation = %#v", annotation)
	}
	if len(doc.FormFields) != 1 || doc.FormFields[0].Name != "customer" ||
		doc.FormFields[0].Value != "Grace" || doc.FormFields[0].Page != 1 {
		t.Fatalf("form fields = %#v", doc.FormFields)
	}
}

func TestOCRHook(t *testing.T) {
	data := simpleDoc("")
	calls := 0
	doc, err := extractOptions(t, data, Options{OCR: OCRFunc(func(_ context.Context, request OCRRequest) ([]Glyph, error) {
		calls++
		if request.PageNumber != 1 || request.Size != int64(len(data)) {
			return nil, fmt.Errorf("bad OCR request: %#v", request)
		}
		return []Glyph{{Text: "scanned", X: 10, Y: 20, Advance: 40, Size: 12}}, nil
	})})
	if err != nil || calls != 1 || doc.Text() != "scanned" {
		t.Fatalf("OCR: calls=%d text=%q err=%v", calls, doc.Text(), err)
	}

	wantErr := errors.New("OCR unavailable")
	doc, err = extractOptions(t, data, Options{OCR: OCRFunc(func(context.Context, OCRRequest) ([]Glyph, error) {
		return nil, wantErr
	})})
	if err != nil || !hasWarning(doc.Warnings, WarningOCR) {
		t.Fatalf("permissive OCR error: warnings=%#v err=%v", doc.Warnings, err)
	}
	_, err = extractOptions(t, data, Options{
		Strict: true,
		OCR: OCRFunc(func(context.Context, OCRRequest) ([]Glyph, error) {
			return nil, wantErr
		}),
	})
	var strict *StrictError
	if !errors.As(err, &strict) || !errors.Is(err, wantErr) {
		t.Fatalf("strict OCR error = %v", err)
	}
}

// The three policies are the answers worth naming, not the only
// reasonable ones. A predicate says what a particular document needs.
func TestOCRSelectOverridesThePolicy(t *testing.T) {
	// A page with typeset text and one image: OCRTextlessPages passes it
	// by, which is the case a threshold exists to catch.
	data := imageDoc("BT /F1 12 Tf 72 700 Td (Typeset) Tj ET /Im1 Do", grayImageObj(1, 1, "\x00", ""))

	t.Run("selects a page the policy would skip", func(t *testing.T) {
		var seen Page
		doc, err := extractOptions(t, data, Options{
			OCRSelect: func(page Page) bool {
				seen = page
				return len(page.Glyphs) < 100 // a floor on glyphs per page
			},
			OCR: OCRFunc(func(context.Context, OCRRequest) ([]Glyph, error) {
				return []Glyph{{Text: "scanned", X: 10, Y: 20, Advance: 40, Size: 12}}, nil
			}),
		})
		if err != nil {
			t.Fatalf("extract: %v", err)
		}
		if !strings.Contains(doc.Text(), "scanned") {
			t.Errorf("text = %q, want the OCR'd word: the predicate selected this page", doc.Text())
		}
		// The page a predicate judges is the one the content streams
		// produced: its glyphs and its image count, but not image data,
		// which is read only once something asks for it.
		if len(seen.Glyphs) == 0 {
			t.Error("predicate saw no glyphs, want the page's own text")
		}
		if seen.ImageCount != 1 {
			t.Errorf("predicate saw ImageCount = %d, want 1", seen.ImageCount)
		}
		if len(seen.Images) != 0 {
			t.Errorf("predicate saw %d images, want none loaded yet", len(seen.Images))
		}
	})

	t.Run("skips a page the policy would select", func(t *testing.T) {
		calls := 0
		doc, err := extractOptions(t, simpleDoc(""), Options{
			OCRPolicy: OCRAllPages,
			OCRSelect: func(Page) bool { return false },
			OCR: OCRFunc(func(context.Context, OCRRequest) ([]Glyph, error) {
				calls++
				return []Glyph{{Text: "scanned", Advance: 1, Size: 1}}, nil
			}),
		})
		if err != nil {
			t.Fatalf("extract: %v", err)
		}
		if calls != 0 {
			t.Errorf("OCR calls = %d, want none: the predicate overrides the policy", calls)
		}
		if doc.Text() != "" {
			t.Errorf("text = %q, want none", doc.Text())
		}
	})

	// Without a predicate the policy still decides, so the field is
	// additive for every existing caller.
	t.Run("policy still applies when unset", func(t *testing.T) {
		calls := 0
		if _, err := extractOptions(t, data, Options{
			OCR: OCRFunc(func(context.Context, OCRRequest) ([]Glyph, error) {
				calls++
				return nil, nil
			}),
		}); err != nil {
			t.Fatalf("extract: %v", err)
		}
		if calls != 0 {
			t.Errorf("OCR calls = %d, want none: the page has text and the default policy is textless-only", calls)
		}
	})
}

func TestOptionValidation(t *testing.T) {
	data := simpleDoc("")
	for _, options := range []Options{
		{Pages: []PageRange{{First: 0}}},
		{Pages: []PageRange{{First: 2, Last: 1}}},
		{Limits: Limits{MaxStreamBytes: -1}},
		{Layout: LayoutOptions{Mode: LayoutMode(255)}},
	} {
		if _, err := extractOptions(t, data, options); err == nil {
			t.Errorf("options %#v should fail validation", options)
		}
	}
}
