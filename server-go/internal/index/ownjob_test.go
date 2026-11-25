package index

import (
	"strings"
	"testing"
	"time"
)

// A job of a caller's own that fails of its own (a panic) is a failed job:
// the server stays up.
func TestOwnJobPanics(t *testing.T) {
	ix := newFixture(t).ix
	job, err := ix.StartOwnJob("library:x", func(*OwnJob) (int64, error) { panic("of its own") })
	if err != nil {
		t.Fatal(err)
	}
	for range 200 {
		ix.mu.Lock()
		j := ix.recs[job.ID].pub.clone()
		ix.mu.Unlock()
		if j.State == "running" {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if j.State != "error" || j.Error == nil || !strings.Contains(*j.Error, "of its own") || j.FinishedAt == nil {
			t.Errorf("%+v", j)
		}
		return
	}
	t.Error("it never ended")
}
