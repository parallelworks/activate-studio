package updates

// Release notes and the change log, as entries a person reads, parsed as
// server/src/changelog.ts does. Both come from scripts/release-notes.mjs: a
// release's notes are one section per pull request, `### <title> (#N)` with
// its description below, and CHANGELOG.md holds the same for every version
// under `## vX.Y (date)`, newest first. Entries titled "Change log through
// vX" only regenerate the change log and are left out.

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ChangeEntry is one pull request's entry.
type ChangeEntry struct {
	Title string `json:"title"`
	PR    *int   `json:"pr"`
	Body  string `json:"body"`
}

// ChangeVersion is one version of the change log.
type ChangeVersion struct {
	Version string        `json:"version"`
	Date    *string       `json:"date"`
	Entries []ChangeEntry `json:"entries"`
}

var (
	entryRe     = regexp.MustCompile(`^###\s+(.*?)\s*\(#(\d+)\)\s*$`)
	bareEntryRe = regexp.MustCompile(`^###\s+#(\d+)\s*$`)
	versionRe   = regexp.MustCompile(`^##\s+(v\d+\.\d+(?:\.\d+)?\S*)(?:\s+\(([^)]*)\))?\s*$`)
	openFence   = regexp.MustCompile("^\\s{0,3}(`{3,}|~{3,})")
	closeFence  = regexp.MustCompile("^\\s{0,3}(`{3,}|~{3,})\\s*$")
	housekeep   = regexp.MustCompile(`(?i)^change log through\b`)
	tagOnly     = regexp.MustCompile(`^_No pull requests; tag only\._$`)
)

type mdLine struct {
	text string
	code bool
}

// mdLines is each line, and whether it is inside a fenced code block, where
// a line that looks like a heading is not one.
func mdLines(md string) []mdLine {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	md = strings.ReplaceAll(md, "\r", "\n")
	var out []mdLine
	fence := ""
	for _, line := range strings.Split(md, "\n") {
		if fence != "" {
			if m := closeFence.FindStringSubmatch(line); m != nil && m[1][0] == fence[0] && len(m[1]) >= len(fence) {
				fence = ""
			}
			out = append(out, mdLine{line, true})
			continue
		}
		if m := openFence.FindStringSubmatch(line); m != nil {
			fence = m[1]
			out = append(out, mdLine{line, true})
			continue
		}
		out = append(out, mdLine{line, false})
	}
	return out
}

// jsTrim is String.prototype.trim for the whitespace that occurs here.
func jsTrim(s string) string { return strings.TrimSpace(s) }

// ParseNotes is one release's notes as entries. Notes with no entry
// headings, such as a release written by hand, are one entry titled
// "Release notes".
func ParseNotes(md string) []ChangeEntry {
	type found struct {
		title string
		pr    int
		body  []string
	}
	var fs []*found
	var before []string
	for _, l := range mdLines(md) {
		var m, bare []string
		if !l.code {
			m = entryRe.FindStringSubmatch(l.text)
			if m == nil {
				bare = bareEntryRe.FindStringSubmatch(l.text)
			}
		}
		switch {
		case m != nil:
			n, _ := strconv.Atoi(m[2])
			fs = append(fs, &found{title: jsTrim(m[1]), pr: n})
		case bare != nil:
			n, _ := strconv.Atoi(bare[1])
			fs = append(fs, &found{title: "Pull request #" + bare[1], pr: n})
		case len(fs) > 0:
			fs[len(fs)-1].body = append(fs[len(fs)-1].body, l.text)
		default:
			before = append(before, l.text)
		}
	}
	entries := []ChangeEntry{}
	lead := jsTrim(strings.Join(before, "\n"))
	if lead != "" && !tagOnly.MatchString(lead) {
		entries = append(entries, ChangeEntry{Title: "Release notes", Body: lead})
	}
	for _, f := range fs {
		if housekeep.MatchString(f.title) {
			continue
		}
		title := f.title
		if title == "" {
			title = "Pull request #" + strconv.Itoa(f.pr)
		}
		pr := f.pr
		entries = append(entries, ChangeEntry{Title: title, PR: &pr, Body: jsTrim(strings.Join(f.body, "\n"))})
	}
	return entries
}

// ParseChangelog is CHANGELOG.md as versions in the file's order.
func ParseChangelog(md string) []ChangeVersion {
	type ver struct {
		version string
		date    *string
		text    []string
	}
	var vs []*ver
	for _, l := range mdLines(md) {
		var m []string
		if !l.code {
			m = versionRe.FindStringSubmatch(l.text)
		}
		if m != nil {
			v := &ver{version: m[1]}
			if d := jsTrim(m[2]); d != "" {
				v.date = &d
			}
			vs = append(vs, v)
		} else if len(vs) > 0 {
			vs[len(vs)-1].text = append(vs[len(vs)-1].text, l.text)
		}
	}
	out := make([]ChangeVersion, 0, len(vs))
	for _, v := range vs {
		out = append(out, ChangeVersion{Version: v.version, Date: v.date, Entries: ParseNotes(strings.Join(v.text, "\n"))})
	}
	return out
}

// changelogCache keeps the bundled change log, parsed once per change of
// the file.
type changelogCache struct {
	mu       sync.Mutex
	file     string
	mtime    time.Time
	size     int64
	versions []ChangeVersion
}

// read is the change log in file: whether there is one, and its versions.
func (c *changelogCache) read(file string) (bool, []ChangeVersion) {
	st, err := os.Stat(file)
	if err != nil || !st.Mode().IsRegular() {
		return false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.file == file && c.mtime.Equal(st.ModTime()) && c.size == st.Size() {
		return true, c.versions
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return false, nil
	}
	c.file, c.mtime, c.size, c.versions = file, st.ModTime(), st.Size(), ParseChangelog(string(b))
	return true, c.versions
}
