package docwrite

import (
	"fmt"
	"strings"
	"sync"

	"golang.org/x/image/font/sfnt"
)

// A PDF draws only the characters its font holds; the PDF library leaves
// a gap for any other without saying so. Text is therefore checked
// against the font before it is drawn: a character the font lacks is
// replaced by an equivalent it has, or by a visible placeholder, and the
// placeholders are reported.

// coverage reports whether a font face can draw a character.
type coverage func(rune) bool

// faceCoverage reads a TrueType face's character map. A face that cannot
// be read gives nil, which stands for a face that draws everything, as
// the writer assumed before coverage was checked.
func faceCoverage(ttf []byte) coverage {
	f, err := sfnt.Parse(ttf)
	if err != nil {
		return nil
	}
	var (
		mu   sync.Mutex
		buf  sfnt.Buffer
		seen = map[rune]bool{}
	)
	return func(r rune) bool {
		// The PDF library reads the basic plane of a font only.
		if r > 0xFFFF {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		if ok, hit := seen[r]; hit {
			return ok
		}
		g, err := f.GlyphIndex(&buf, r)
		ok := err == nil && g != 0
		seen[r] = ok
		return ok
	}
}

// cp1252Extra is what Windows-1252 holds beyond Latin-1.
const cp1252Extra = "\u20ac\u201a\u0192\u201e\u2026\u2020\u2021\u02c6\u2030\u0160\u2039\u0152\u017d\u2018\u2019\u201c\u201d\u2022\u2013\u2014\u02dc\u2122\u0161\u203a\u0153\u017e\u0178"

// standardCoverage is what the standard PDF fonts draw: Windows-1252.
func standardCoverage(r rune) bool {
	return r >= 0x20 && r < 0x7F || r >= 0xA0 && r <= 0xFF || strings.ContainsRune(cp1252Extra, r)
}

// substitutes lists, for a character a font may lack, what to draw in its
// place, best first. The first whose characters the font all has is used.
var substitutes = map[rune][]string{
	// Hyphens and dashes.
	'\u2010': {"-"}, '\u2011': {"-"}, '\u2012': {"\u2013", "-"}, '\u2013': {"-"}, '\u2014': {"\u2013", "-"},
	'\u2015': {"\u2014", "\u2013", "-"}, '−': {"\u2013", "-"}, '\ufe63': {"-"}, '\uff0d': {"-"},
	// Spaces.
	'\u00a0': {" "}, '\u2000': {" "}, '\u2001': {" "}, '\u2002': {" "}, '\u2003': {" "}, '\u2004': {" "},
	'\u2005': {" "}, '\u2006': {" "}, '\u2007': {"\u00a0", " "}, '\u2008': {" "}, '\u2009': {" "},
	'\u200a': {" "}, '\u202f': {"\u00a0", " "}, '\u205f': {" "}, '\u3000': {" "},
	// Quotes and primes.
	'\u2018': {"'"}, '\u2019': {"'"}, '\u201a': {","}, '\u201b': {"'"}, '\u201c': {`"`}, '\u201d': {`"`},
	'\u201e': {`"`}, '\u201f': {`"`}, '«': {`"`}, '»': {`"`},
	'\u2039': {"<"}, '\u203a': {">"},
	// Arrows.
	'→': {"->"}, '←': {"<-"}, '↔': {"<->"}, '⇒': {"=>"}, '⇐': {"<="}, '⇔': {"<=>"}, '➜': {"→", "->"},
	'➔': {"→", "->"}, '➝': {"→", "->"}, '⟶': {"→", "->"}, '⟵': {"←", "<-"}, '↑': {"^"}, '↓': {"v"},
	// Check and cross marks, and boxes.
	'✓': {"✔", "√", "[x]"}, '✔': {"✓", "√", "[x]"}, '✅': {"✓", "✔", "√", "[x]"}, '☑': {"[x]"},
	'✗': {"✘", "×", "x"}, '✘': {"✗", "×", "x"}, '❌': {"✗", "✘", "×", "x"}, '✕': {"×", "x"},
	'✖': {"×", "x"}, '☒': {"[x]"}, '☐': {"[ ]"},
	// Bullets.
	'\u2022': {"·", "-"}, '◦': {"\u2022", "-"}, '▪': {"\u2022", "-"}, '▫': {"\u2022", "-"}, '●': {"\u2022", "-"}, '○': {"◦", "\u2022", "-"},
	'■': {"\u2022", "-"}, '□': {"[ ]"}, '\u2023': {"\u2022", "-"}, '∙': {"·", "\u2022", "-"}, '·': {"-"},
	'★': {"*"}, '☆': {"*"},
	// Mathematics and marks.
	'\u2026': {"..."}, '≥': {">="}, '≤': {"<="}, '≠': {"!="}, '≈': {"~"}, '×': {"x"}, '÷': {"/"}, '±': {"+/-"},
	'∞': {"inf"}, '√': {"sqrt"}, '∼': {"~"}, '⋅': {"·", "*"}, '∗': {"*"},
	'™': {"(TM)"}, '©': {"(c)"}, '®': {"(R)"}, '°': {" deg"}, 'µ': {"μ", "u"}, 'μ': {"µ", "u"},
	'€': {"EUR"}, '£': {"GBP"}, '¢': {"c"}, '½': {"1/2"}, '¼': {"1/4"}, '¾': {"3/4"},
	'²': {"^2"}, '³': {"^3"}, '¹': {"^1"}, '№': {"No."}, '§': {"S."}, '\u2030': {"o/oo"},
	'\u2028': {" "}, '\u2029': {" "},
}

// invisible reports a character that draws nothing and may be left out:
// zero-width marks, the soft hyphen and variation selectors.
func invisible(r rune) bool {
	switch {
	case r == '\u00ad', r >= '\u200b' && r <= '\u200f', r >= '\u202a' && r <= '\u202e',
		r >= '\u2060' && r <= '\u2064', r == '\ufeff', r >= '\ufe00' && r <= '\ufe0f':
		return true
	}
	return false
}

// glyphs fits text to the faces of one document, and keeps what it could
// not fit.
type glyphs struct {
	missing []rune // each character replaced by a placeholder, once, in order
	seen    map[rune]bool
}

// fit returns s with every character cov cannot draw replaced. A nil cov
// draws everything.
func (g *glyphs) fit(s string, cov coverage) string {
	if cov == nil {
		return s
	}
	clean := true
	for _, r := range s {
		if r != '\n' && r != '\t' && !cov(r) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var sb strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\t' || cov(r) {
			sb.WriteRune(r)
			continue
		}
		if invisible(r) {
			continue
		}
		if sub, ok := substitute(r, cov); ok {
			sb.WriteString(sub)
			continue
		}
		if g.seen == nil {
			g.seen = map[rune]bool{}
		}
		if !g.seen[r] {
			g.seen[r] = true
			g.missing = append(g.missing, r)
		}
		sb.WriteString(placeholder(cov))
	}
	return sb.String()
}

func substitute(r rune, cov coverage) (string, bool) {
	for _, sub := range substitutes[r] {
		ok := true
		for _, c := range sub {
			ok = ok && cov(c)
		}
		if ok {
			return sub, true
		}
	}
	return "", false
}

// placeholder is what stands for a character with no equivalent: the
// replacement character when the font has it, or a question mark.
func placeholder(cov coverage) string {
	if cov('\ufffd') {
		return "\ufffd"
	}
	return "?"
}

// problem says which characters were replaced by placeholders, or "" when
// none were.
func (g *glyphs) problem() string {
	n := len(g.missing)
	if n == 0 {
		return ""
	}
	const show = 12
	var list []string
	for i, r := range g.missing {
		if i == show {
			list = append(list, fmt.Sprintf("and %d more", n-show))
			break
		}
		list = append(list, fmt.Sprintf("%c (U+%04X)", r, r))
	}
	if n == 1 {
		return "1 character is not in the PDF font and was replaced by a placeholder: " + list[0]
	}
	return fmt.Sprintf("%d characters are not in the PDF font and were replaced by a placeholder: %s", n, strings.Join(list, ", "))
}
