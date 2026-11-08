package pdf

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

// The benchmark gate compares a pull request against its base, which
// catches a step but not a slow drift, and timing on shared runners is
// noisy. Bytes and allocations per extraction are stable across machines,
// so they are pinned absolutely: testdata/corpus/golden/budget.json holds
// the figures last accepted with -update, and the test fails when a file
// exceeds its budget by more than budgetHeadroom. A 3× memory regression
// like v0.3.0's fails this on the first run.
//
// Extraction is measured sequentially. Concurrent extraction allocates a
// set of buffers per worker, and the worker count follows the machine, so
// the figures would otherwise depend on where the test ran; what is worth
// pinning is the work itself.

const budgetHeadroom = 1.15

type allocationBudget struct {
	Bytes  uint64 `json:"bytes"`
	Allocs uint64 `json:"allocs"`
}

func TestCorpusAllocationBudget(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not deterministic under -race (sync.Pool drops objects at random)")
	}
	budgetPath := filepath.Join(corpusDir, "golden", "budget.json")
	budgets := map[string]allocationBudget{}
	if data, err := os.ReadFile(filepath.Clean(budgetPath)); err == nil {
		if err := json.Unmarshal(data, &budgets); err != nil {
			t.Fatal(err)
		}
	} else if !*updateGolden {
		t.Fatalf("missing %s (run: go test -run TestCorpusAllocationBudget -update): %v", budgetPath, err)
	}

	measured := map[string]allocationBudget{}
	for _, path := range corpusFiles(t, false) {
		name := strings.TrimSuffix(filepath.Base(path), ".pdf")
		got := measureAllocations(t, readCorpusFile(t, path))
		measured[name] = got
		want, ok := budgets[name]
		switch {
		case *updateGolden:
			continue
		case !ok:
			t.Errorf("%s: no budget recorded (run with -update)", name)
		case float64(got.Bytes) > float64(want.Bytes)*budgetHeadroom:
			t.Errorf("%s: %d bytes/op exceeds budget %d by more than %.0f%% (run with -update to accept a deliberate change)",
				name, got.Bytes, want.Bytes, (budgetHeadroom-1)*100)
		case float64(got.Allocs) > float64(want.Allocs)*budgetHeadroom:
			t.Errorf("%s: %d allocs/op exceeds budget %d by more than %.0f%% (run with -update to accept a deliberate change)",
				name, got.Allocs, want.Allocs, (budgetHeadroom-1)*100)
		case float64(got.Bytes) < float64(want.Bytes)*0.8 || float64(got.Allocs) < float64(want.Allocs)*0.8:
			t.Logf("%s: now %d bytes/op, %d allocs/op — well under budget (%d, %d); tighten with -update",
				name, got.Bytes, got.Allocs, want.Bytes, want.Allocs)
		}
	}
	if *updateGolden {
		data, err := json.MarshalIndent(measured, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Clean(budgetPath), append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// measureAllocations returns the average bytes and allocations of one
// Extract plus Document.Text over several runs, with the collector off so
// pooled decoders are not evicted mid-measurement.
func measureAllocations(tb testing.TB, data []byte) allocationBudget {
	tb.Helper()
	ctx := context.Background()
	run := func() {
		doc, err := ExtractWithOptions(ctx, bytes.NewReader(data), int64(len(data)), Options{Concurrency: 1})
		if err != nil {
			tb.Fatal(err)
		}
		if doc.Text() == "" {
			tb.Fatal("empty extraction")
		}
	}
	run() // warm pools and caches the same way for every file
	const runs = 10
	runtime.GC()
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		run()
	}
	runtime.ReadMemStats(&after)
	return allocationBudget{
		Bytes:  (after.TotalAlloc - before.TotalAlloc) / runs,
		Allocs: (after.Mallocs - before.Mallocs) / runs,
	}
}
