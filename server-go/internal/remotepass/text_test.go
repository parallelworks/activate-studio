package remotepass

import (
	"encoding/json"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/index"
)

func TestReportIsEnrichPys(t *testing.T) {
	b := ReportJSON(map[string]*index.DocCount{".pdf": {Files: 2, NoText: 1}, ".docx": {Files: 1}}, "pdftotext", false, 7)
	want := `{"documents": {".docx": {"files": 1, "noText": 0}, ".pdf": {"files": 2, "noText": 1}}, "pdfReader": "pdftotext", "ocr": false, "textFiles": 7}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
	if !json.Valid(ReportJSON(nil, "", true, 0)) {
		t.Fatal("an empty report is not JSON")
	}
}

func TestPDFReaderHere(t *testing.T) {
	for _, c := range []struct {
		reader string
		have   bool
		want   string
	}{
		{"auto", true, "pdftotext"}, {"auto", false, BuiltInPDFReader}, {"pdftotext", false, ""},
		{"pdftotext", true, "pdftotext"}, {"pypdf", true, BuiltInPDFReader},
	} {
		if got := PDFReaderHere(c.reader, c.have); got != c.want {
			t.Errorf("%s, pdftotext %v: %q, want %q", c.reader, c.have, got, c.want)
		}
	}
}
