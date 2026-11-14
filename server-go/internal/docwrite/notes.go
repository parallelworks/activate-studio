package docwrite

import (
	"regexp"
	"strings"
)

var noteDef = regexp.MustCompile(`^ {0,3}\[\^([^\]\s]+)\]:`)

// unusedNotes names the footnotes that are written but not referred to
// anywhere in the text. The Markdown parser leaves such a note out of the
// document, so the author is told and can put the reference in.
func unusedNotes(md string) []string {
	var labels []string
	var rest strings.Builder
	fence := ""
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(t, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = t[:3]
			continue
		}
		if m := noteDef.FindStringSubmatch(line); m != nil {
			labels = append(labels, m[1])
			line = line[len(m[0]):]
		}
		rest.WriteString(strings.ToLower(line))
		rest.WriteByte('\n')
	}
	text := rest.String()
	var out []string
	seen := map[string]bool{}
	for _, l := range labels {
		k := strings.ToLower(l)
		if seen[k] || strings.Contains(text, "[^"+k+"]") {
			seen[k] = true
			continue
		}
		seen[k] = true
		out = append(out, "footnote [^"+l+"] is written but not referred to, so it was left out; put [^"+l+"] in the text where it applies")
	}
	return out
}
