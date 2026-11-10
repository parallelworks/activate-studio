// Package libconfig is a library list as a file: an administrator exports
// the libraries of one Studio, and each person imports the file into their
// own Studio, where every entry is added through the same routes and checks
// as the Add a library forms. The file holds where each library is and how
// it is built, and nothing that is one person's or one Studio's: no ids,
// build state, counts, keys, tokens or settings.
//
// This file is the format: the entries, reading a file (Parse), and
// writing one (Export). routes.go serves export and the check before an
// import.
package libconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// FormatName is the file's "format".
const FormatName = "activate-studio-libraries"

// Version is the format version this Studio writes and reads.
const Version = 1

// MaxEntries is the most entries a file may have.
const MaxEntries = 500

// The kinds of entry.
const (
	KindIndex  = "index"  // a GUFI index on this computer
	KindFolder = "folder" // a folder the Studio indexes on this computer
	KindSSH    = "ssh"    // a library on another system over ssh
	KindPw     = "pw"     // a library on an ACTIVATE cluster over pw ssh
)

// The modes of an ssh or pw entry.
const (
	ModeConnect = "connect" // an index that exists there
	ModeBuild   = "build"   // a folder the Studio indexes there
)

// File is a library list as written.
type File struct {
	Format        string  `json:"format"`
	Version       int     `json:"version"`
	ExportedAt    string  `json:"exportedAt"`
	StudioVersion string  `json:"studioVersion,omitempty"`
	Libraries     []Entry `json:"libraries"`
}

// Entry is one library. Which fields apply depends on Kind and Mode:
//
//	index:  indexRoot, sourceRoot, readThrough
//	folder: sourceRoot, indexDir, depth
//	ssh:    destination, port, sshOptions, gufiBin, and for connect
//	        indexRoot, sourceRoot and readThrough, for build sourceRoot,
//	        indexDir, depth
//	pw:     cluster, gufiBin, and the same paths as ssh
type Entry struct {
	Kind        string       `json:"kind"`
	Label       string       `json:"label,omitempty"`
	Mode        string       `json:"mode,omitempty"`
	Destination string       `json:"destination,omitempty"`
	Port        int          `json:"port,omitempty"`
	SSHOptions  []string     `json:"sshOptions,omitempty"`
	Cluster     string       `json:"cluster,omitempty"`
	IndexRoot   string       `json:"indexRoot,omitempty"`
	SourceRoot  string       `json:"sourceRoot,omitempty"`
	IndexDir    string       `json:"indexDir,omitempty"`
	GufiBin     string       `json:"gufiBin,omitempty"`
	Depth       string       `json:"depth,omitempty"`
	ReadThrough *ReadThrough `json:"readThrough,omitempty"`
}

// ReadThrough is another host over ssh that has an index's files.
type ReadThrough struct {
	Destination string   `json:"destination"`
	Port        int      `json:"port,omitempty"`
	SSHOptions  []string `json:"sshOptions,omitempty"`
	Root        string   `json:"root,omitempty"`
}

// ---- reading ----

// Parsed is one entry of a file as read: the entry normalized, or why it
// cannot be imported.
type Parsed struct {
	Entry *Entry
	Error string
}

// Header is what a file says about itself.
type Header struct {
	Version       int
	ExportedAt    string
	StudioVersion string
}

// ErrNotList is a file that is not a library list at all.
var ErrNotList = errors.New("This is not a library list exported from the Studio: it has no \"format\": \"" + FormatName + "\".")

