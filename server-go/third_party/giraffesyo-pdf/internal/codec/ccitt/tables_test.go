package ccitt

import (
	"strings"
	"testing"
)

// lookup decodes a single code word from the given table width.
func lookup(t *testing.T, tbl []entry, bits uint, code string) entry {
	t.Helper()
	var v uint
	for _, b := range []byte(code) {
		v = v<<1 | uint(b-'0')
	}
	return tbl[v<<(bits-uint(len(code)))]
}

func TestCodeLookup(t *testing.T) {
	tests := []struct {
		name string
		tbl  []entry
		bits uint
		code string
		want entry
	}{
		{"white 0", whiteTable, whiteBits, "00110101", entry{8, 0}},
		{"white 1", whiteTable, whiteBits, "000111", entry{6, 1}},
		{"white 2", whiteTable, whiteBits, "0111", entry{4, 2}},
		{"white 63", whiteTable, whiteBits, "00110100", entry{8, 63}},
		{"white 64", whiteTable, whiteBits, "11011", entry{5, 64}},
		{"white 1664", whiteTable, whiteBits, "011000", entry{6, 1664}},
		{"white 1728", whiteTable, whiteBits, "010011011", entry{9, 1728}},
		{"white 1792", whiteTable, whiteBits, "00000001000", entry{11, 1792}},
		{"white 2560", whiteTable, whiteBits, "000000011111", entry{12, 2560}},
		{"black 0", blackTable, blackBits, "0000110111", entry{10, 0}},
		{"black 1", blackTable, blackBits, "010", entry{3, 1}},
		{"black 2", blackTable, blackBits, "11", entry{2, 2}},
		{"black 3", blackTable, blackBits, "10", entry{2, 3}},
		{"black 63", blackTable, blackBits, "000001100111", entry{12, 63}},
		{"black 64", blackTable, blackBits, "0000001111", entry{10, 64}},
		{"black 1728", blackTable, blackBits, "0000001100101", entry{13, 1728}},
		{"black 2560", blackTable, blackBits, "000000011111", entry{12, 2560}},
		{"pass", modeTable, modeBits, "0001", entry{4, modePass}},
		{"horizontal", modeTable, modeBits, "001", entry{3, modeHoriz}},
		{"V0", modeTable, modeBits, "1", entry{1, modeV0}},
		{"VR1", modeTable, modeBits, "011", entry{3, modeV0 + 1}},
		{"VR3", modeTable, modeBits, "0000011", entry{7, modeV0 + 3}},
		{"VL1", modeTable, modeBits, "010", entry{3, modeV0 - 1}},
		{"VL3", modeTable, modeBits, "0000010", entry{7, modeV0 - 3}},
		{"extension", modeTable, modeBits, "0000001", entry{7, modeExt}},
		{"EOL prefix is not a mode", modeTable, modeBits, "0000000", entry{}},
		{"EOL prefix is not a white run", whiteTable, whiteBits, "000000000001", entry{}},
		{"EOL prefix is not a black run", blackTable, blackBits, "0000000000010", entry{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := lookup(t, tc.tbl, tc.bits, tc.code); got != tc.want {
				t.Errorf("lookup(%s) = %+v, want %+v", tc.code, got, tc.want)
			}
		})
	}
}

// TestTablesPrefixFree checks that no code word of a table is a prefix of
// another (or of the EOL code), which would make buildTable silently
// overwrite entries.
func TestTablesPrefixFree(t *testing.T) {
	const eol = "000000000001"
	sets := map[string][]string{
		"white": collect(whiteCodes, extMakeupCodes),
		"black": collect(blackCodes, extMakeupCodes),
		"mode":  collect(modeCodes),
	}
	for name, codes := range sets {
		codes = append(codes, eol)
		for i, a := range codes {
			for j, b := range codes {
				if i != j && strings.HasPrefix(b, a) {
					t.Errorf("%s table: %q is a prefix of %q", name, a, b)
				}
			}
		}
	}
	if n := len(whiteCodes); n != 64+27 {
		t.Errorf("white codes: %d entries, want 91", n)
	}
	if n := len(blackCodes); n != 64+27 {
		t.Errorf("black codes: %d entries, want 91", n)
	}
	if n := len(extMakeupCodes); n != 13 {
		t.Errorf("extended makeup codes: %d entries, want 13", n)
	}
}

func collect(sets ...[]codeDef) []string {
	var out []string
	for _, s := range sets {
		for _, c := range s {
			out = append(out, c.code)
		}
	}
	return out
}

// TestRunsComplete checks that every run length 0..2560 that the standard
// assigns a code to is present exactly once per colour.
func TestRunsComplete(t *testing.T) {
	for _, set := range []struct {
		name string
		defs []codeDef
	}{{"white", whiteCodes}, {"black", blackCodes}} {
		seen := map[uint16]bool{}
		for _, c := range set.defs {
			if seen[c.value] {
				t.Errorf("%s: run %d defined twice", set.name, c.value)
			}
			seen[c.value] = true
		}
		for r := range uint16(64) {
			if !seen[r] {
				t.Errorf("%s: terminating code %d missing", set.name, r)
			}
		}
		for r := uint16(64); r <= 1728; r += 64 {
			if !seen[r] {
				t.Errorf("%s: makeup code %d missing", set.name, r)
			}
		}
	}
	seen := map[uint16]bool{}
	for _, c := range extMakeupCodes {
		seen[c.value] = true
	}
	for r := uint16(1792); r <= 2560; r += 64 {
		if !seen[r] {
			t.Errorf("extended makeup code %d missing", r)
		}
	}
}
