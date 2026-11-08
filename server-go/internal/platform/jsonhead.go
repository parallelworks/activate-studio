package platform

import (
	"errors"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// The pw CLI appends upgrade notices after its JSON, so callers take the
// first balanced value. Two spellings exist in Node, which differ only when
// the value is never closed.

func balanced(text string) (start, end int, found bool) {
	start = strings.IndexAny(text, "[{")
	if start < 0 {
		return -1, -1, false
	}
	open := text[start]
	close := byte('}')
	if open == '[' {
		close = ']'
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(text); i++ {
		c := text[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return start, i + 1, true
			}
		}
	}
	return start, len(text), false
}

// FirstJSON is workflowsTab.ts's firstJson.
func FirstJSON(text string) (any, error) {
	s, e, ok := balanced(text)
	if s < 0 {
		return nil, errors.New("no JSON in CLI output")
	}
	if !ok {
		return nil, errors.New("unterminated JSON in CLI output")
	}
	return jsonv.DecodeString(text[s:e])
}

// JSONHead is tasks.ts's jsonHead: an unclosed value is parsed as it
// stands (and fails as JSON.parse would).
func JSONHead(text string) (any, error) {
	s, e, ok := balanced(text)
	if s < 0 {
		return nil, errors.New("no JSON in CLI output: " + sliceUnits(text, 120))
	}
	if !ok {
		return jsonv.DecodeString(text[s:])
	}
	return jsonv.DecodeString(text[s:e])
}

// sliceUnits is text.slice(0, n), by runes.
func sliceUnits(text string, n int) string { return sliceRunes(text, n) }
