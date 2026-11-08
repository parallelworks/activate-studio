package index

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeSnap(t testing.TB, dir, id, body string) {
	t.Helper()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(body))
	zw.Close()
	if err := os.WriteFile(filepath.Join(dir, id+".json.gz"), gz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func entriesJSON(n int, size int) string {
	var b strings.Builder
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"path":"d%d/f%d.txt","size":%d,"mtime":1700000000,"inode":%d}`, i%7, i, size, 1000+i)
	}
	return b.String()
}

// The listing reads the metadata whether it comes before the entries (as
// both servers write it) or after; a snapshot deleted and written again
// under the same id is read afresh by the listing, a level and a diff.
func TestHistoryCachesFollowFiles(t *testing.T) {
	dir := t.TempDir()
	h := &History{dir: dir, keep: 30}
	writeSnap(t, dir, "100", `{"id":"100","takenAt":100,"files":2,"bytes":20,"disk":0,"entries":[`+entriesJSON(2, 10)+`]}`)
	writeSnap(t, dir, "200", `{"entries":[`+entriesJSON(3, 10)+`],"id":"200","takenAt":200,"files":3,"bytes":30,"disk":0}`)
	got := h.List()
	if len(got) != 2 || got[0].Files != 2 || got[1].Files != 3 || got[1].Bytes != 30 || got[1].Disk == 0 {
		t.Fatalf("list %+v", got)
	}
	d, err := h.diff("100", "200")
	if err != nil || d.Counts.Added != 1 {
		t.Fatalf("diff %+v %v", d.Counts, err)
	}
	l, _ := h.level("200", "")
	if l.Totals.Files != 3 {
		t.Fatalf("level %+v", l.Totals)
	}

	h.delete("200")
	writeSnap(t, dir, "200", `{"id":"200","takenAt":200,"files":5,"bytes":50,"disk":0,"entries":[`+entriesJSON(5, 10)+`]}`)
	future := time.Now().Add(2 * time.Second)
	os.Chtimes(filepath.Join(dir, "200.json.gz"), future, future)
	if got := h.List(); got[1].Files != 5 {
		t.Fatalf("relist %+v", got)
	}
	if d, _ := h.diff("100", "200"); d.Counts.Added != 3 {
		t.Fatalf("rediff %+v", d.Counts)
	}
	if l, _ := h.level("200", ""); l.Totals.Files != 5 {
		t.Fatalf("relevel %+v", l.Totals)
	}
}

// Thirty days of a 40,000-file library: listing again reads no snapshot.
func BenchmarkHistoryList(b *testing.B) {
	dir := b.TempDir()
	h := &History{dir: dir, keep: 30}
	ents := entriesJSON(40000, 100)
	for i := range 30 {
		id := fmt.Sprint(1000 + i)
		writeSnap(b, dir, id, `{"id":"`+id+`","takenAt":`+id+`,"files":40000,"bytes":4000000,"disk":0,"entries":[`+ents+`]}`)
	}
	b.ResetTimer()
	for b.Loop() {
		if len(h.List()) != 30 {
			b.Fatal("list")
		}
	}
}
