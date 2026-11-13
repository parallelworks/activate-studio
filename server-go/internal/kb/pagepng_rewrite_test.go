package kb

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/docwrite"
)

// A PDF rewritten under the same name gets new page images: pdftoppm's
// p-N.png replaced a missing pN.png only, so the old image was served.
func TestPagePNGFollowsARewrittenPDF(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("no poppler here")
	}
	base := t.TempDir()
	p := &Previews{Roots: Roots{KBRoot: base, IndexBase: filepath.Join(base, "state"), IndexDir: filepath.Join(base, "state")}}
	pdf := filepath.Join(base, "a.pdf")
	render := func(md string, at time.Time) []byte {
		r, err := docwrite.Render(".pdf", md, nil)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(pdf, r.Data, 0o644)
		os.Chtimes(pdf, at, at)
		png, err := p.PagePNG(context.Background(), pdf, "a.pdf", 1)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(png)
		return b
	}
	first := render("# First version\n\nOne paragraph.", time.Now().Add(-time.Hour))
	second := render("# Second\n\n"+string(bytes.Repeat([]byte("Many more words here. "), 80)), time.Now().Add(time.Second))
	if bytes.Equal(first, second) {
		t.Error("the rewritten PDF's page 1 is the old image")
	}
}
