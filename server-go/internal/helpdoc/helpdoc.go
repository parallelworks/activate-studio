// Package helpdoc fills in the user guide for one deployment.
//
// docs/HELP.md is written to read on its own, on GitHub as well as in the
// app: it calls the app "Studio", its default name. A deployment given
// another name has that name in place of the word, except in "ACTIVATE
// Studio" (the product's full name, as in install paths) and inside code.
// The older placeholders {appName} and {kbLabel} are still filled in, and
// the comment <!-- kbLabel --> becomes a line naming the knowledge base.
package helpdoc

import (
	"regexp"
	"strings"
)

// DefaultName is the name the guide is written with.
const DefaultName = "Studio"

var word = regexp.MustCompile(`\b` + DefaultName + `\b`)

// Fill returns md for a deployment named appName whose main library is
// labelled kbLabel.
func Fill(md, appName, kbLabel string) string {
	md = strings.ReplaceAll(md, "{appName}", appName)
	md = strings.ReplaceAll(md, "{kbLabel}", kbLabel)
	md = strings.ReplaceAll(md, "<!-- kbLabel -->", "Knowledge base for this deployment: **"+kbLabel+"**")
	if appName == "" || appName == DefaultName {
		return md
	}
	lines := strings.SplitAfter(md, "\n")
	fenced := false
	for i, l := range lines {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fenced = !fenced
			continue
		}
		if !fenced {
			lines[i] = renameLine(l, appName)
		}
	}
	return strings.Join(lines, "")
}

// renameLine puts name in place of the default name in one line of
// Markdown, outside inline code and outside "ACTIVATE Studio".
func renameLine(l, name string) string {
	parts := strings.Split(l, "`")
	for i := 0; i < len(parts); i += 2 {
		p := parts[i]
		var b strings.Builder
		last := 0
		for _, m := range word.FindAllStringIndex(p, -1) {
			if strings.HasSuffix(p[:m[0]], "ACTIVATE ") {
				continue
			}
			b.WriteString(p[last:m[0]])
			b.WriteString(name)
			last = m[1]
		}
		b.WriteString(p[last:])
		parts[i] = b.String()
	}
	return strings.Join(parts, "`")
}
