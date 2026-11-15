package transfers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// rclone: remotes the user configured in rclone are locations
// (KindRclone), and rclone is a mover for transfers with an rclone remote
// at one end and this computer or another remote at the other.
//
// Studio uses the rclone installed on this computer and its existing
// configuration as they are. It never writes rclone's configuration: it
// reads the remotes with `rclone listremotes --long` (names and types, no
// secrets) and names them in rclone's own commands. With sign-in on none
// of this is offered, since every viewer would act with the credentials of
// the account the Studio runs as.

// homebrewDirs are where Homebrew puts rclone on macOS, looked in after
// the path set in Settings and before PATH (a desktop launcher can start
// the Studio with a short PATH). A variable for tests.
var homebrewDirs = []string{"/opt/homebrew/bin", "/usr/local/bin"}

// goos is runtime.GOOS, a variable for tests.
var goos = runtime.GOOS

// FindRclone is the rclone program for a setting, "" when there is none:
// the path set in Settings, Transfers when it is one; on macOS Homebrew's
// folders; then PATH. A setting that is a bare name ("rclone", the
// default) is the name looked for.
func FindRclone(getenv func(string) string, setting string) string {
	name := strings.TrimSpace(setting)
	if name == "" {
		name = "rclone"
	}
	if strings.ContainsAny(name, `/\`) {
		if runnable(name) {
			return name
		}
		return ""
	}
	try := func(d string) string {
		for _, n := range exeNames(name) {
			if p := filepath.Join(d, n); runnable(p) {
				return p
			}
		}
		return ""
	}
	if goos == "darwin" {
		for _, d := range homebrewDirs {
			if p := try(d); p != "" {
				return p
			}
		}
	}
	for _, d := range filepath.SplitList(getenv("PATH")) {
		if d == "" {
			continue
		}
		if p := try(d); p != "" {
			return p
		}
	}
	return ""
}

func exeNames(name string) []string {
	if goos == "windows" && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		return []string{name + ".exe", name}
	}
	return []string{name}
}

// RcloneRemote is one remote of rclone's configuration: its name (without
// the colon) and backend type.
type RcloneRemote struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// RcloneDetected is what Settings, Transfers shows of rclone: whether it
// is on, the program found and its version, and the remotes configured.
// Why says plainly what is missing.
type RcloneDetected struct {
	Enabled bool           `json:"enabled"`
	Found   bool           `json:"found"`
	Path    string         `json:"path,omitempty"`
	Version string         `json:"version,omitempty"`
	Remotes []RcloneRemote `json:"remotes"`
	Why     string         `json:"why,omitempty"`
}

// rcloneState keeps what rclone said, so a list of locations does not run
// it every time: the remotes for 30 seconds, a remote's features for ten
// minutes.
type rcloneState struct {
	mu       sync.Mutex
	remotes  map[string]rcloneRemotesEntry
	features map[string]rcloneFeaturesEntry
}

type rcloneRemotesEntry struct {
	at      time.Time
	version string
	remotes []RcloneRemote
	why     string
}

type rcloneFeaturesEntry struct {
	at time.Time
	f  rcloneFeatures
}

const (
	rcloneRemotesTTL  = 30 * time.Second
	rcloneFeaturesTTL = 10 * time.Minute
)

// rcloneFeatures is the part of `rclone backend features` Studio uses.
type rcloneFeatures struct {
	Hashes   []string
	Move     bool // server-side rename of a file
	About    bool // free space
	EmptyDir bool // can hold an empty folder
	SlowHash bool // a hash is computed by reading the file
	known    bool
}

func (f rcloneFeatures) has(hash string) bool {
	for _, h := range f.Hashes {
		if h == hash {
			return true
		}
	}
	return false
}

// rcloneEnv is the environment rclone runs with: this process's, so
// RCLONE_CONFIG and the rest apply as the user set them.
func rcloneEnv() []string { return os.Environ() }

// runRclone runs rclone with args and returns its standard output. An
// error carries rclone's last message and its exit status.
func runRclone(ctx context.Context, bin string, input []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = rcloneEnv()
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			return out, &rcloneError{code: -1, msg: err.Error()}
		}
		return out, &rcloneError{code: code, msg: rcloneMessage(stderr.String())}
	}
	return out, nil
}

// rcloneError is rclone ending with a status other than 0.
type rcloneError struct {
	code int
	msg  string
}

func (e *rcloneError) Error() string {
	if e.msg == "" {
		return "rclone ended with status " + itoa(e.code)
	}
	return e.msg
}

// notFound: rclone's exit statuses 3 (folder not found) and 4 (file not
// found).
func notFound(err error) bool {
	var e *rcloneError
	return errors.As(err, &e) && (e.code == 3 || e.code == 4)
}

var rcloneLogPrefix = regexp.MustCompile(`^\d{4}/\d\d/\d\d \d\d:\d\d:\d\d (?:[A-Z]+ ?: ?)?`)

// rcloneMessage is the last useful line rclone printed on standard error,
// without its time stamp and level, or the message of its last JSON log
// line.
func rcloneMessage(stderr string) string {
	var last string
	sc := bufio.NewScanner(strings.NewReader(stderr))
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "{") {
			var j struct {
				Level, Msg string
			}
			if json.Unmarshal([]byte(l), &j) == nil {
				if j.Level == "error" || j.Level == "critical" || j.Level == "fatal" {
					last = strings.TrimSpace(j.Msg)
				}
				continue
			}
		}
		last = rcloneLogPrefix.ReplaceAllString(l, "")
	}
	return short(last)
}

// listRemotes reads `rclone listremotes --long`: "name: type" a line.
func parseRemotes(out []byte) []RcloneRemote {
	var rs []RcloneRemote
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		i := strings.Index(line, ":")
		if i <= 0 {
			continue
		}
		name := strings.TrimSpace(line[:i])
		typ := strings.TrimSpace(line[i+1:])
		if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, "\x00\r\n") {
			continue
		}
		rs = append(rs, RcloneRemote{Name: name, Type: typ})
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].Name < rs[j].Name })
	return rs
}

var rcloneVersionRe = regexp.MustCompile(`rclone\s+v?(\d+\.\d+(?:\.\d+)?\S*)`)

// parseRcloneVersion is the version in `rclone version`'s first line.
func parseRcloneVersion(out string) string {
	line, _, _ := strings.Cut(out, "\n")
	if m := rcloneVersionRe.FindStringSubmatch(line); m != nil {
		return m[1]
	}
	return ""
}

// rcloneBin is the rclone program under the settings, "" with rclone
// switched off or with sign-in on, and why not.
func (s *Service) rcloneBin(v Viewer, set Settings) (string, string) {
	if v.AuthOn {
		return "", "with sign-in on, rclone is not used: every viewer would act with this server's rclone credentials"
	}
	if !set.Movers.Rclone.Enabled {
		return "", "rclone is switched off in Settings, Transfers"
	}
	bin := FindRclone(s.d.Getenv, set.Movers.Rclone.Path)
	if bin == "" {
		return "", "rclone is not installed on this computer (looked for " + strings.TrimSpace(orDefault(set.Movers.Rclone.Path, "rclone")) + ")"
	}
	return bin, ""
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

// rcloneRemotes is rclone's version and remotes, kept 30 seconds; why
// says what is missing.
func (s *Service) rcloneRemotes(ctx context.Context, bin string) (string, []RcloneRemote, string) {
	key := bin + "|" + s.d.Getenv("RCLONE_CONFIG")
	st := s.rc
	st.mu.Lock()
	e, ok := st.remotes[key]
	st.mu.Unlock()
	if ok && time.Since(e.at) < rcloneRemotesTTL {
		return e.version, e.remotes, e.why
	}
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	e = rcloneRemotesEntry{at: time.Now()}
	if out, err := runRclone(c, bin, nil, "version"); err != nil {
		e.why = "rclone at " + bin + " did not run: " + messageOf(err)
	} else {
		e.version = parseRcloneVersion(string(out))
		out, err := runRclone(c, bin, nil, "listremotes", "--long")
		if err != nil {
			e.why = "rclone could not list its remotes: " + messageOf(err)
		} else if e.remotes = parseRemotes(out); len(e.remotes) == 0 {
			e.why = "rclone has no remotes configured; add them with rclone config"
		}
	}
	if ctx.Err() == nil {
		st.mu.Lock()
		st.remotes[key] = e
		st.mu.Unlock()
	}
	return e.version, e.remotes, e.why
}

// DetectRclone is rclone as Settings, Transfers shows it.
func (s *Service) detectRclone(ctx context.Context, v Viewer, set Settings) *RcloneDetected {
	d := &RcloneDetected{Enabled: set.Movers.Rclone.Enabled && !v.AuthOn, Remotes: []RcloneRemote{}}
	bin, why := s.rcloneBin(v, set)
	if bin == "" {
		d.Why = why
		return d
	}
	d.Path = bin
	ver, rs, why := s.rcloneRemotes(ctx, bin)
	d.Found = ver != "" || len(rs) > 0
	d.Version, d.Why = ver, why
	if rs != nil {
		d.Remotes = rs
	}
	return d
}

// trailingComma is a comma before a closing brace or bracket, which
// `rclone backend features` prints.
var trailingComma = regexp.MustCompile(`,(\s*[}\]])`)

// parseFeatures reads `rclone backend features`.
func parseFeatures(out []byte) (rcloneFeatures, error) {
	var raw struct {
		Hashes   []string
		Features map[string]bool
	}
	if err := json.Unmarshal(trailingComma.ReplaceAll(out, []byte("$1")), &raw); err != nil {
		return rcloneFeatures{}, err
	}
	f := raw.Features
	return rcloneFeatures{Hashes: raw.Hashes, Move: f["Move"], About: f["About"], EmptyDir: f["CanHaveEmptyDirectories"],
		SlowHash: f["SlowHash"], known: true}, nil
}

// rcloneFeaturesOf is a remote's features, kept ten minutes. A remote whose
// features cannot be read is taken as an object store with no hashes: no
// rename, no free space, no empty folders.
func (s *Service) rcloneFeaturesOf(ctx context.Context, bin, remote string) rcloneFeatures {
	key := bin + "|" + s.d.Getenv("RCLONE_CONFIG") + "|" + remote
	st := s.rc
	st.mu.Lock()
	e, ok := st.features[key]
	st.mu.Unlock()
	if ok && time.Since(e.at) < rcloneFeaturesTTL {
		return e.f
	}
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := runRclone(c, bin, nil, "backend", "features", "--", remote+":")
	var f rcloneFeatures
	if err == nil {
		f, err = parseFeatures(out)
	}
	if err != nil {
		return rcloneFeatures{}
	}
	st.mu.Lock()
	st.features[key] = rcloneFeaturesEntry{time.Now(), f}
	st.mu.Unlock()
	return f
}

// rcloneLocs is a location for each configured remote, with sign-in off
// and rclone found. They are not probed: a remote is reached when it is
// browsed, and an error then says why.
func (s *Service) rcloneLocs(ctx context.Context, v Viewer, set Settings) []*loc {
	bin, _ := s.rcloneBin(v, set)
	if bin == "" {
		return nil
	}
	_, rs, _ := s.rcloneRemotes(ctx, bin)
	var out []*loc
	for _, r := range rs {
		f := &RcloneFS{Bin: bin, Remote: r.Name, Type: r.Type, features: func(ctx context.Context) rcloneFeatures {
			return s.rcloneFeaturesOf(ctx, bin, r.Name)
		}}
		detail := "rclone remote"
		if r.Type != "" {
			detail += ", " + r.Type
		}
		out = append(out, &loc{Location: Location{ID: KindRclone + ":" + r.Name, Kind: KindRclone, Label: r.Name, Detail: detail,
			Writable: true, Browsable: true, Available: true}, fs: f})
	}
	return out
}

func asRclone(f FS) (*RcloneFS, bool) {
	r, ok := f.(*RcloneFS)
	return r, ok && r != nil
}

// RcloneOnHost is whether rclone is on a host's PATH and its version, from
// the helper's `command -v rclone`. It is information for a location's
// test; transfers do not run rclone on hosts.
func RcloneOnHost(ctx context.Context, run Runner, site, key string) (string, bool) {
	if sftpOnlySite(ctx, run, site, key) {
		return "", false
	}
	res, _, err := runTool(ctx, run, site, key, 60*time.Second, nil, "rclone", "/")
	if err != nil || res.Code != 0 {
		return "", false
	}
	return parseRcloneVersion(strings.TrimSpace(lastLine(res.Stdout))), true
}

// syncRule is how a sync with an rclone remote at one end tells changed
// files, said in the plan (Compare): size and a hash both ends have where
// there is one, since an object store's listed time is its upload time;
// else size and modification time. It returns the hash, "" for none (and
// then sha256 with checksum, as for every sync).
func syncRule(ctx context.Context, p *planRec) string {
	_, a := asRclone(p.src.fs)
	_, b := asRclone(p.dst.fs)
	kind := ""
	if a || b {
		kind = commonHash(ctx, p.src.fs, p.dst.fs)
	}
	switch {
	case kind != "":
		p.Compare = "size and " + kind + ", a hash both ends have (size and modification time for a file with no " + kind + ")"
	case p.req.Options.checksum():
		p.Compare = "size and sha256"
	case a || b:
		p.Compare = "size and modification time, since the ends share no hash; where an object store lists upload times, files of the same size are sent again"
	default:
		p.Compare = "size and modification time"
	}
	return kind
}

// syncSums is the hashes a sync compares: of the kind both ends share, or
// sha256.
func syncSums(ctx context.Context, p *planRec, kind string, src, dst []string) (map[string]string, map[string]string, error) {
	if kind == "" {
		a, err := p.src.fs.Sha256(ctx, src)
		if err != nil {
			return nil, nil, err
		}
		b, err := p.dst.fs.Sha256(ctx, dst)
		return a, b, err
	}
	a, err := p.src.fs.(hashKinder).hashes(ctx, kind, src)
	if err != nil {
		return nil, nil, err
	}
	b, err := p.dst.fs.(hashKinder).hashes(ctx, kind, dst)
	return a, b, err
}
