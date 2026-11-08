package wordpiece

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
)

// TestSamples compares the ids of many texts with the library's
// (WORDPIECE_SAMPLES, written by scripts/wordpiece_samples.py).
func TestSamples(t *testing.T) {
	samples, tj := os.Getenv("WORDPIECE_SAMPLES"), os.Getenv("WORDPIECE_TOKENIZER")
	if samples == "" || tj == "" {
		t.Skip("WORDPIECE_SAMPLES and WORDPIECE_TOKENIZER not set")
	}
	tok, err := Load(tj)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(samples)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	n, bad := 0, 0
	for sc.Scan() {
		var s struct {
			Text string  `json:"text"`
			IDs  []int32 `json:"ids"`
		}
		if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		n++
		got := tok.Encode(s.Text, 256)
		if !equal(got, s.IDs) {
			bad++
			if bad <= 5 {
				t.Errorf("%q:\n got  %v\n want %v", trunc(s.Text), got, s.IDs)
			}
		}
	}
	t.Logf("%d texts, %d differ", n, bad)
}

func equal(a, b []int32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func trunc(s string) string {
	if len(s) > 300 {
		return s[:300]
	}
	return s
}
