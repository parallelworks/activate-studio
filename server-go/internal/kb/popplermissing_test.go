package kb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// A host without poppler answers a page count or a page image with a 415
// that names what is missing, which the viewer shows over the PDF's text,
// in place of an internal error.
func TestPDFPagesWithoutPopplerSayWhy(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	base := t.TempDir()
	pdf := filepath.Join(base, "a.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &Previews{Roots: Roots{KBRoot: base, IndexBase: filepath.Join(base, "state"), IndexDir: filepath.Join(base, "state")}}
	_, err := p.PageCount(context.Background(), pdf)
	_, err2 := p.PagePNG(context.Background(), pdf, "a.pdf", 1)
	for tool, e := range map[string]error{"pdfinfo": err, "pdftoppm": err2} {
		var ae *apierr.Error
		if !errors.As(e, &ae) || ae.Status != 415 || !strings.Contains(e.Error(), "poppler's "+tool+" is not installed") {
			t.Fatalf("%s: got %v", tool, e)
		}
	}
}
