package chat

import (
	"reflect"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
)

func TestDistill(t *testing.T) {
	for in, want := range map[string][]string{
		"What does the budget forecast say for the quarter?": {"budget", "forecast", "say", "quarter"},
		"Tell me about the Q1 budget, budget and BUDGET!":    {"budget"},
		"a b c":                    {},
		"café_menu prix-fixe 2026": {"caf", "_menu", "prix", "fixe", "2026"},
		"one two three four five six seven eight nine ten eleven twelve": {"four", "five", "six", "seven", "eight", "nine", "ten", "eleven"},
	} {
		if got := distill(in); !reflect.DeepEqual(got, want) {
			t.Errorf("distill(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBlendHits(t *testing.T) {
	h := func(p, mode string) gufi.SearchHit { return gufi.SearchHit{Path: p, Mode: mode} }
	fts := []gufi.SearchHit{h("a", "fts"), h("b", "fts"), h("c", "fts"), h("d", "fts"), h("e", "fts"), h("f", "fts")}
	names := []gufi.SearchHit{h("n", "name"), h("a", "name")}
	vec := []gufi.SearchHit{h("v", "vector"), h("b", "vector"), h("w", "vector")}
	var paths, modes []string
	for _, x := range blendHits(fts, names, vec, 8) {
		paths = append(paths, x.Path)
		modes = append(modes, x.Mode)
	}
	// limit 8: two meaning hits first-ranked, six words hits, then names.
	if want := []string{"a", "b", "c", "d", "e", "f", "v", "n"}; !reflect.DeepEqual(paths, want) {
		t.Errorf("paths %q, want %q", paths, want)
	}
	// A path's mode is the last list it was found in: vec, names, fts.
	if want := []string{"fts", "fts", "fts", "fts", "fts", "fts", "vector", "name"}; !reflect.DeepEqual(modes, want) {
		t.Errorf("modes %q, want %q", modes, want)
	}
	if got := blendHits(nil, nil, nil, 5); len(got) != 0 {
		t.Errorf("empty: %v", got)
	}
}
