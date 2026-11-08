package object

import (
	"bytes"
	"testing"

	"github.com/giraffesyo/pdf/pdftest"
)

func TestEncryptedDocuments(t *testing.T) {
	specs := []struct {
		name string
		spec pdftest.EncryptSpec
	}{
		{"RC4-R2-40", pdftest.EncryptSpec{R: 2}},
		{"RC4-R3-128", pdftest.EncryptSpec{R: 3}},
		{"RC4-R4", pdftest.EncryptSpec{R: 4}},
		{"AESV2-R4", pdftest.EncryptSpec{R: 4, AES: true}},
	}
	for _, s := range specs {
		t.Run(s.name, func(t *testing.T) {
			doc := pdftest.BuildEncrypted(1, s.spec,
				pdftest.Catalog(2),
				pdftest.Pages(3),
				pdftest.Page(2, 4, "<< >>"),
				pdftest.Stream("", "secret content"),
			)
			r := open(t, doc)
			if got := streamText(t, r.Page(1).Key("Contents")); got != "secret content" {
				t.Errorf("%s: content = %q, want %q", s.name, got, "secret content")
			}
		})
	}
}

func TestEncryptedPasswordRequired(t *testing.T) {
	doc := pdftest.BuildEncrypted(1, pdftest.EncryptSpec{R: 4},
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< >>"),
		pdftest.Stream("", "x"),
	)
	// Corrupt the /U hex string so no empty password authenticates.
	corrupt := append([]byte{}, doc...)
	if k := bytes.Index(corrupt, []byte("/U <")); k >= 0 {
		corrupt[k+4] ^= 0xFF // flip a nibble inside the hex
		corrupt[k+4] = 'A'
	}
	if _, err := NewReader(bytes.NewReader(corrupt), int64(len(corrupt))); err == nil {
		t.Error("expected an error for an unrecoverable encrypted document")
	}
}
