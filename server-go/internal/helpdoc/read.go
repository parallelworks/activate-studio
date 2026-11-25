package helpdoc

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Request is what a reader of a document asks for: the whole of it, one
// section, or the passages that have some words.
type Request struct {
	Section, Find string
	// Part is which part of what is too long for one answer, from 1.
	Part int
}

// section is a heading with the text under it, up to the next heading.
type section struct {
	level       int
	title, body string
	// under is the page (the ## heading) a ### heading belongs to.
	under string
}

func (s section) name() string {
	if s.under != "" {
		return s.under + ", " + s.title
	}
	return s.title
}

var (
	heading = regexp.MustCompile(`^(#{1,3}) +(.+?) *$`)
	aiMark  = regexp.MustCompile(`<!-- /?ai -->`)
)

// split is md's headings, the text before the first one under "". A #
// heading groups the pages after it and has no text of its own.
func split(md string) []section {
	out := []section{{}}
	var b strings.Builder
	end := func() { out[len(out)-1].body = strings.TrimSpace(b.String()); b.Reset() }
	fenced, page := false, ""
	for _, l := range strings.Split(md, "\n") {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fenced = !fenced
		}
		m := heading.FindStringSubmatch(l)
		if m == nil || fenced {
			b.WriteString(l + "\n")
			continue
		}
		end()
		s := section{level: len(m[1]), title: m[2]}
		switch s.level {
		case 3:
			s.under = page
		default:
			page = ""
			if s.level == 2 {
				page = s.title
			}
		}
		out = append(out, s)
	}
	end()
	return out
}

// Read answers req from md in at most limit characters. A document that does
// not fit is answered with its opening and the names of its sections.
func Read(md string, req Request, limit int) string {
	md = strings.TrimSpace(aiMark.ReplaceAllString(md, ""))
	secs := split(md)
	switch {
	case strings.TrimSpace(req.Section) != "":
		text, ok := one(secs, req.Section)
		if !ok {
			return cut("No section is named \""+strings.TrimSpace(req.Section)+"\".\n\n"+contents(secs), limit)
		}
		return part(text, req.Part, limit)
	case strings.TrimSpace(req.Find) != "":
		if text := find(secs, req.Find, limit); text != "" {
			return text
		}
		return cut("Nothing in it has those words.\n\n"+contents(secs), limit)
	case len([]rune(md)) <= limit:
		return md
	case req.Part > 1:
		return part(md, req.Part, limit)
	}
	list := contents(secs)
	return cut(opening(secs), limit-len([]rune(list))-2) + "\n\n" + list
}

// opening is what comes before a document's first page.
func opening(secs []section) string {
	var b []string
	for _, s := range secs {
		if s.level == 2 || s.level == 1 && len(b) > 0 {
			break
		}
		if t := s.text(); t != "" {
			b = append(b, t)
		}
	}
	return strings.Join(b, "\n\n")
}

func (s section) text() string {
	if s.level == 0 {
		return s.body
	}
	return strings.TrimSpace(strings.Repeat("#", s.level) + " " + s.title + "\n\n" + s.body)
}

// contents names the sections, so the next call can ask for one.
func contents(secs []section) string {
	b := []string{"This is not the whole document. Call again with section set to one of these, or with find set to the words to look for:", ""}
	for _, s := range secs {
		switch s.level {
		case 2:
			b = append(b, "- "+s.title)
		case 3:
			if s.under != "" {
				b = append(b, "  - "+s.title)
			} else {
				b = append(b, "- "+s.title)
			}
		}
	}
	return strings.Join(b, "\n")
}

// one is the section named, with those under it.
func one(secs []section, name string) (string, bool) {
	want := fold(name)
	for _, same := range []func(section) bool{
		func(s section) bool { return fold(s.title) == want || fold(s.name()) == want },
		func(s section) bool { return strings.Contains(fold(s.title), want) },
		func(s section) bool { return len(want) > 3 && strings.Contains(want, fold(s.title)) },
	} {
		i := slices.IndexFunc(secs, func(s section) bool { return s.level > 0 && same(s) })
		if i < 0 {
			continue
		}
		b := []string{secs[i].text()}
		for _, s := range secs[i+1:] {
			if s.level <= secs[i].level {
				break
			}
			b = append(b, s.text())
		}
		return strings.Join(b, "\n\n"), true
	}
	return "", false
}

func fold(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.Trim(strings.TrimSpace(s), "#\"'“”. "))), " ")
}

var (
	wordRE = regexp.MustCompile(`[\p{L}\p{N}]+`)
	common = map[string]bool{"the": true, "and": true, "how": true, "for": true, "can": true, "does": true, "what": true, "with": true, "you": true, "are": true, "this": true, "that": true, "where": true, "when": true, "from": true, "into": true, "have": true, "use": true, "studio": true, "there": true, "someone": true, "else": true}
)

