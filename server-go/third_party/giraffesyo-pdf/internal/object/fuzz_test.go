package object

import (
	"bytes"
	"io"
	"testing"

	"github.com/giraffesyo/pdf/internal/safeio"
	"github.com/giraffesyo/pdf/pdftest"
)

// FuzzReader drives NewReader and a bounded walk of every object over
// arbitrary input. The object layer must never panic or hang: malformed
// files return errors, malformed objects resolve to null.
func FuzzReader(f *testing.F) {
	f.Add(simpleDoc("BT (x) Tj ET"))
	f.Add(pdftest.BuildXrefStream(1,
		pdftest.Catalog(2), pdftest.Pages(3), pdftest.Page(2, 4, "<< >>"), pdftest.Stream("", "y")))
	f.Add(pdftest.BuildObjStm(1, []int{1, 2, 3},
		pdftest.Catalog(2), pdftest.Pages(3), pdftest.Page(2, 4, "<< >>"), pdftest.Stream("", "z")))
	f.Add(pdftest.BuildEncrypted(1, pdftest.EncryptSpec{R: 4},
		pdftest.Catalog(2), pdftest.Pages(3), pdftest.Page(2, 4, "<< >>"), pdftest.Stream("", "w")))
	f.Add([]byte("%PDF-1.7 garbage"))

	f.Fuzz(func(_ *testing.T, data []byte) {
		r, err := NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return
		}
		walked := 0
		for num := 1; num < len(r.xref) && walked < 2000; num++ {
			walked++
			obj, err := r.object(num, r.xref[num].gen)
			if err != nil {
				continue
			}
			if s, ok := obj.(*stream); ok {
				rc, err := r.streamReader(s)
				if err != nil {
					continue
				}
				_, _ = io.CopyN(io.Discard, rc, int64(safeio.MaxStreamBytes))
				_ = rc.Close()
			}
		}
		// Page navigation must also stay bounded.
		_ = r.Page(1)
	})
}
