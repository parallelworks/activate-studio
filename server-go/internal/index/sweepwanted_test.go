package index

import (
	"context"
	"testing"
	"time"
)

// A sync asked for before a sweep starts (a change of depth, then Sync
// now) is covered by that sweep: the waiting timer must not start a second
// sweep after it, which could run the work again with stale inputs.
func TestSweepCoversAnEarlierRequest(t *testing.T) {
	f := newFixture(t)
	fired := make(chan struct{}, 1)
	f.ix.mu.Lock()
	f.ix.sweepWanted = true
	f.ix.kickTimer = time.AfterFunc(time.Hour, func() { fired <- struct{}{} })
	f.ix.mu.Unlock()
	f.ix.Sweep(context.Background(), func(string) {})
	f.ix.mu.Lock()
	wanted, timer := f.ix.sweepWanted, f.ix.kickTimer
	f.ix.mu.Unlock()
	if wanted || timer != nil {
		t.Fatalf("after the sweep: wanted %v, timer %v", wanted, timer != nil)
	}
}