// words is the words of s worth looking for, each without the endings that
// would keep "shared folders" from finding "share a folder".
func words(s string) []string {
	var out []string
	for _, w := range wordRE.FindAllString(strings.ToLower(s), -1) {
		if len(w) < 3 || common[w] {
			continue
		}
		if len(w) > 4 {
			for _, end := range []string{"ing", "ed", "es", "s", "e"} {
				if t := strings.TrimSuffix(w, end); t != w && len(t) >= 4 {
					w = t
					break
				}
			}
		}
		if !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

// foundPassages is how many passages an answer to find has at most.
const foundPassages = 14

// find is the passages that have the most of the words, a rare word
// counting for more, in the document's order under their headings.
func find(secs []section, query string, limit int) string {
	want := words(query)
	if len(want) == 0 {
		return ""
	}
	type passage struct {
		sec, at int
		text    string
		score   float64
	}
	var all []passage
	has := func(words []string, w string) bool {
		return slices.ContainsFunc(words, func(x string) bool { return strings.HasPrefix(x, w) })
	}
	for i, s := range secs {
		if s.level == 1 {
			continue
		}
		for j, p := range passages(s.body) {
			all = append(all, passage{sec: i, at: j, text: p})
		}
	}
	bodies, heads := make([][]string, len(all)), make([][]string, len(all))
	df := map[string]int{}
	for i, p := range all {
		bodies[i] = wordRE.FindAllString(strings.ToLower(p.text), -1)
		heads[i] = wordRE.FindAllString(strings.ToLower(secs[p.sec].name()), -1)
		for _, w := range want {
			if has(bodies[i], w) || has(heads[i], w) {
				df[w]++
			}
		}
	}
	for i := range all {
		for _, w := range want {
			idf := math.Log(1 + float64(len(all))/float64(1+df[w]))
			if has(bodies[i], w) {
				all[i].score += idf
			}
			if has(heads[i], w) {
				all[i].score += idf
			}
		}
	}
	all = slices.DeleteFunc(all, func(p passage) bool { return p.score == 0 })
	sort.SliceStable(all, func(a, b int) bool { return all[a].score > all[b].score })
	if len(all) > foundPassages {
		all = all[:foundPassages]
	}
	for len(all) > 1 {
		n := 0
		for _, p := range all {
			n += len([]rune(p.text)) + 80
		}
		if n <= limit/2 {
			break
		}
		all = all[:len(all)-1]
	}
	sort.SliceStable(all, func(a, b int) bool {
		if all[a].sec != all[b].sec {
			return all[a].sec < all[b].sec
		}
		return all[a].at < all[b].at
	})
	var b strings.Builder
	last := -1
	for _, p := range all {
		if p.sec != last {
			name := secs[p.sec].name()
			if name == "" {
				name = "Opening"
			}
			fmt.Fprintf(&b, "## %s\n\n", name)
			last = p.sec
		}
		b.WriteString(p.text + "\n\n")
	}
	if len(all) == 0 {
		return ""
	}
	return cut(strings.TrimSpace(b.String()), limit)
}

var listItem = regexp.MustCompile(`^([-*]|\d+\.) `)

// passages is a section's paragraphs and list items, an item with what is
// indented under it.
func passages(body string) []string {
	var out []string
	var cur []string
	end := func() {
		if t := strings.TrimSpace(strings.Join(cur, "\n")); t != "" {
			out = append(out, t)
		}
		cur = nil
	}
	for _, l := range strings.Split(body, "\n") {
		if strings.TrimSpace(l) == "" || listItem.MatchString(l) {
			end()
		}
		cur = append(cur, l)
	}
	end()
	return out
}

// part is part n of text, cut between lines into pieces of at most limit.
func part(text string, n, limit int) string {
	if len([]rune(text)) <= limit {
		return text
	}
	room := limit - 100
	var parts []string
	cur := ""
	for _, l := range strings.SplitAfter(text, "\n") {
		for len([]rune(l)) > room {
			r := []rune(l)
			if cur != "" {
				parts, cur = append(parts, cur), ""
			}
			parts, l = append(parts, string(r[:room])), string(r[room:])
		}
		if len([]rune(cur))+len([]rune(l)) > room {
			parts, cur = append(parts, cur), ""
		}
		cur += l
	}
	if strings.TrimSpace(cur) != "" {
		parts = append(parts, cur)
	}
	n = min(max(n, 1), len(parts))
	note := fmt.Sprintf("[Part %d of %d", n, len(parts))
	if n < len(parts) {
		note += fmt.Sprintf("; call again with part %d for the next", n+1)
	}
	return strings.TrimSpace(parts[n-1]) + "\n\n" + note + ".]"
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:max(n, 0)]))
	}
	return s
}