// Parse reads a file. A file that is not a library list is an error; an
// entry that cannot be imported (an unknown kind, a missing or bad field,
// a version newer than this Studio reads) is reported in its Parsed, and
// the others are read. Keys this Studio does not know are ignored.
func Parse(data []byte) (Header, []Parsed, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil || top == nil {
		return Header{}, nil, errors.New("The file is not JSON, or not a JSON object.")
	}
	var format string
	if json.Unmarshal(top["format"], &format) != nil || format != FormatName {
		return Header{}, nil, ErrNotList
	}
	h := Header{}
	var v float64
	if json.Unmarshal(top["version"], &v) != nil || v < 1 || v != float64(int(v)) {
		return Header{}, nil, errors.New("The file has no format version, or one that is not a whole number.")
	}
	h.Version = int(v)
	json.Unmarshal(top["exportedAt"], &h.ExportedAt)
	json.Unmarshal(top["studioVersion"], &h.StudioVersion)
	var raw []json.RawMessage
	if json.Unmarshal(top["libraries"], &raw) != nil {
		return Header{}, nil, errors.New("The file has no \"libraries\" list.")
	}
	if len(raw) > MaxEntries {
		return Header{}, nil, fmt.Errorf("The file has %d libraries; at most %d are read.", len(raw), MaxEntries)
	}
	out := make([]Parsed, len(raw))
	for i, r := range raw {
		var m map[string]any
		if json.Unmarshal(r, &m) != nil || m == nil {
			out[i] = Parsed{Error: "This entry is not a JSON object."}
			continue
		}
		if h.Version > Version {
			// Read what can be read, for the preview's kind and label, but
			// add nothing a newer format may mean otherwise.
			e := &Entry{Kind: strField(m, "kind"), Label: clip(strField(m, "label"))}
			out[i] = Parsed{Entry: e, Error: fmt.Sprintf("The file is format version %d, written by a newer Studio; this one reads version %d. Update the Studio to import it.", h.Version, Version)}
			continue
		}
		e, err := Normalize(m)
		if err != nil {
			out[i] = Parsed{Entry: e, Error: err.Error()}
			continue
		}
		out[i] = Parsed{Entry: e}
	}
	return h, out, nil
}

