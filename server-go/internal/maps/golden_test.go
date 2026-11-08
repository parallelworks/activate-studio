package maps

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func nodeBin() string {
	if home, err := os.UserHomeDir(); err == nil {
		if p := filepath.Join(home, ".nvm", "versions", "node", "v26.10.0", "bin", "node"); exists(p) {
			return p
		}
	}
	p, _ := exec.LookPath("node")
	return p
}

// TestStagesMatchNode: the gufi_query stages of the remote map (count,
// fetch, neighbors), the inode hash and the int8 means put back are
// server/dist/remoteMap.js's, for the same arguments. Skipped without node
// or a built server/dist.
func TestStagesMatchNode(t *testing.T) {
	node := nodeBin()
	dist, _ := filepath.Abs("../../../server/dist")
	if node == "" || !exists(filepath.Join(dist, "remoteMap.js")) {
		t.Skip("node or server/dist missing")
	}
	hex := strings.Repeat("7F80", 192)
	script := `
const R = await import(process.env.DIST + '/remoteMap.js')
const q = '` + hex + `'
console.log(JSON.stringify({
  count: [R.countStages(null), R.countStages('/ext/*.db')],
  fetch: [R.fetchStages({ model: 'minilm-l6-mean-int8', source: 'studio' }, 2147483648, 1000, null),
    R.fetchStages({ model: "it's", source: 'external' }, 12345, 678, '/ext/*.db')],
  near: [R.neighborStages({ model: 'minilm-l6-mean-int8', source: 'studio' }, q, 4, null),
    R.neighborStages({ model: 'm', source: 'external' }, q, 7, '/ext/*.db')],
  hash: [0, 1, 5000, 123456789, 2 ** 40 + 3].map(R.fileHash),
  deq: [Array.from(R.dequantize('00017F80FF', 1)), Array.from(R.dequantize(q.slice(0, 16), 0.8))],
}))`
	tmp := t.TempDir()
	cmd := exec.Command(node, "--input-type=module", "-e", script)
	cmd.Env = append(os.Environ(), "DIST="+dist, "KB_ROOT="+filepath.Join(tmp, "kb"), "INDEX_BASE="+filepath.Join(tmp, "ix"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	var want struct {
		Count [][]string
		Fetch [][]string
		Near  [][]string
		Hash  []int64
		Deq   [][]float32
	}
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	ext := "/ext/*.db"
	f1, _ := FetchStages("minilm-l6-mean-int8", "studio", 2147483648, 1000, nil)
	f2, err := FetchStages("it's", "external", 12345, 678, &ext)
	if err != nil {
		t.Fatal(err)
	}
	n1, _ := NeighborStages("minilm-l6-mean-int8", "studio", hex, 4, nil)
	n2, _ := NeighborStages("m", "external", hex, 7, &ext)
	got := map[string]any{
		"count": [][]string{CountStages(nil), CountStages(&ext)},
		"fetch": [][]string{f1, f2},
		"near":  [][]string{n1, n2},
	}
	for name, w := range map[string][][]string{"count": want.Count, "fetch": want.Fetch, "near": want.Near} {
		g := got[name].([][]string)
		for i := range w {
			if !reflect.DeepEqual(g[i], w[i]) {
				t.Errorf("%s[%d]:\n go   %q\n node %q", name, i, g[i], w[i])
			}
		}
	}
	for i, in := range []int64{0, 1, 5000, 123456789, 1<<40 + 3} {
		if g := FileHash(in); g != want.Hash[i] {
			t.Errorf("FileHash(%d) = %d, node %d", in, g, want.Hash[i])
		}
	}
	if g := Dequantize("00017F80FF", 1); !reflect.DeepEqual(g, want.Deq[0]) {
		t.Errorf("Dequantize = %v, node %v", g, want.Deq[0])
	}
	if g := Dequantize(hex[:16], 0.8); !reflect.DeepEqual(g, want.Deq[1]) {
		t.Errorf("Dequantize scaled = %v, node %v", g, want.Deq[1])
	}
}
