// Package folders lists a directory's folders and counts a directory's
// files, on this computer or with a shell script on another, for the
// pages that choose a folder: the composer's folder picker and Add a
// library.
package folders

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Limit is the most folders one listing answers with.
const Limit = 1000

// Entry is one folder of a listing.
type Entry struct {
	Name       string `json:"name"`
	Repository bool   `json:"repository,omitempty"`
}

// Listing is a machine's folders as pw code lists them (AgentDirectories).
type Listing struct {
	Path      string  `json:"path"`
	Home      string  `json:"home,omitempty"`
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated,omitempty"`
}

// Filter keeps the folders whose names hold filter, in order of name, at
// most Limit of them.
func (l *Listing) Filter(filter string) *Listing {
	filter = strings.ToLower(filter)
	kept := []Entry{}
	for _, e := range l.Entries {
		if strings.Contains(strings.ToLower(e.Name), filter) {
			kept = append(kept, e)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return strings.ToLower(kept[i].Name) < strings.ToLower(kept[j].Name) })
	if len(kept) > Limit {
		kept, l.Truncated = kept[:Limit], true
	}
	l.Entries = kept
	return l
}

func expand(dir string) (string, string) {
	home, _ := os.UserHomeDir()
	switch {
	case dir == "" || dir == "~":
		dir = home
	case strings.HasPrefix(dir, "~/"):
		dir = filepath.Join(home, dir[2:])
	}
	return dir, home
}

// Local lists a directory of this computer; nil when it cannot be.
func Local(dir string) *Listing {
	dir, home := expand(dir)
	if !filepath.IsAbs(dir) {
		return nil
	}
	dir = filepath.Clean(dir)
	es, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	l := &Listing{Path: dir, Home: home, Entries: []Entry{}}
	for _, e := range es {
		p := filepath.Join(dir, e.Name())
		isDir := e.IsDir()
		if !isDir && e.Type()&os.ModeSymlink != 0 {
			st, err := os.Stat(p)
			isDir = err == nil && st.IsDir()
		}
		if isDir {
			_, err := os.Lstat(filepath.Join(p, ".git"))
			l.Entries = append(l.Entries, Entry{Name: e.Name(), Repository: err == nil})
		}
	}
	return l
}

// Script lists a directory's folders, one a line, as Parse reads them. Its
// argument is the directory.
const Script = `p=$1
case "$p" in
'' | '~') p=$HOME ;;
'~/'*) p=$HOME/${p#??} ;;
/*) ;;
*) exit 0 ;;
esac
cd -- "$p" 2>/dev/null && [ -r . ] || exit 0
printf 'home=%s\npath=%s\n' "$HOME" "$(pwd)"
n=0
for d in .* *; do
  case "$d" in . | ..) continue ;; esac
  [ -d "$d" ] || continue
  n=$((n+1))
  [ $n -gt 5000 ] && { echo more=1; break; }
  if [ -e "$d/.git" ]; then printf 'repo=%s\n' "$d"; else printf 'dir=%s\n' "$d"; fi
done
`

// Parse is Script's answer; nil when it listed nothing.
func Parse(out string) *Listing {
	l := &Listing{Entries: []Entry{}}
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(strings.TrimRight(line, "\r"), "=")
		switch {
		case v == "":
		case k == "home":
			l.Home = v
		case k == "path":
			l.Path = v
		case k == "more":
			l.Truncated = true
		case k == "dir" || k == "repo":
			l.Entries = append(l.Entries, Entry{Name: v, Repository: k == "repo"})
		}
	}
	if l.Path == "" {
		return nil
	}
	return l
}
