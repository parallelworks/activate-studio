package search

import (
	"testing"
	"time"
)

// Dropping the counts drops the details kept with them, so Library Details
// shows a folder's file count as the index has it after a change.
func TestInvalidateCountsDropsDetails(t *testing.T) {
	counts.Lock()
	counts.m["root\x00tree\x00"] = &countEntry{at: time.Now(), done: make(chan struct{})}
	counts.Unlock()
	detailsCache.Lock()
	detailsCache.m["|kb|"] = detailsEntry{time.Now(), Details{Files: 8}}
	detailsCache.order = append(detailsCache.order, "|kb|")
	detailsCache.Unlock()

	InvalidateCounts()

	counts.Lock()
	n := len(counts.m)
	counts.Unlock()
	detailsCache.Lock()
	d, o := len(detailsCache.m), len(detailsCache.order)
	detailsCache.Unlock()
	if n != 0 || d != 0 || o != 0 {
		t.Fatalf("kept after InvalidateCounts: %d counts, %d details, %d in order", n, d, o)
	}
}
