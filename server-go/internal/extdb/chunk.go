package extdb

import "strings"

// ChunkChars is the most characters an ext_bm25 chunk holds.
const ChunkChars = 2000

// Chunk is one ext_bm25 row's text: its place in the file's chunks
// (chunk_sequence, from 0) and the characters of the file's text it holds
// (start_position up to end_position, counted in characters).
type Chunk struct {
	Seq        int
	Start, End int
	Text       string
}

// Split cuts text into chunks of at most max characters that do not
// overlap. A cut falls at the last blank line in the chunk where there is
// one, else at a line end or a space in its second half, else at max;
// the separator at a cut belongs to neither side. So a text whose
// paragraphs are each shorter than max comes back whole when its chunks
// are joined with a blank line, which is how the Studio's reader joins
// them. Chunks holding only white space are left out.
func Split(text string, max int) []Chunk {
	if max <= 0 {
		max = ChunkChars
	}
	r := []rune(text)
	var out []Chunk
	for pos := 0; pos < len(r); {
		end, next := len(r), len(r)
		if pos+max < len(r) {
			end, next = cut(r, pos, pos+max)
		}
		if s := string(r[pos:end]); strings.TrimSpace(s) != "" {
			out = append(out, Chunk{Seq: len(out), Start: pos, End: end, Text: s})
		}
		pos = next
	}
	return out
}

// cut is where a chunk of r starting at pos and ending at most at limit
// ends, and where the next one starts.
func cut(r []rune, pos, limit int) (end, next int) {
	half := pos + (limit-pos)/2
	for _, sep := range []string{"\n\n", "\n", " "} {
		s := []rune(sep)
		floor := half
		if sep == "\n\n" {
			floor = pos + 1
		}
		for i := limit; i >= floor; i-- {
			if runesAt(r, i, s) {
				return i, i + len(s)
			}
		}
	}
	return limit, limit
}

func runesAt(r []rune, i int, s []rune) bool {
	if i < 0 || i+len(s) > len(r) {
		return false
	}
	for j, c := range s {
		if r[i+j] != c {
			return false
		}
	}
	return true
}
