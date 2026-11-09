package libraries

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Omission is a library a search or query left out, or searched in part.
type Omission struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Reason  string `json:"reason"`
	Partial bool   `json:"partial"`
}

// Omissions is a list that joins a second reason for the same library.
type Omissions struct {
	mu   sync.Mutex
	List []Omission
}

// Add is addOmission.
func (l *Omissions) Add(o Omission) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.List {
		w := &l.List[i]
		if w.ID != o.ID {
			continue
		}
		if !contains(strings.Split(w.Reason, "; "), o.Reason) {
			w.Reason += "; " + o.Reason
		}
		w.Partial = w.Partial && o.Partial
		return
	}
	l.List = append(l.List, o)
}

// Items is the list so far.
func (l *Omissions) Items() []Omission {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Omission{}, l.List...)
}

// Set replaces the list.
func (l *Omissions) Set(list []Omission) {
	l.mu.Lock()
	l.List = list
	l.mu.Unlock()
}

func contains(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}

func short(m string, n int) string {
	lines := strings.Split(strings.TrimSpace(m), "\n")
	line := ""
	for _, l := range lines {
		if l != "" {
			line = l
		}
	}
	u := utf16Units(line)
	if len(u) > n {
		return utf16String(u[:n-1]) + "…"
	}
	return line
}

var deniedText = regexp.MustCompile(`(?i)Operation not permitted|Permission denied`)

// IsDenied is isDeniedError.
func IsDenied(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, fs.ErrPermission) || deniedText.MatchString(err.Error())
}

// DeniedReason is deniedReason.
func DeniedReason(folder string) string {
	if runtime.GOOS == "darwin" {
		return "macOS has not allowed ACTIVATE Studio to read " + folder + "; allow it in System Settings, Privacy & Security, Files and Folders (or Full Disk Access)"
	}
	return "permission denied reading " + folder
}

var deniedRoots sync.Map // root -> deniedAt

type deniedAt struct {
	denied bool
	at     time.Time
}

// RootDenied is rootDenied: whether a library's folder cannot be listed,
// looked at once a minute.
func RootDenied(root string) bool {
	if v, ok := deniedRoots.Load(root); ok && time.Since(v.(deniedAt).at) < time.Minute {
		return v.(deniedAt).denied
	}
	_, err := os.ReadDir(root)
	d := err != nil && IsDenied(err)
	deniedRoots.Store(root, deniedAt{d, time.Now()})
	return d
}

// DeniedRecord is what a build could not read (denied.json).
type DeniedRecord struct {
	Folders []string `json:"folders"`
	Files   int      `json:"files"`
	At      string   `json:"at"`
}

func (r *Registry) deniedFile(id string) string {
	if id == PrimaryID {
		return filepath.Join(r.o.IndexBase, "denied.json")
	}
	return filepath.Join(r.BuiltLibraryDir(id), "denied.json")
}

// ReadDenied is readDenied.
func (r *Registry) ReadDenied(id string) *DeniedRecord {
	b, err := os.ReadFile(r.deniedFile(id))
	if err != nil {
		return nil
	}
	var d DeniedRecord
	if json.Unmarshal(b, &d) != nil || d.Folders == nil || (len(d.Folders) == 0 && d.Files == 0) {
		return nil
	}
	return &d
}

// PrimaryIndexing and PrimaryBuilt say whether the main folder's index is
// being built and was ever built (indexing.ts).
// TODO(index track): set these from internal/index.
var (
	PrimaryIndexing = func() bool { return false }
	PrimaryBuilt    = func() bool { return false }
)

func expectsWords(l Library) bool { return !l.Primary && l.Depth != "" && l.Depth != "names" }
func building(l Library) bool {
	return l.Build != nil && (l.Build.State == "building" || l.Build.State == "updating")
}

