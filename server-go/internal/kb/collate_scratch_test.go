package kb

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
)

func TestScratchCollate(t *testing.T) {
	var n, want []string
	b, _ := os.ReadFile(os.Getenv("HOME") + "/studio-dev/files-scratch/coll/names.json")
	json.Unmarshal(b, &n)
	b, _ = os.ReadFile(os.Getenv("HOME") + "/studio-dev/files-scratch/coll/node.json")
	json.Unmarshal(b, &want)
	sort.SliceStable(n, func(i, j int) bool { return LocaleCompare(n[i], n[j]) < 0 })
	for i := range n {
		if n[i] != want[i] {
			t.Errorf("%d go %q node %q", i, n[i], want[i])
		}
	}
}
