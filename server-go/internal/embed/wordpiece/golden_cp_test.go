package wordpiece

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"unicode"
)

// TestCodePoints compares every code point's normalization and
// pre-tokenizer class with the library's (WORDPIECE_CP_GOLDEN, written by
// scripts/wordpiece_golden.py).
func TestCodePoints(t *testing.T) {
	golden := os.Getenv("WORDPIECE_CP_GOLDEN")
	tj := os.Getenv("WORDPIECE_TOKENIZER")
	if golden == "" || tj == "" {
		t.Skip("WORDPIECE_CP_GOLDEN and WORDPIECE_TOKENIZER not set")
	}
	tok, err := Load(tj)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string][2]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	want := map[rune][2]any{}
	for k, v := range raw {
		n, _ := strconv.Atoi(k)
		want[rune(n)] = v
	}
	var bad []string
	for cp := rune(0x80); cp < 0x110000; cp++ {
		if cp >= 0xD800 && cp <= 0xDFFF {
			continue
		}
		ch := string(cp)
		wn, wc := ch, 0.0
		if v, ok := want[cp]; ok {
			wn, wc = v[0].(string), v[1].(float64)
		}
		gn := tok.normalize(ch)
		gc := 0.0
		pieces := tok.preTokenize("a" + ch + "a")
		if len(pieces) == 3 && pieces[1] == ch {
			gc = 1
		} else if len(pieces) == 2 {
			gc = 2
		}
		if gn != wn || gc != wc {
			bad = append(bad, fmt.Sprintf("%U %q: norm %q want %q, class %v want %v (Go: L=%v M=%v P=%v S=%v C=%v)", cp, ch, gn, wn, gc, wc,
				unicode.IsLetter(cp), unicode.IsMark(cp), unicode.IsPunct(cp), unicode.IsSymbol(cp), unicode.IsControl(cp)))
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		os.WriteFile(os.Getenv("WORDPIECE_CP_DIFF"), []byte(fmt.Sprint(len(bad), "\n", joinLines(bad))), 0o644)
		t.Errorf("%d code points differ; first: %v", len(bad), bad[:min(20, len(bad))])
	}
}

func joinLines(xs []string) string {
	s := ""
	for _, x := range xs {
		s += x + "\n"
	}
	return s
}