// Gap is libraryGap: what a library's state leaves out, nil for nothing.
func (r *Registry) Gap(l Library, words, meaning bool) *Omission {
	o := func(reason string, partial bool) *Omission {
		return &Omission{ID: l.ID, Label: l.Label, Reason: reason, Partial: partial}
	}
	if root := l.SourceRoot; root != "" && l.Site == "" && RootDenied(root) {
		if l.Caps.Index {
			return o(DeniedReason(root)+"; results are from its last pass", true)
		}
		return o(DeniedReason(root), false)
	}
	if !l.Caps.Index {
		if (l.Build != nil && l.Build.State == "building") || (l.Primary && PrimaryIndexing()) {
			return o("being built; searchable once its first pass is done", false)
		}
		if l.Build != nil && l.Build.State == "failed" {
			msg := "its build failed"
			if l.Build.Error != nil && *l.Build.Error != "" {
				msg += ": " + short(*l.Build.Error, 160)
			}
			return o(msg, false)
		}
		if l.Primary && !PrimaryBuilt() {
			return o("not indexed yet", false)
		}
		return o("index not found", false)
	}
	var reasons []string
	if d := r.ReadDenied(l.ID); d != nil && len(d.Folders) > 0 {
		n := len(d.Folders)
		what := "a folder"
		if n != 1 {
			what = fmt.Sprintf("%d folders", n)
		}
		reasons = append(reasons, what+" could not be read ("+DeniedReason(d.Folders[0])+")")
	}
	if g := FormatGap(l.Format); g != "" {
		reasons = append(reasons, g)
	}
	if words && expectsWords(l) && !l.Caps.FullText && (building(l) || r.Lacks(l, "fullText")) {
		if building(l) {
			reasons = append(reasons, "words not searched yet: its text is being read")
		} else {
			reasons = append(reasons, "words not searched: its index has no text yet")
		}
	}
	if meaning && !l.Primary && l.Meaning != "" && l.Meaning != "off" && !l.Caps.Vectors && (building(l) || r.Lacks(l, "vectors")) {
		switch {
		case !building(l):
			reasons = append(reasons, "not searched by meaning: its index has no vectors yet")
		case l.Caps.FullText:
			reasons = append(reasons, "not searched by meaning yet: meaning is still being computed")
		default:
			reasons = append(reasons, "not searched by meaning yet: its text is being read")
		}
	}
	if len(reasons) == 0 {
		return nil
	}
	return o(strings.Join(reasons, "; "), true)
}

var (
	sshish  = regexp.MustCompile(`(?i)ssh|connect|resolve|timed out|refused|unreachable|host key|broken pipe|reset by peer|No route`)
	missing = regexp.MustCompile(`(?i)ENOENT|no such file|index not built|the index is not there`)
)

// FailureReason is failureReason.
func FailureReason(l Library, err error) string {
	m := short(err.Error(), 160)
	if IsDenied(err) {
		root := l.SourceRoot
		if root == "" {
			root = l.Label
		}
		return DeniedReason(root)
	}
	if l.Site != "" && sshish.MatchString(m) {
		return "not reachable over ssh: " + m
	}
	if missing.MatchString(m) {
		return "index not found"
	}
	if f := FormatFailure(m, l.Format); f != "" {
		return f
	}
	return "search failed: " + m
}

// OmissionsText is omissionsText.
func OmissionsText(list []Omission) string {
	if len(list) == 0 {
		return ""
	}
	parts := make([]string, len(list))
	for i, o := range list {
		parts[i] = o.Label + " (" + o.Reason + ")"
	}
	return "Left out or searched in part: " + strings.Join(parts, "; ") + "."
}

// RecordDenied is recordDenied: what a build or sync of a library could
// not read; nothing denied clears the record.
func (r *Registry) RecordDenied(id string, folders []string, files int) {
	f := r.deniedFile(id)
	if len(folders) == 0 && files == 0 {
		os.Remove(f)
		return
	}
	seen := map[string]bool{}
	uniq := []string{}
	for _, p := range folders {
		if !seen[p] {
			seen[p] = true
			uniq = append(uniq, p)
		}
	}
	if len(uniq) > 200 {
		uniq = uniq[:200]
	}
	os.MkdirAll(filepath.Dir(f), 0o755)
	b, err := marshalJS(DeniedRecord{Folders: uniq, Files: files, At: nowISO()}, "")
	if err == nil {
		os.WriteFile(f, b, 0o644)
	}
}

var (
	diskFullRe   = regexp.MustCompile(`(?i)No space left on device|database or disk is full|ENOSPC|File too large|EFBIG|SIGXFSZ`)
	noWriteRe    = regexp.MustCompile(`(?i)EACCES|EPERM|Permission denied|Operation not permitted|read-only file system|EROFS`)
	writeVerbsRe = regexp.MustCompile(`(?i)mkdir|open|rename|write|\.build|index`)
)

// PlainBuildError is plainBuildError: a build's failure in words a
// person can act on.
func PlainBuildError(message string) string {
	if diskFullRe.MatchString(message) {
		return "The disk the index is written on is full, so the new index was not finished. The index in place is untouched; free some space and rebuild."
	}
	if noWriteRe.MatchString(message) && writeVerbsRe.MatchString(message) {
		return "The index folder cannot be written (" + short(message, 120) + "), so nothing was changed. The index in place is untouched; make the folder writable and rebuild."
	}
	return message
}
