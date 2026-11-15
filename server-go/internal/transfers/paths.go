package transfers

import (
	"path"
	"strings"
	"unicode/utf8"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// CleanRel checks a path relative to a location's root and returns it in
// one spelling: slash separated, no leading or trailing slash, "" for the
// root itself ("." and "/" name it too). Refused (400): an absolute path,
// "..", a control character, invalid UTF-8, and a hidden name (one starting
// with a dot) anywhere in it, since hidden entries are never transferred or
// browsed into.
func CleanRel(rel string) (string, error) {
	// "", "." and "/" all name the root.
	if t := strings.TrimSpace(rel); t == "/" || t == "." {
		return "", nil
	}
	if strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "\\") || (len(rel) > 1 && rel[1] == ':') {
		return "", apierr.New(400, "%q: give a path inside the location, not an absolute one", short(rel))
	}
	if !utf8.ValidString(rel) || len(rel) > 4096 {
		return "", apierr.New(400, "a path is not valid text or is too long")
	}
	for _, r := range rel {
		if r < 0x20 || r == 0x7f {
			return "", apierr.New(400, "a path holds a control character")
		}
	}
	var parts []string
	for _, seg := range strings.Split(rel, "/") {
		switch {
		case seg == "" || seg == ".":
			continue
		case seg == "..":
			return "", apierr.New(400, "%q: \"..\" is not allowed in a path", short(rel))
		case strings.HasPrefix(seg, "."):
			return "", apierr.New(400, "%q: hidden names are not transferred", short(rel))
		}
		parts = append(parts, seg)
	}
	return strings.Join(parts, "/"), nil
}

// join is rel under base, both clean.
func join(base, rel string) string {
	switch {
	case base == "":
		return rel
	case rel == "":
		return base
	}
	return base + "/" + rel
}

// within reports whether p is base or under it (clean relative paths).
func within(p, base string) bool {
	return base == "" || p == base || strings.HasPrefix(p, base+"/")
}

// parentOf is the folder holding p, "" for one at the root.
func parentOf(p string) string {
	d := path.Dir(p)
	if d == "." {
		return ""
	}
	return d
}

// hiddenSeg reports whether any segment of a relative path is hidden.
func hiddenSeg(p string) bool {
	for _, s := range strings.Split(p, "/") {
		if strings.HasPrefix(s, ".") {
			return true
		}
	}
	return false
}

func short(s string) string {
	if len([]rune(s)) > 80 {
		return string([]rune(s)[:80]) + "..."
	}
	return s
}
