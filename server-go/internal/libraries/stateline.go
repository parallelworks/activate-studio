package libraries

import "strings"

// StateLine is libraryStateLine (omissions.ts): one line per library for
// the server and index logs at start, with what it can do and, when its
// state leaves it out of search, why.
func (r *Registry) StateLine(l Library) string {
	yes := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	parts := []string{"index " + yes(l.Caps.Index), "words " + yes(l.Caps.FullText), "vectors " + yes(l.Caps.Vectors)}
	if l.Depth != "" {
		parts = append(parts, "depth "+l.Depth)
	}
	if l.Format != nil && l.Format.Format == "older" {
		p := "written by GUFI 0.7.0 or earlier"
		if l.Format.RolledUp != nil && *l.Format.RolledUp {
			p += ", rolled up"
		}
		parts = append(parts, p)
	}
	if l.Build != nil {
		p := "build " + l.Build.State
		if l.Build.BuiltAt != nil && *l.Build.BuiltAt != "" {
			p += " (built " + *l.Build.BuiltAt + ")"
		}
		parts = append(parts, p)
	}
	if l.Site != "" {
		parts = append(parts, "on "+l.Site)
	}
	if k := r.Known(l.IndexRoot); k != nil && k.Stale {
		p := "capabilities stale"
		if k.Error != "" {
			p += " (" + k.Error + ")"
		}
		parts = append(parts, p)
	}
	line := "library " + l.ID + " (" + l.Label + "): " + strings.Join(parts, ", ")
	if gap := r.Gap(l, true, true); gap != nil {
		if gap.Partial {
			line += "; searched in part: " + gap.Reason
		} else {
			line += "; left out of search: " + gap.Reason
		}
	}
	return line
}
