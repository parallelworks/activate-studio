package docwrite

import (
	"regexp"
	"strings"
	"unicode"
)

// A paragraph that opens with a label, a short phrase naming what the rest
// of the paragraph says ("Why now: the contract ends in March."), has the
// label set in italic with a colon, and the rest in roman type. Markdown
// sets such labels in many ways: in bold, ending in a period, or with the
// period or colon outside the emphasis. Each is set the one way, in body
// paragraphs, list items and quotes; front matter leaders: as-written keeps
// them as written. A key and its value ("Date: 14 November 2026"), or lines
// of them broken within one paragraph, are not labels and keep their form;
// a label is followed by a sentence.

// maxLeaderWords is the longest label taken, in words; a longer bold run
// is emphasis in a sentence, not a label.
const maxLeaderWords = 12

// numberedCaption is a figure's or a table's label, "Figure 2." or "Table
// 1:", which keeps its own form.
var numberedCaption = regexp.MustCompile(`(?i)^(figure|fig\.?|table|exhibit|chart|diagram)\s*[0-9ivx]`)

// italicLeaders sets the leading labels in bs, and in the lists and quotes
// inside them, and returns how many it set.
func italicLeaders(bs []Block) int {
	n := 0
	for i := range bs {
		b := &bs[i]
		switch b.Kind {
		case KPara:
			if rs, ok := leader(b.Runs); ok {
				b.Runs = rs
				n++
			}
		case KList:
			for _, item := range b.Items {
				n += italicLeaders(item)
			}
		case KQuote:
			n += italicLeaders(b.Children)
		}
	}
	return n
}

// leader is a paragraph's runs with its leading label set in italic with
// a colon, and whether it had one.
func leader(rs []Run) ([]Run, bool) {
	if len(rs) < 2 {
		return rs, false
	}
	first := rs[0]
	if !first.Bold && !first.Italic || first.Code || first.Link != "" || first.Strike || first.Sup {
		return rs, false
	}
	label := strings.TrimSpace(first.Text)
	next := rs[1]
	switch {
	case strings.HasSuffix(label, ":") || strings.HasSuffix(label, ".") && !strings.HasSuffix(label, ".."):
		label = strings.TrimSpace(label[:len(label)-1])
	case !next.Bold && !next.Italic && !next.Code && (strings.HasPrefix(next.Text, ":") || strings.HasPrefix(next.Text, ".") && !strings.HasPrefix(next.Text, "..")):
		next.Text = next.Text[1:]
	default:
		return rs, false
	}
	if !isLabel(label) {
		return rs, false
	}
	// The label is followed by a sentence: more words than a value has, or
	// an end of sentence, and no line broken within the paragraph.
	rest := next.Text
	for _, r := range rs[2:] {
		rest += r.Text
	}
	rest = strings.TrimSpace(rest)
	if rest == "" || strings.Contains(rest, "\n") {
		return rs, false
	}
	if len(strings.Fields(rest)) < 8 && !strings.ContainsAny(rest[len(rest)-1:], ".?!") {
		return rs, false
	}
	if next.Text != "" && !unicode.IsSpace([]rune(next.Text)[0]) {
		next.Text = " " + next.Text
	}
	out := make([]Run, 0, len(rs))
	out = append(out, Run{Text: label + ":", Italic: true})
	if next.Text != "" {
		out = append(out, next)
	}
	return append(out, rs[2:]...), true
}

// isLabel reports a phrase short enough to label a paragraph, with a
// letter in it, and no sentence of its own inside it.
func isLabel(s string) bool {
	if len([]rune(s)) < 2 || numberedCaption.MatchString(s) {
		return false
	}
	if n := len(strings.Fields(s)); n == 0 || n > maxLeaderWords {
		return false
	}
	if strings.ContainsAny(s, ":?!;") || strings.Contains(s, ". ") {
		return false
	}
	return strings.IndexFunc(s, unicode.IsLetter) >= 0
}
