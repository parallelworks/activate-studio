package pdf

import (
	"bytes"
	"context"
	"testing"
)

func BenchmarkExtract(b *testing.B) {
	doc := simpleDoc(`BT /F1 12 Tf 14 TL 72 720 Td
(Benchmark line one with several words on it) Tj
T* (line two follows with more words) Tj
T* [(kerned) -300 (segments) -250 (too)] TJ
ET`)
	ctx := context.Background()
	for b.Loop() {
		d, err := Extract(ctx, bytes.NewReader(doc), int64(len(doc)))
		if err != nil {
			b.Fatal(err)
		}
		if d.Text() == "" {
			b.Fatal("empty extraction")
		}
	}
}
