package pdf

import (
	"bytes"
	"context"
	"testing"

	"github.com/giraffesyo/pdf/pdftest"
)

// End-to-end Extract tests over the document structures the native object
// layer added: xref streams, object streams, hybrid references, Flate
// content, and encrypted files.

func extractText(t *testing.T, doc []byte) string {
	t.Helper()
	d, err := Extract(context.Background(), bytes.NewReader(doc), int64(len(doc)))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return d.Text()
}

func TestExtractXrefStream(t *testing.T) {
	doc := pdftest.BuildXrefStream(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
		pdftest.Stream("", "BT /F1 12 Tf 72 700 Td (xref stream page) Tj ET"),
		pdftest.Helvetica(),
	)
	if got := extractText(t, doc); got != "xref stream page" {
		t.Errorf("text = %q", got)
	}
}

func TestExtractObjectStream(t *testing.T) {
	doc := pdftest.BuildObjStm(1, []int{1, 2, 3, 5},
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
		pdftest.Stream("", "BT /F1 12 Tf 72 700 Td (packed in objstm) Tj ET"),
		pdftest.Helvetica(),
	)
	if got := extractText(t, doc); got != "packed in objstm" {
		t.Errorf("text = %q", got)
	}
}

func TestExtractHybridXref(t *testing.T) {
	doc := pdftest.BuildHybrid(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
		pdftest.Stream("", "BT /F1 12 Tf 72 700 Td (hybrid xref) Tj ET"),
		pdftest.Helvetica(),
	)
	if got := extractText(t, doc); got != "hybrid xref" {
		t.Errorf("text = %q", got)
	}
}

func TestExtractFlateContent(t *testing.T) {
	doc := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
		pdftest.Flate("", "BT /F1 12 Tf 72 700 Td (compressed content) Tj ET"),
		pdftest.Helvetica(),
	)
	if got := extractText(t, doc); got != "compressed content" {
		t.Errorf("text = %q", got)
	}
}

func TestExtractEncrypted(t *testing.T) {
	specs := map[string]pdftest.EncryptSpec{
		"RC4-R2": {R: 2},
		"RC4-R3": {R: 3},
		"RC4-R4": {R: 4},
		"AESV2":  {R: 4, AES: true},
	}
	for name, spec := range specs {
		t.Run(name, func(t *testing.T) {
			doc := pdftest.BuildEncrypted(1, spec,
				pdftest.Catalog(2),
				pdftest.Pages(3),
				pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
				pdftest.Stream("", "BT /F1 12 Tf 72 700 Td (decrypted text) Tj ET"),
				pdftest.Helvetica(),
			)
			if got := extractText(t, doc); got != "decrypted text" {
				t.Errorf("%s: text = %q, want %q", name, got, "decrypted text")
			}
		})
	}
}

func TestExtractEncryptedPasswordRequired(t *testing.T) {
	doc := pdftest.BuildEncrypted(1, pdftest.EncryptSpec{R: 4},
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< >>"),
		pdftest.Stream("", "BT (x) Tj ET"),
	)
	corrupt := append([]byte{}, doc...)
	if k := bytes.Index(corrupt, []byte("/U <")); k >= 0 {
		corrupt[k+4] = 'A' // break the /U hash so no empty password works
	}
	if _, err := Extract(context.Background(), bytes.NewReader(corrupt), int64(len(corrupt))); err == nil {
		t.Error("expected an error for a password-protected document")
	}
}
