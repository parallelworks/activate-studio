package docwrite

import (
	"fmt"
	"regexp"
	"strings"
)

// StyleCheck reads a document's Markdown for constructions that plain,
// factual writing avoids, and returns advice on each one found, with how
// often it occurs. Counting is the check: rereading a draft for tone
// misses what a count shows. Code, front matter, and the lines that rule
// a table are left out. The advice is for the author to weigh; a word the
// check counts can be the right word.
func StyleCheck(md string) []string {
	_, body, _ := SplitFrontMatter(md)
	text := proseOf(body)
	var out []string
	add := func(n int, one, many, advice string) {
		switch {
		case n == 1:
			out = append(out, one+" ("+advice+")")
		case n > 1:
			out = append(out, fmt.Sprintf(many, n)+" ("+advice+")")
		}
	}
	add(strings.Count(text, "—")+len(enDash.FindAllString(text, -1)), "a dash used as punctuation (— or –)", "%d dashes used as punctuation (— or –)", "use a comma, a colon, parentheses or two sentences")
	add(len(doubleHyphen.FindAllString(text, -1)), "a double hyphen (--)", "%d double hyphens (--)", "use a comma, a colon, parentheses or two sentences")
	if words := intensifiers(text); len(words) > 0 {
		out = append(out, "intensifiers: "+strings.Join(words, ", ")+" (cut each one that adds no meaning)")
	}
	if n := len(ratherThan.FindAllString(text, -1)); n > 1 {
		out = append(out, fmt.Sprintf(`"rather than" %d times (keep it where two options are compared)`, n))
	}
	add(len(notContrast.FindAllString(text, -1)), `an "X, not Y" contrast`, `%d "X, not Y" contrasts`, "state what the thing is")
	if rep := repeated(text); len(rep) > 0 {
		out = append(out, fmt.Sprintf("%d sentence%s written twice or more, such as %q (state each fact once, where the reader will look for it)", len(rep), plural(len(rep)), rep[0]))
	}
	if total, name, most := sectionWords(body); total >= 1000 {
		out = append(out, fmt.Sprintf("%d words, the longest section %q at %d (cut what does no work for the reader)", total, name, most))
	}
	return out
}

var (
	enDash       = regexp.MustCompile(`\s–\s`)
	doubleHyphen = regexp.MustCompile(`\w--\w|\s--\s|\s--$`)
	ratherThan   = regexp.MustCompile(`(?i)\brather than\b`)
	notContrast  = regexp.MustCompile(`(?i)[a-z0-9)"'”],\s+not\s+\w`)
	intensifier  = regexp.MustCompile(`(?i)\b(truly|genuinely|genuine|actually|really|properly|real)\b(-| )?([a-z]*)`)
	fenceLine    = regexp.MustCompile("^\\s*(```|~~~)")
	ruleLine     = regexp.MustCompile(`^\s*[-|:=*_ ]+\s*$`)
	inlineCode   = regexp.MustCompile("`[^`]*`")
	sentenceEnd  = regexp.MustCompile(`[.!?]+(\s+|$)`)
	nonWord      = regexp.MustCompile(`[^a-z0-9 ]+`)
)

// proseOf is the Markdown's text without code blocks, inline code, and the
// lines that are only rules or a table's dividing row.
func proseOf(md string) string {
	var b strings.Builder
	inFence := false
	for _, l := range strings.Split(md, "\n") {
		if fenceLine.MatchString(l) {
			inFence = !inFence
			continue
		}
		if inFence || ruleLine.MatchString(l) {
			continue
		}
		b.WriteString(inlineCode.ReplaceAllString(l, " "))
		b.WriteByte('\n')
	}
	return b.String()
}

// intensifiers counts the words used to add emphasis, as "truly 2". "Real"
// in "real time", "real-time", "real world" and the like names a thing and
// is not counted.
func intensifiers(text string) []string {
	counts := map[string]int{}
	var order []string
	for _, m := range intensifier.FindAllStringSubmatch(text, -1) {
		w := strings.ToLower(m[1])
		if w == "real" {
			switch strings.ToLower(m[3]) {
			case "time", "world", "estate", "number", "numbers", "data", "users", "hardware":
				continue
			}
		}
		if counts[w] == 0 {
			order = append(order, w)
		}
		counts[w]++
	}
	out := make([]string, len(order))
	for i, w := range order {
		out[i] = fmt.Sprintf("%q %d", w, counts[w])
	}
	return out
}

// repeated is the sentences of eight words or more that appear more than
// once, in their first wording.
func repeated(text string) []string {
	seen := map[string]int{}
	first := map[string]string{}
	var order []string
	for _, para := range strings.Split(text, "\n") {
		para = strings.TrimLeft(strings.TrimSpace(para), "#>-*+0123456789. |")
		for _, s := range sentenceEnd.Split(para, -1) {
			s = strings.TrimSpace(s)
			key := strings.Join(strings.Fields(nonWord.ReplaceAllString(strings.ToLower(s), " ")), " ")
			if len(strings.Fields(key)) < 8 {
				continue
			}
			if seen[key] == 0 {
				first[key] = s
			}
			seen[key]++
			if seen[key] == 2 {
				order = append(order, key)
			}
		}
	}
	out := make([]string, len(order))
	for i, k := range order {
		s := first[k]
		if r := []rune(s); len(r) > 80 {
			s = string(r[:77]) + "..."
		}
		out[i] = s
	}
	return out
}

// sectionWords is the document's word count, and its longest section by
// words under a heading of the top two levels, with that section's count.
func sectionWords(md string) (total int, name string, most int) {
	cur, n := "the opening", 0
	flush := func() {
		if n > most {
			name, most = cur, n
		}
	}
	inFence := false
	for _, l := range strings.Split(md, "\n") {
		if fenceLine.MatchString(l) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "# ") || strings.HasPrefix(t, "## ") {
			flush()
			cur, n = strings.TrimSpace(strings.TrimLeft(t, "#")), 0
			continue
		}
		if ruleLine.MatchString(l) {
			continue
		}
		k := len(strings.Fields(strings.NewReplacer("|", " ", "*", "", "_", " ", "#", "").Replace(t)))
		n += k
		total += k
	}
	flush()
	return total, name, most
}
