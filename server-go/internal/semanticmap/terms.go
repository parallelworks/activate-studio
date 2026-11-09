package semanticmap

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Naming clusters by their words: each file's passages give at most
// TextChars characters and keep their FileTerms commonest terms.
const (
	TextChars = 20_000
	FileTerms = 30
	// embed.py's passages: ChunkChars characters, one every ChunkStep, cut
	// wherever that falls.
	ChunkChars = 1000
	ChunkStep  = 880
)

var termRE = regexp.MustCompile(`[a-z][a-z0-9]{2,23}`)

var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`
a about above after again against all also am an and any are as at be because been before being below between both
but by can cannot could did do does doing down during each else etc even ever every few for from further get gets got
had has have having he her here hers him his how however if in into is it its itself just like made make makes many
may me might more most much must my no nor not now of off on once one only or other our ours out over own per same
see shall she should since so some such than that the their theirs them then there these they this those though
through thus to too two under until up upon us use used uses using very via was way we well were what when where
whether which while who whom whose why will with within without would yet you your yours three four five first
second new old also within non way ways need needs based include includes including example examples following
given within page pages figure fig table tables section chapter file files http https www com org net html htm pdf
png jpg jpeg gif svg doc docx ppt pptx xls xlsx txt csv json xml yaml yml true false null none nan
`) {
		m[w] = true
	}
	return m
}()

// TermCount is one of a file's commonest terms and how often it appears.
type TermCount struct {
	Term  string
	Count int
}

// FileTerms returns the FileTerms commonest terms of a file's text, with
// their counts: lower-case words of 3 to 24 letters and digits, starting
// with a letter, no stop words, and no runs of digits (versions, ids and
// hashes).
func fileTerms(text string) []TermCount {
	counts := map[string]int{}
	for _, t := range termRE.FindAllString(strings.ToLower(text), -1) {
		if stopwords[t] {
			continue
		}
		digits := 0
		for i := 0; i < len(t); i++ {
			if t[i] >= '0' && t[i] <= '9' {
				digits++
			}
		}
		if digits > 2 {
			continue
		}
		counts[t]++
	}
	out := make([]TermCount, 0, len(counts))
	for t, n := range counts {
		out = append(out, TermCount{t, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Term < out[j].Term
	})
	if len(out) > FileTerms {
		out = out[:FileTerms]
	}
	return out
}

// isAlnum matches Python's str.isalnum for one character.
func isAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }

// wholeWords joins a file's passages, in order, into one text without the
// words their cuts split. A passage after the first can start inside a word
// (the "eef" of "reef") and a full-length one can end inside one. Where the
// passages on either side overlap it, as consecutive passages do, the
// characters around the cut say whether it split a word, which is then whole
// in the neighbor; otherwise a word at the cut is taken off. Lengths and
// offsets count characters (code points), as Python's do.
func wholeWords(parts []string) string {
	over := ChunkChars - ChunkStep
	rs := make([][]rune, len(parts))
	for i, p := range parts {
		rs[i] = []rune(p)
	}
	eq := func(a, b []rune) bool {
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
	// Python slices clamp to the length.
	from := func(r []rune, i int) []rune {
		if i > len(r) {
			return nil
		}
		return r[i:]
	}
	upto := func(r []rune, i int) []rune {
		if i > len(r) {
			return r
		}
		return r[:i]
	}
	out := make([]string, 0, len(rs))
	for k, t := range rs {
		start, end := 0, len(t)
		var prev, next []rune
		hasPrev, hasNext := k > 0, k+1 < len(rs)
		if hasPrev {
			prev = rs[k-1]
		}
		if hasNext {
			next = rs[k+1]
		}
		if k > 0 && len(t) > 0 && isAlnum(t[0]) {
			joined := hasPrev && len(prev) == ChunkChars && eq(from(prev, ChunkStep), upto(t, over))
			if !joined || isAlnum(prev[ChunkStep-1]) {
				for start < end && isAlnum(t[start]) {
					start++
				}
			}
		}
		if len(t) >= ChunkChars && len(t) > 0 && isAlnum(t[len(t)-1]) {
			joined := hasNext && eq(from(t, ChunkStep), upto(next, over))
			if !joined || (len(next) > over && isAlnum(next[over])) {
				for end > start && isAlnum(t[end-1]) {
					end--
				}
			}
		}
		out = append(out, string(t[start:end]))
	}
	return strings.Join(out, " ")
}