var (
	controlChars = regexp.MustCompile(`[\x00-\x1f\x7f]`)
	remoteRooted = regexp.MustCompile(`^(?:/|~(?:/|$))`)
	clusterRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,119}$`)
	destPortRe   = regexp.MustCompile(`^(.+):(\d{1,5})$`)
)

func strField(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return strings.TrimSpace(s)
}

func clip(s string) string {
	if utf8.RuneCountInString(s) <= 60 {
		return s
	}
	return string([]rune(s)[:60])
}

// Normalize checks one entry and returns it with only the fields its kind
// takes. On an error the entry returned has what was read of it, for the
// preview.
func Normalize(m map[string]any) (*Entry, error) {
	e := &Entry{Kind: strField(m, "kind"), Label: clip(strField(m, "label"))}
	if controlChars.MatchString(e.Label) {
		return e, errors.New("label: no control characters")
	}
	var err error
	switch e.Kind {
	case KindIndex:
		if e.IndexRoot, err = localPath(m, "indexRoot", "index directory", true); err != nil {
			return e, err
		}
		if e.SourceRoot, err = localPath(m, "sourceRoot", "source directory", false); err != nil {
			return e, err
		}
		e.Label = labelOr(e.Label, e.IndexRoot)
	case KindFolder:
		if e.SourceRoot, err = localPath(m, "sourceRoot", "folder", true); err != nil {
			return e, err
		}
		if e.IndexDir, err = localPath(m, "indexDir", "index folder", false); err != nil {
			return e, err
		}
		if e.Depth, err = depthOf(m); err != nil {
			return e, err
		}
		e.Label = labelOr(e.Label, e.SourceRoot)
	case KindSSH, KindPw:
		if e.Kind == KindSSH {
			if e.Destination, e.Port, e.SSHOptions, err = sshOf(m["destination"], m["port"], m["sshOptions"]); err != nil {
				return e, err
			}
		} else {
			e.Cluster = strField(m, "cluster")
			if !clusterRe.MatchString(e.Cluster) {
				return e, errors.New("cluster: the name of a cluster, in letters, digits, dots, dashes and underscores")
			}
		}
		e.Mode = strField(m, "mode")
		if e.Mode == "" {
			e.Mode = ModeConnect
		}
		if e.GufiBin, err = remotePath(m, "gufiBin", "GUFI bin directory", false); err != nil {
			return e, err
		}
		switch e.Mode {
		case ModeConnect:
			if e.IndexRoot, err = remotePath(m, "indexRoot", "index directory", true); err != nil {
				return e, err
			}
			if e.SourceRoot, err = remotePath(m, "sourceRoot", "source directory", false); err != nil {
				return e, err
			}
			e.Label = labelOr(e.Label, e.IndexRoot)
		case ModeBuild:
			if e.SourceRoot, err = remotePath(m, "sourceRoot", "source folder", true); err != nil {
				return e, err
			}
			if e.IndexDir, err = remotePath(m, "indexDir", "index folder", false); err != nil {
				return e, err
			}
			if e.Depth, err = depthOf(m); err != nil {
				return e, err
			}
			e.Label = labelOr(e.Label, e.SourceRoot)
		default:
			return e, fmt.Errorf("mode: connect or build, not %q", e.Mode)
		}
	case "":
		return e, errors.New("This entry has no kind.")
	default:
		return e, fmt.Errorf("The kind %q is not one this Studio knows (index, folder, ssh or pw). A newer Studio may have written it.", e.Kind)
	}
	// Another host that has the files, for an index the Studio only reads.
	if rt, ok := m["readThrough"].(map[string]any); ok && e.Kind != KindFolder && e.Mode != ModeBuild {
		if e.ReadThrough, err = readThroughOf(rt); err != nil {
			return e, err
		}
	}
	return e, nil
}

func labelOr(label, p string) string {
	if label != "" {
		return label
	}
	b := path.Base(strings.TrimRight(p, "/"))
	if b == "." || b == "/" || b == "" || b == "~" {
		return "library"
	}
	return clip(b)
}

func hasDotDot(p string) bool {
	for _, s := range strings.Split(p, "/") {
		if s == ".." {
			return true
		}
	}
	return false
}

// localPath is a path on the computer that imports the file: absolute, or
// under ~.
func localPath(m map[string]any, key, what string, required bool) (string, error) {
	return checkPath(m, key, what, required, "an absolute path, or one under ~")
}

// remotePath is a path on the remote system: absolute, or under ~, which
// the connection test resolves there.
func remotePath(m map[string]any, key, what string, required bool) (string, error) {
	return checkPath(m, key, what, required, "an absolute path on that system, or one under ~")
}

func checkPath(m map[string]any, key, what string, required bool, want string) (string, error) {
	v, present := m[key]
	if !present || v == nil {
		if required {
			return "", fmt.Errorf("%s: missing (%s)", what, key)
		}
		return "", nil
	}
	s, ok := v.(string)
	s = strings.TrimSpace(s)
	if !ok || (s == "" && required) {
		return "", fmt.Errorf("%s: %s", what, want)
	}
	if s == "" {
		return "", nil
	}
	if len(s) > 1024 || controlChars.MatchString(s) || !remoteRooted.MatchString(s) || hasDotDot(s) {
		return "", fmt.Errorf("%s: %s", what, want)
	}
	if len(s) > 1 {
		s = strings.TrimRight(s, "/")
		if s == "" {
			s = "/"
		}
	}
	return s, nil
}

func depthOf(m map[string]any) (string, error) {
	v, ok := m["depth"]
	if !ok || v == nil || v == "" {
		return "", nil
	}
	s, _ := v.(string)
	for _, d := range libraries.IndexDepths {
		if s == d {
			return s, nil
		}
	}
	return "", errors.New("depth: names, text, ocr, meaning-documents or meaning-full")
}

// sshOf is a host over ssh as an entry gives it: user@host or host (or an
// alias), a port given apart or after a colon, and options as a list or as
// typed.
func sshOf(destination, port, options any) (string, int, []string, error) {
	dest, _ := destination.(string)
	dest = strings.TrimSpace(dest)
	var given any = port
	if mm := destPortRe.FindStringSubmatch(dest); mm != nil && (port == nil || port == "" || port == float64(0)) {
		dest, given = mm[1], mm[2]
	}
	if !remote.SSHDestination.MatchString(dest) {
		return "", 0, nil, errors.New("destination: user@host, host, or a Host alias from ~/.ssh/config")
	}
	p := 0
	switch g := given.(type) {
	case nil:
	case float64:
		p = int(g)
		if float64(p) != g || (p != 0 && (p < 1 || p > 65535)) {
			return "", 0, nil, errors.New("port: a number from 1 to 65535")
		}
	case string:
		if g != "" {
			n, err := strconv.Atoi(g)
			if err != nil || n < 1 || n > 65535 {
				return "", 0, nil, errors.New("port: a number from 1 to 65535")
			}
			p = n
		}
	default:
		return "", 0, nil, errors.New("port: a number from 1 to 65535")
	}
	var opts []string
	var err error
	switch o := options.(type) {
	case nil:
		opts = []string{}
	case string:
		opts, err = remote.ParseSSHOptions(o)
	case []any:
		words := make([]string, 0, len(o))
		for _, w := range o {
			s, ok := w.(string)
			if !ok {
				return "", 0, nil, errors.New("ssh options: a list of words")
			}
			words = append(words, s)
		}
		opts, err = remote.CheckSSHOptions(words)
	default:
		return "", 0, nil, errors.New("ssh options: a list of words")
	}
	if err != nil {
		return "", 0, nil, err
	}
	if _, err := remote.SSHSite(dest, p, opts); err != nil {
		return "", 0, nil, err
	}
	if len(opts) == 0 {
		opts = nil
	}
	return dest, p, opts, nil
}

func readThroughOf(m map[string]any) (*ReadThrough, error) {
	d, p, o, err := sshOf(m["destination"], m["port"], m["sshOptions"])
	if err != nil {
		return nil, fmt.Errorf("readThrough %s", err.Error())
	}
	root, err := remotePath(m, "root", "readThrough root", false)
	if err != nil {
		return nil, err
	}
	return &ReadThrough{Destination: d, Port: p, SSHOptions: o, Root: root}, nil
}

// ---- user names ----

// UserOf splits user@host.
func UserOf(destination string) (user, host string) {
	if i := strings.LastIndex(destination, "@"); i >= 0 {
		return destination[:i], destination[i+1:]
	}
	return "", destination
}

// WithoutUser is a destination with its user name left out.
func WithoutUser(destination string) string {
	_, h := UserOf(destination)
	return h
}

// optionValue reports the value of a flag word: "-iFILE" or "-i", "FILE".
func flagOf(w string) (flag, value string, ok bool) {
	if len(w) >= 2 && w[0] == '-' && w[1] != '-' {
		return w[1:2], w[2:], true
	}
	return "", "", false
}

// identityKeys are the ssh options that name one person's own key files,
// which mean nothing on another computer and are never exported.
var identityKeys = map[string]bool{"identityfile": true, "certificatefile": true, "identityagent": true}

// CleanOptions is ssh options as exported: never the options that name the
// exporter's own key files or ssh config (-i, -F, IdentityFile and the
// like); with stripUser also no login name (-l, User=), and no user name on
// a jump host (-J, ProxyJump=).
func CleanOptions(opts []string, stripUser bool) []string {
	var out []string
	for i := 0; i < len(opts); i++ {
		w := opts[i]
		flag, value, isFlag := flagOf(w)
		takesNext := isFlag && value == "" && flag != "4" && flag != "6" && flag != "C"
		next := ""
		if takesNext && i+1 < len(opts) {
			next = opts[i+1]
		}
		val := value
		if takesNext {
			val = next
		}
		skip := func() {
			if takesNext {
				i++
			}
		}
		switch {
		case isFlag && (flag == "i" || flag == "F"):
			skip()
			continue
		case isFlag && flag == "l" && stripUser:
			skip()
			continue
		case isFlag && flag == "J" && stripUser:
			skip()
			out = append(out, "-J", stripJumpUsers(val))
			continue
		case isFlag && flag == "o":
			k, v, _ := strings.Cut(val, "=")
			lk := strings.ToLower(strings.TrimSpace(k))
			if identityKeys[lk] || (stripUser && lk == "user") {
				skip()
				continue
			}
			if stripUser && lk == "proxyjump" {
				skip()
				out = append(out, "-o", k+"="+stripJumpUsers(v))
				continue
			}
		}
		out = append(out, w)
		if takesNext && i+1 < len(opts) {
			out = append(out, next)
			i++
		}
	}
	return out
}

func stripJumpUsers(list string) string {
	parts := strings.Split(list, ",")
	for i, p := range parts {
		parts[i] = WithoutUser(p)
	}
	return strings.Join(parts, ",")
}

// ---- writing ----

// Options is what an export takes in.
type Options struct {
	// IDs are the libraries to export; none means all.
	IDs []string
	// Primary includes the main folder, as a folder entry.
	Primary bool
	// KeepUser keeps user@host; otherwise only host is written, so each
	// person connects as themselves.
	KeepUser      bool
	StudioVersion string
	Now           time.Time
}

// Source is the libraries an export reads: the deployment's (main folder,
// pinned and added, as the registry lists them) and the viewer's own.
type Source struct {
	Libraries []libraries.Library
	Users     []libraries.UserDef
	// Depth is a library's stored depth, for one the Studio builds.
	Depth func(id string) string
}

// Skipped is a library that is not in the file, and why.
type Skipped struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Why   string `json:"why"`
}

// studioDir is a folder the Studio makes in a home folder for itself (the
// default index folder of a remote build, GUFI it installed there): one
// person's, so left out and made again where the file is imported.
func studioDir(p string) bool { return strings.Contains(p, "/.activate-studio/") }

// Export writes the libraries o selects.
func Export(src Source, o Options) (File, []Skipped) {
	want := map[string]bool{}
	for _, id := range o.IDs {
		want[id] = true
	}
	picked := func(id string) bool { return len(want) == 0 || want[id] }
	depth := func(id string) string {
		if src.Depth == nil {
			return ""
		}
		return src.Depth(id)
	}
	f := File{Format: FormatName, Version: Version, ExportedAt: o.Now.UTC().Format("2006-01-02T15:04:05.000Z"), StudioVersion: o.StudioVersion, Libraries: []Entry{}}
	var skipped []Skipped
	for _, l := range src.Libraries {
		if l.Remote() {
			continue
		}
		if l.Primary {
			if !o.Primary || (len(want) > 0 && !want[l.ID]) {
				continue
			}
			if l.SourceRoot == "" {
				skipped = append(skipped, Skipped{l.ID, l.Label, "The main folder has no folder on this computer."})
				continue
			}
			f.Libraries = append(f.Libraries, Entry{Kind: KindFolder, Label: l.Label, SourceRoot: l.SourceRoot, Depth: l.Depth})
			continue
		}
		if !picked(l.ID) {
			continue
		}
		if l.Build != nil {
			if l.SourceRoot == "" {
				skipped = append(skipped, Skipped{l.ID, l.Label, "Its folder is not recorded."})
				continue
			}
			e := Entry{Kind: KindFolder, Label: l.Label, SourceRoot: l.SourceRoot, Depth: l.Depth}
			if !l.Build.Managed {
				e.IndexDir = l.Build.IndexDir
			}
			f.Libraries = append(f.Libraries, e)
			continue
		}
		e := Entry{Kind: KindIndex, Label: l.Label, IndexRoot: l.IndexRoot, SourceRoot: l.SourceRoot,
			ReadThrough: exportReadThrough(l.ReadThrough, o.KeepUser)}
		f.Libraries = append(f.Libraries, e)
	}
	for _, d := range src.Users {
		if !picked(d.ID) {
			continue
		}
		e, why := userEntry(d, depth(d.ID), o.KeepUser)
		if why != "" {
			skipped = append(skipped, Skipped{d.ID, d.Label, why})
			continue
		}
		f.Libraries = append(f.Libraries, e)
	}
	return f, skipped
}

// exportReadThrough is a read-through host as exported, with the same
// user name and key file rules as a library over ssh.
func exportReadThrough(rt *libraries.ReadThrough, keepUser bool) *ReadThrough {
	if rt == nil || rt.Destination == "" {
		return nil
	}
	x := &ReadThrough{Destination: rt.Destination, SSHOptions: CleanOptions(rt.SSHOptions, !keepUser)}
	if !keepUser {
		x.Destination = WithoutUser(rt.Destination)
	}
	if rt.Port != nil {
		x.Port = *rt.Port
	}
	if rt.Root != nil {
		x.Root = *rt.Root
	}
	if len(x.SSHOptions) == 0 {
		x.SSHOptions = nil
	}
	return x
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func userEntry(d libraries.UserDef, depth string, keepUser bool) (Entry, string) {
	e := Entry{Label: d.Label}
	if d.IsSSH() {
		e.Kind = KindSSH
		e.Destination = deref(d.Destination)
		if e.Destination == "" {
			e.Destination = d.Site
		}
		if !keepUser {
			e.Destination = WithoutUser(e.Destination)
		}
		if d.Port != nil {
			e.Port = *d.Port
		}
		e.SSHOptions = CleanOptions(d.SSHOptions, !keepUser)
		if len(e.SSHOptions) == 0 {
			e.SSHOptions = nil
		}
	} else {
		e.Kind = KindPw
		e.Cluster = d.Site
		if d.Build == nil && (d.Existing == nil || !*d.Existing) {
			return e, "It was built by the platform's index workflow, in your own space on the cluster. Add it again in the other Studio."
		}
	}
	gufiBin := func(b string) string {
		if studioDir(b) {
			return ""
		}
		return b
	}
	if d.Build != nil {
		e.Mode = ModeBuild
		e.SourceRoot = d.Build.SourceRoot
		if !studioDir(d.Build.IndexDir) {
			e.IndexDir = d.Build.IndexDir
		}
		e.GufiBin = gufiBin(d.Build.GufiBin)
		e.Depth = depth
		if e.SourceRoot == "" {
			return e, "Its folder is not recorded."
		}
		return e, ""
	}
	e.Mode = ModeConnect
	e.ReadThrough = exportReadThrough(d.ReadThrough, keepUser)
	e.IndexRoot = deref(d.IndexRoot)
	e.SourceRoot = deref(d.SourceRoot)
	e.GufiBin = gufiBin(deref(d.GufiBin))
	if e.IndexRoot == "" {
		return e, "Its index directory is not recorded."
	}
	return e, ""
}

// FileName is the name an export is saved as: <label>.studio-libraries.json.
func FileName(label string) string {
	s := strings.ToLower(label)
	s = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	if s == "" {
		s = "libraries"
	}
	return s + ".studio-libraries.json"
}
