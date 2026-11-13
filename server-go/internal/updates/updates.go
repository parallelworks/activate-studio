// Package updates is server/src/updates.ts in Go: updates from the
// project's GitHub releases, for the macOS app and the per-user Linux app,
// with the contract docs/UPDATES.md describes (channels, the signed
// manifest and the checks on every download, installing a chosen version,
// Move to Stable, state snapshots and the state format, requiresRelaunch,
// the rollback to the copy kept beside the app) and the background check.
//
// A verified archive is unpacked beside the app, swapped in with the old
// copy kept as .<name>.previous, and the server restarted so the run loop
// (studio-run.sh) starts the new copy: through launchctl kickstart for the
// macOS LaunchAgent, through exit 76 for the Linux payload and a macOS run
// loop started without an agent. updates/pending.json marks the first
// starts of a new version: the run loop puts the previous copy back if the
// new one keeps stopping, and the server clears the mark once it has been up
// for a minute. A clone is told the version and how to update; a hosted
// Studio never checks. The files on disk (state.json, pending.json,
// rolled-back.json, swap.json, reopen, relaunch.json, the snapshots) are
// the Node server's, so Node and Go versions read each other's.
package updates

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/netx"
	"github.com/parallelworks/activate-studio/server-go/internal/settings"
	"github.com/parallelworks/activate-studio/server-go/internal/state"
)

const (
	repo          = "parallelworks/activate-studio"
	appName       = "ACTIVATE Studio.app"
	linuxTop      = "activate-studio"
	installKind   = "install-kind"
	defaultPkg    = "activate-studio"
	bundleID      = "com.parallelworks.activate-studio"
	agentLabel    = "com.parallelworks.activate-studio"
	requestTimout = 15 * time.Second
	lsregister    = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
	cloneCommand  = "git pull\npnpm install\npnpm build"
	pendingWindow = 2 * time.Hour

	// RestartExitCode asks the launcher to start the server again;
	// RelaunchExitCode asks the run loop to run the copy an update put in
	// place (lifecycle.ts).
	RestartExitCode  = 75
	RelaunchExitCode = 76
)

// Timings are measured by the clock between checks; Poll is how often the
// server looks whether one is due (a Mac's timers stop while it sleeps).
type Timings struct {
	FirstCheck, Interval, Soon, Poll, Idle, IdlePoll, Healthy time.Duration
	// Retire is how often the server looks again whether the copy an update
	// replaced can be removed (retire.go); 30 seconds when unset.
	Retire time.Duration
}

// DefaultTimings are updates.ts's.
var DefaultTimings = Timings{FirstCheck: time.Minute, Interval: 15 * time.Minute, Soon: 5 * time.Minute,
	Poll: 2 * time.Minute, Idle: 10 * time.Minute, IdlePoll: 30 * time.Second, Healthy: time.Minute, Retire: 30 * time.Second}

// Options is what the updater needs from the server.
type Options struct {
	ProjectRoot string // PROJECT_ROOT: the payload's app/ in the Linux app
	IndexBase   string
	Getenv      func(string) string
	Settings    *settings.Store
	AuthEnabled bool
	Log         func(string)
	// Restart asks the launcher to start the server again with code, after
	// the current response has gone out; false when no launcher restarts it
	// (lifecycle.ts requestRestart).
	Restart func(code int) bool
	// Busy: a chat is answering or indexing runs; an automatic install
	// waits until it has been false for Timings.Idle.
	Busy func() bool
	// Version is the running build's version and build time (version.ts).
	Version func() (string, *string)
	Client  *http.Client
	Timings Timings
	GOOS    string // runtime.GOOS unless set (tests)
	GOARCH  string
	// SystemVersion, when set, stands for sw_vers or glibc's version (tests).
	SystemVersion func() string
	// ShellExe, when set, stands for the program of the window that started
	// this server, "" with none (tests); else read from /proc on Linux.
	ShellExe func() string
	// InUse, when set, stands for the processes running from a folder
	// (tests); else read from /proc on Linux and lsof on macOS.
	InUse func(dir string) []int
	// LastNodeRelease is the newest release that runs the Node server
	// (versions.go's LastNodeRelease unless set here or, for tests that run
	// the server as a process, in STUDIO_LAST_NODE_RELEASE). Install a
	// version, Roll back and Move to Stable offer only releases after it.
	LastNodeRelease string
}

// Updater is the update state of one server.
type Updater struct {
	o     Options
	state state.Dir
	log   func(string)

	// bg counts the goroutines the updater started, and the programs they
	// wait for (launchctl, lsregister), so a test can wait for them.
	bg sync.WaitGroup

	mu         sync.Mutex
	st         status
	saved      *saved
	prepared   *prepared
	op         chan struct{} // closed when the operation under way ends
	refused    string        // a version that failed verification here; auto install skips it
	rollback   *string       // what the run loop's rollback did, reported once
	wanted     bool          // Update now was asked for during an automatic download
	lastTry    time.Time
	scheduled  bool
	stop       chan struct{}
	idleStop   chan struct{}
	changelogs changelogCache

	sysOnce sync.Once
	sysVer  string
	inferMu sync.Mutex
	infer   map[string]string
}

type status struct {
	State    string
	Error    *string
	Progress *Progress
	Offline  bool
}

// Progress is a download's bytes so far and total.
type Progress struct {
	Bytes int64 `json:"bytes"`
	Total int64 `json:"total"`
}

type asset struct {
	URL  string `json:"url"`
	Size int64  `json:"size"`
	// API is the asset's address on the release API (its "url"), the second
	// way to fetch it when the download address fails (fetchAsset).
	API string `json:"api,omitempty"`
}

type release struct {
	Tag         string           `json:"tag"`
	Prerelease  bool             `json:"prerelease"`
	PublishedAt *string          `json:"publishedAt"`
	Notes       string           `json:"notes"`
	URL         string           `json:"url"`
	Assets      map[string]asset `json:"assets"`
}

type saved struct {
	Etag      *string   `json:"etag"`
	Releases  []release `json:"releases"`
	CheckedAt *string   `json:"checkedAt"`
}

type manifest struct {
	Version          string
	Asset            string
	SHA256           string
	Size             int64
	MinMacos         string
	MinGlibc         string
	BuiltAt          string
	Channel          string
	StateFormat      int
	RequiresRelaunch bool
}

type prepared struct {
	Version      string
	From         string
	ShellChanged bool
	StateFormat  int
	Relaunch     bool
	Older        bool
}

// installOpts: allowOlder for a picked version, restoreState to put back
// the snapshot a version needs, rollback for Roll back (a picked version
// that is reported as a rollback).
type installOpts struct{ allowOlder, restoreState, rollback bool }

// refusedErr is a check the update failed, as opposed to a network or disk failure.
type refusedErr struct{ msg string }

func (e *refusedErr) Error() string { return e.msg }

func refuse(format string, a ...any) error { return &refusedErr{fmt.Sprintf(format, a...)} }

func isRefused(err error) bool {
	var r *refusedErr
	return errors.As(err, &r)
}

// New is the updater; Start begins its background work.
func New(o Options) *Updater {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Log == nil {
		o.Log = func(string) {}
	}
	if o.Restart == nil {
		o.Restart = func(int) bool { return false }
	}
	if o.Busy == nil {
		o.Busy = func() bool { return false }
	}
	if o.Client == nil {
		o.Client = &http.Client{Transport: http.DefaultTransport}
	}
	if o.Timings == (Timings{}) {
		o.Timings = DefaultTimings
	}
	if o.Timings.Retire <= 0 {
		o.Timings.Retire = DefaultTimings.Retire
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.GOARCH == "" {
		o.GOARCH = runtime.GOARCH
	}
	if o.Version == nil {
		o.Version = func() (string, *string) { return "dev", nil }
	}
	if o.LastNodeRelease == "" {
		o.LastNodeRelease = LastNodeRelease
		if v := o.Getenv("STUDIO_LAST_NODE_RELEASE"); v != "" {
			if _, ok := ParseVersion(v); ok {
				o.LastNodeRelease = v
			}
		}
	}
	return &Updater{o: o, state: state.Dir{Base: o.IndexBase, Getenv: o.Getenv}, log: o.Log, st: status{State: "idle"},
		stop: make(chan struct{}), infer: map[string]string{}}
}

// StateStore is the state format and snapshots of this Studio.
func (u *Updater) StateStore() state.Dir { return u.state }

func (u *Updater) env(k string) string { return u.o.Getenv(k) }

func (u *Updater) policy() settings.UpdatePolicy {
	if u.o.Settings == nil {
		return settings.UpdatePolicy{Check: true, Install: "manual", Channel: "stable"}
	}
	return u.o.Settings.Effective().Updates
}

// ---- the app and its files ----

type current struct {
	Version string  `json:"version"`
	BuiltAt *string `json:"builtAt"`
}

func (u *Updater) running() current {
	v, b := u.o.Version()
	return current{Version: v, BuiltAt: b}
}

func (u *Updater) hosted() bool { return u.o.AuthEnabled || u.env("PW_PLATFORM_HOST") != "" }

func (u *Updater) appBundle() string {
	b := u.env("STUDIO_APP_BUNDLE")
	if b == "" {
		return ""
	}
	abs, err := filepath.Abs(b)
	if err != nil {
		return b
	}
	return abs
}

func (u *Updater) bundleWritable() bool {
	b := u.appBundle()
	if b == "" {
		return false
	}
	if !writableDir(filepath.Dir(b)) {
		return false
	}
	st, err := os.Stat(b)
	return err == nil && st.IsDir()
}

type kit struct {
	flavor    string // macos or linux
	asset     string
	manifest  string
	signature string
	top       string
	label     string
}

func (k kit) resources(root string) string {
	if k.flavor == "macos" {
		return filepath.Join(root, "Contents", "Resources")
	}
	return root
}

func newKit(flavor, asset, top, label string) kit {
	m := strings.TrimSuffix(asset, ".tar.gz") + ".manifest.json"
	return kit{flavor: flavor, asset: asset, manifest: m, signature: m + ".sig", top: top, label: label}
}

// MacAsset is the macOS app archive's name for this computer.
func (u *Updater) MacAsset() string {
	arch := u.o.GOARCH
	if arch == "amd64" {
		arch = "x86_64"
	}
	return "activate-studio-macos-" + arch + ".tar.gz"
}

// LinuxAsset is the Linux archive's name for this computer.
func (u *Updater) LinuxAsset() string {
	arch := u.o.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	}
	return "activate-studio-linux-" + arch + ".tar.gz"
}

func (u *Updater) linuxKit() kit {
	return newKit("linux", u.LinuxAsset(), linuxTop, "Linux app")
}

type payloadInfo struct {
	root        string
	marker      string // user, deb, rpm or ""
	packageName string
	kind        string // user, deb, rpm, other
	selfUpdates bool
}

// payloadRoot is the top directory of the Linux payload this server runs
// from: STUDIO_PAYLOAD_ROOT, else the folder above PROJECT_ROOT when that
// is the payload's app/ (Linux only).
func (u *Updater) payloadRoot() string {
	root := u.env("STUDIO_PAYLOAD_ROOT")
	if root == "" {
		if u.o.GOOS != "linux" || filepath.Base(u.o.ProjectRoot) != "app" {
			return ""
		}
		root = filepath.Dir(u.o.ProjectRoot)
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return ""
	}
	root = real
	// A payload that replaced this one with no run loop (requiresRelaunch)
	// starts through bin/activate-studio; until the app reopens, this server
	// runs from the copy moved aside and still counts as the app.
	started := isFile(filepath.Join(root, "studio-run.sh")) || isFile(filepath.Join(root, "bin", "activate-studio"))
	if started && isFile(filepath.Join(root, "app", "build-info.json")) {
		return root
	}
	return ""
}

var pkgNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]{0,63}$`)

func readMarker(root string) (kind, name string) {
	b, err := os.ReadFile(filepath.Join(root, installKind))
	if err != nil {
		return "", ""
	}
	f := strings.Fields(string(b))
	if len(f) == 0 || (f[0] != "user" && f[0] != "deb" && f[0] != "rpm") {
		return "", ""
	}
	name = defaultPkg
	if len(f) > 1 && pkgNameRe.MatchString(f[1]) {
		name = f[1]
	}
	return f[0], name
}

func writableDir(dir string) bool {
	// access(2) with W_OK|X_OK, as fs.accessSync does.
	return accessOK(dir, 0x2|0x1)
}

func replaceable(root string) bool {
	if !writableDir(filepath.Dir(root)) {
		return false
	}
	st, err := os.Stat(root)
	return err == nil && st.IsDir()
}

func (u *Updater) packageOwner(root string) string {
	u.inferMu.Lock()
	defer u.inferMu.Unlock()
	if k, ok := u.infer[root]; ok {
		return k
	}
	kind := ""
	if b, err := os.ReadFile("/var/lib/dpkg/info/" + defaultPkg + ".list"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if l == filepath.Join(root, "studio-run.sh") {
				kind = "deb"
			}
		}
	}
	if kind == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if exec.CommandContext(ctx, "rpm", "-qf", filepath.Join(root, "studio-run.sh")).Run() == nil {
			kind = "rpm"
		}
		cancel()
	}
	u.infer[root] = kind
	return kind
}

func (u *Updater) payload() *payloadInfo {
	root := u.payloadRoot()
	if root == "" {
		return nil
	}
	mk, name := readMarker(root)
	if mk != "" && mk != "user" {
		return &payloadInfo{root: root, marker: mk, packageName: name, kind: mk}
	}
	if name == "" {
		name = defaultPkg
	}
	if replaceable(root) {
		return &payloadInfo{root: root, marker: mk, packageName: name, kind: "user", selfUpdates: true}
	}
	kind := u.packageOwner(root)
	if kind == "" {
		kind = "other"
	}
	return &payloadInfo{root: root, marker: mk, packageName: name, kind: kind}
}

// Mode is app, clone, hosted or package. A hosted deployment that updates
// itself (deployment.go) is app.
func (u *Updater) Mode() string {
	if u.hosted() {
		if u.deployment() {
			if p := u.payload(); p != nil && p.selfUpdates {
				return "app"
			}
		}
		return "hosted"
	}
	if u.appBundle() != "" {
		if u.bundleWritable() {
			return "app"
		}
		return "clone"
	}
	p := u.payload()
	if p == nil {
		return "clone"
	}
	if p.selfUpdates {
		return "app"
	}
	return "package"
}

func (u *Updater) kit() *kit {
	if u.appBundle() != "" {
		k := newKit("macos", u.MacAsset(), appName, "macOS app")
		return &k
	}
	if u.payload() != nil {
		k := u.linuxKit()
		return &k
	}
	return nil
}

func (u *Updater) installRoot() string {
	if b := u.appBundle(); b != "" {
		return b
	}
	if p := u.payload(); p != nil {
		return p.root
	}
	return ""
}

func (u *Updater) beside(suffix string) string {
	b := u.installRoot()
	return filepath.Join(filepath.Dir(b), "."+filepath.Base(b)+"."+suffix)
}

// UpdatesDir is <data>/updates, or INDEX_BASE/updates.
func (u *Updater) UpdatesDir() string {
	data := u.env("STUDIO_DATA_DIR")
	if data == "" && u.env("STUDIO_CONFIG_FILE") != "" {
		data = filepath.Dir(u.env("STUDIO_CONFIG_FILE"))
	}
	if data != "" {
		return filepath.Join(data, "updates")
	}
	return filepath.Join(u.o.IndexBase, "updates")
}

func (u *Updater) inUpdates(name string) string { return filepath.Join(u.UpdatesDir(), name) }

func (u *Updater) publicKey() ed25519.PublicKey {
	b, k := u.installRoot(), u.kit()
	if b == "" || k == nil {
		return nil
	}
	return readPublicKey(filepath.Join(k.resources(b), "update-signing.pub"))
}

func readPublicKey(file string) ed25519.PublicKey {
	pemBytes, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	blk, _ := pem.Decode(pemBytes)
	if blk == nil {
		return nil
	}
	key, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return nil
	}
	pk, _ := key.(ed25519.PublicKey)
	return pk
}

func (u *Updater) versionOf(bundle string) string {
	k := u.kit()
	if k == nil {
		return ""
	}
	var info struct {
		Version any `json:"version"`
	}
	b, err := os.ReadFile(filepath.Join(k.resources(bundle), "app", "build-info.json"))
	if err != nil || json.Unmarshal(b, &info) != nil || info.Version == nil {
		return ""
	}
	v := jsString(info.Version)
	if v == "" || v == "false" || v == "0" {
		return ""
	}
	return v
}

// jsString is String(v) for the JSON values a field may hold.
func jsString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func readJSON(file string, v any) bool {
	b, err := os.ReadFile(file)
	return err == nil && json.Unmarshal(b, v) == nil
}

// writeJSON writes JSON.stringify(v, null, 1) and a newline through a
// temporary file renamed over the file.
func writeJSON(file string, v any) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	return writeRename(file, buf.Bytes())
}

// systemVersion is the macOS release (sw_vers) or glibc's version, or "".
func (u *Updater) systemVersion() string {
	if u.o.SystemVersion != nil {
		return u.o.SystemVersion()
	}
	u.sysOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		switch u.o.GOOS {
		case "darwin":
			if out, err := exec.CommandContext(ctx, "sw_vers", "-productVersion").Output(); err == nil {
				u.sysVer = strings.TrimSpace(string(out))
			}
		case "linux":
			// "glibc 2.35"; nothing on musl.
			if out, err := exec.CommandContext(ctx, "getconf", "GNU_LIBC_VERSION").Output(); err == nil {
				if f := strings.Fields(string(out)); len(f) == 2 && f[0] == "glibc" {
					u.sysVer = f[1]
				}
			}
		}
	})
	return u.sysVer
}

// ---- GitHub ----

func (u *Updater) userAgent() string { return "activate-studio/" + u.running().Version }

func (u *Updater) releaseBase() string {
	b := u.env("STUDIO_UPDATE_URL")
	if b == "" {
		b = "https://api.github.com"
	}
	return strings.TrimRight(b, "/")
}

func (u *Updater) releaseHost() string {
	p, err := url.Parse(u.releaseBase())
	if err != nil || p.Host == "" {
		return "api.github.com"
	}
	return p.Host
}

func plain(err error) string {
	if err == nil {
		return ""
	}
	if netx.Code(err) == "timeout" {
		return "no answer within 15 seconds"
	}
	s := err.Error()
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		s = ue.Err.Error()
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func (u *Updater) loadSaved() *saved {
	if u.saved == nil {
		var s saved
		if !readJSON(u.inUpdates("state.json"), &s) {
			s = saved{}
		}
		u.saved = &s
	}
	if u.saved.Releases == nil {
		u.saved.Releases = []release{}
	}
	return u.saved
}

func toRelease(r map[string]any) release {
	rel := release{Assets: map[string]asset{}}
	if list, ok := r["assets"].([]any); ok {
		for _, a := range list {
			m, _ := a.(map[string]any)
			name, ok1 := m["name"].(string)
			dl, ok2 := m["browser_download_url"].(string)
			if ok1 && ok2 {
				size, _ := m["size"].(float64)
				if math.IsNaN(size) {
					size = 0
				}
				api, _ := m["url"].(string)
				rel.Assets[name] = asset{URL: dl, Size: int64(size), API: api}
			}
		}
	}
	if t, ok := r["tag_name"]; ok && t != nil {
		rel.Tag = jsString(t)
	}
	rel.Prerelease = truthy(r["prerelease"])
	if p := r["published_at"]; truthy(p) {
		s := jsString(p)
		rel.PublishedAt = &s
	}
	if b, ok := r["body"]; ok && b != nil {
		rel.Notes = jsString(b)
		if r := []rune(rel.Notes); len(r) > 20000 {
			rel.Notes = string(r[:20000])
		}
	}
	if h, ok := r["html_url"]; ok && h != nil {
		rel.URL = jsString(h)
	}
	return rel
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0 && !math.IsNaN(x)
	}
	return true
}

func (u *Updater) fetchReleases() error {
	s := u.loadSaved()
	ctx, cancel := context.WithTimeout(context.Background(), requestTimout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.releaseBase()+"/repos/"+repo+"/releases?per_page=30", nil)
	req.Header.Set("User-Agent", u.userAgent())
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if s.Etag != nil && *s.Etag != "" {
		req.Header.Set("If-None-Match", *s.Etag)
	}
	u.mu.Unlock()
	res, err := u.o.Client.Do(req)
	var body []byte
	if err == nil {
		body, err = io.ReadAll(io.LimitReader(res.Body, 32<<20))
		res.Body.Close()
	}
	u.mu.Lock()
	if err != nil {
		return err
	}
	now := state.ISO(time.Now())
	switch {
	case res.StatusCode == http.StatusNotModified:
		cp := *s
		cp.CheckedAt = &now
		u.saved = &cp
	case res.StatusCode >= 200 && res.StatusCode < 300:
		var list []any
		if json.Unmarshal(body, &list) != nil || list == nil {
			return errors.New("GitHub did not answer with a list of releases")
		}
		rels := []release{}
		for _, r := range list {
			m, ok := r.(map[string]any)
			if !ok || truthy(m["draft"]) {
				continue
			}
			rel := toRelease(m)
			if _, ok := ParseVersion(rel.Tag); ok {
				rels = append(rels, rel)
			}
		}
		var etag *string
		if e := res.Header.Get("Etag"); e != "" {
			etag = &e
		}
		u.saved = &saved{Etag: etag, Releases: rels, CheckedAt: &now}
	case res.StatusCode == 403 || res.StatusCode == 429:
		return fmt.Errorf("GitHub refused the request (HTTP %d, usually its hourly limit); it is tried again later", res.StatusCode)
	default:
		return fmt.Errorf("GitHub answered HTTP %d", res.StatusCode)
	}
	return writeJSON(u.inUpdates("state.json"), u.saved)
}

func releaseChannel(r release) string {
	if r.Prerelease {
		return "preview"
	}
	return "stable"
}

func onChannel(r release, channel string) bool {
	return channel == "preview" || releaseChannel(r) == "stable"
}

func (u *Updater) hasApp(r release) bool {
	k := u.kit()
	if k == nil {
		return false
	}
	_, a := r.Assets[k.asset]
	_, m := r.Assets[k.manifest]
	_, s := r.Assets[k.signature]
	return a && m && s
}

func (u *Updater) needsBuild() bool { m := u.Mode(); return m == "app" || m == "package" }

func (u *Updater) latestRelease(channel string, needsApp bool) *release {
	var best *release
	for i := range u.loadSaved().Releases {
		r := u.saved.Releases[i]
		if !onChannel(r, channel) || (needsApp && !u.hasApp(r)) {
			continue
		}
		if best == nil || CompareVersions(r.Tag, best.Tag) > 0 {
			rr := r
			best = &rr
		}
	}
	return best
}

func (u *Updater) appPending(channel string, now time.Time) bool {
	any := u.latestRelease(channel, false)
	usable := u.latestRelease(channel, true)
	if any == nil || u.hasApp(*any) || (usable != nil && CompareVersions(any.Tag, usable.Tag) <= 0) {
		return false
	}
	if any.PublishedAt == nil {
		return false
	}
	at, err := time.Parse(time.RFC3339, *any.PublishedAt)
	return err == nil && now.Sub(at) < pendingWindow
}

// ---- status ----

func (u *Updater) inProgress() bool {
	switch u.st.State {
	case "checking", "downloading", "verifying", "installing", "restarting", "relaunch":
		return true
	}
	return false
}

// PackageInfo is how a Linux payload installed from a package is updated.
type PackageInfo struct {
	Kind    string  `json:"kind"`
	Manager *string `json:"manager"`
	Name    string  `json:"name"`
}

func packageCommand(p PackageInfo) string {
	switch p.Kind {
	case "deb":
		return "sudo apt update\nsudo apt install --only-upgrade " + p.Name
	case "rpm":
		return "sudo dnf upgrade --refresh " + p.Name
	}
	return ""
}

func (u *Updater) packageInfo() *PackageInfo {
	p := u.payload()
	if p == nil || p.selfUpdates {
		return nil
	}
	info := PackageInfo{Kind: "other", Name: p.packageName}
	switch p.kind {
	case "deb":
		info.Kind, info.Manager = "deb", strp("apt")
	case "rpm":
		info.Kind, info.Manager = "rpm", strp("dnf")
	}
	return &info
}

func (u *Updater) packageMessage(info PackageInfo, available bool) string {
	if info.Kind == "other" {
		where := "its folder"
		if p := u.payload(); p != nil {
			where = p.root
		}
		return "This copy is installed in " + where + ", which you cannot write to, so it cannot update itself. Update it the way it was installed, or ask whoever manages this computer; or install ACTIVATE Studio in your home folder from the releases page, which then updates itself."
	}
	what, how := "an .rpm package", "dnf"
	if info.Kind == "deb" {
		what, how = "a .deb package", "apt"
	}
	if available {
		return "This copy was installed from " + what + ", so it does not update itself. Update it with " + how + " in a terminal, then quit and reopen ACTIVATE Studio."
	}
	return "This copy was installed from " + what + ", so it does not update itself; updates come through " + how + "."
}

func (u *Updater) refusal(mode string, rel *release, available bool) *string {
	switch mode {
	case "hosted":
		return strp("This Studio is updated by its deployment.")
	case "package":
		if info := u.packageInfo(); info != nil {
			return strp(u.packageMessage(*info, available))
		}
		return strp("This copy is managed by a package manager.")
	case "clone":
		if b := u.appBundle(); b != "" {
			return strp("The folder holding the app (" + filepath.Dir(b) + ") is not writable, so the app cannot replace itself. Move the app to ~/Applications, or install the new version from the releases page.")
		}
		return strp("This Studio runs from a clone of the repository and does not update itself. To update it: " + strings.ReplaceAll(cloneCommand, "\n", " && ") + ", then start it again.")
	}
	k := u.kit()
	if u.publicKey() == nil {
		return strp("This copy of the app carries no update signing key, so it cannot verify a download. Install the new version from the releases page.")
	}
	if rel == nil || !available {
		return nil
	}
	if !u.hasApp(*rel) {
		return strp("The release " + rel.Tag + " has no signed " + k.label + ".")
	}
	if u.inProgress() {
		return strp("An update is already " + u.st.State + ".")
	}
	return nil
}

// UpdateRelease is one release an update brings, with its notes as entries.
type UpdateRelease struct {
	Version     string        `json:"version"`
	PublishedAt *string       `json:"publishedAt"`
	URL         string        `json:"url"`
	Entries     []ChangeEntry `json:"entries"`
}

func (u *Updater) releasesUpTo(rel release, channel, from string) []UpdateRelease {
	var rs []release
	for _, r := range u.loadSaved().Releases {
		if onChannel(r, channel) && IsNewer(r.Tag, from) && CompareVersions(r.Tag, rel.Tag) <= 0 {
			rs = append(rs, r)
		}
	}
	sort.SliceStable(rs, func(i, j int) bool { return CompareVersions(rs[i].Tag, rs[j].Tag) > 0 })
	out := []UpdateRelease{}
	for _, r := range rs {
		out = append(out, UpdateRelease{Version: r.Tag, PublishedAt: r.PublishedAt, URL: r.URL, Entries: ParseNotes(r.Notes)})
	}
	return out
}

func (u *Updater) installable(channel string) []release {
	if u.Mode() != "app" {
		return nil
	}
	var rs []release
	for _, r := range u.loadSaved().Releases {
		if onChannel(r, channel) && u.hasApp(r) && u.goRelease(r.Tag) {
			rs = append(rs, r)
		}
	}
	sort.SliceStable(rs, func(i, j int) bool { return CompareVersions(rs[i].Tag, rs[j].Tag) > 0 })
	return rs
}

func (u *Updater) pickable(version, channel string) *release {
	for _, r := range u.installable(channel) {
		if sameVersion(r.Tag, version) {
			rr := r
			return &rr
		}
	}
	return nil
}

func (u *Updater) stableBehind(p settings.UpdatePolicy, cur string) *release {
	if _, ok := ParseVersion(cur); p.Channel != "stable" || !ok {
		return nil
	}
	r := u.latestRelease("stable", u.needsBuild())
	if r != nil && u.goRelease(r.Tag) && CompareVersions(r.Tag, cur) < 0 {
		return r
	}
	return nil
}

// UpdateVersion is one release Install a version lists.
type UpdateVersion struct {
	Version     string  `json:"version"`
	PublishedAt *string `json:"publishedAt"`
	URL         string  `json:"url"`
	Channel     string  `json:"channel"`
	Current     bool    `json:"current"`
	Newer       bool    `json:"newer"`
}

type latest struct {
	Version     string  `json:"version"`
	PublishedAt *string `json:"publishedAt"`
	Notes       string  `json:"notes"`
	URL         string  `json:"url"`
}

// Status is GET /api/updates, its keys in updates.ts's order.
type Status struct {
	Mode        string                `json:"mode"`
	Current     current               `json:"current"`
	Policy      settings.UpdatePolicy `json:"policy"`
	Latest      *latest               `json:"latest"`
	Releases    []UpdateRelease       `json:"releases"`
	Available   bool                  `json:"available"`
	CanInstall  bool                  `json:"canInstall"`
	Reason      *string               `json:"reason"`
	Command     *string               `json:"command"`
	CommandNote *string               `json:"commandNote"`
	Package     *PackageInfo          `json:"package"`
	State       string                `json:"state"`
	Progress    *Progress             `json:"progress"`
	Error       *string               `json:"error"`
	CheckedAt   *string               `json:"checkedAt"`
	Offline     bool                  `json:"offline"`
	Previous    any                   `json:"previous"`
	Reopen      bool                  `json:"reopen"`
	// Deployment: a hosted deployment that updates itself (deployment.go).
	Deployment   bool            `json:"deployment,omitempty"`
	Versions     []UpdateVersion `json:"versions"`
	MoveToStable *struct {
		Version string `json:"version"`
	} `json:"moveToStable"`
	StateFormat int     `json:"stateFormat"`
	Snapshots   []Brief `json:"snapshots"`
}

// Status is the update status (the caller holds no lock).
func (u *Updater) Status() Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.status()
}

func (u *Updater) status() Status {
	mode := u.Mode()
	pol := u.policy()
	cur := u.running()
	var rel *release
	if mode != "hosted" {
		rel = u.latestRelease(pol.Channel, u.needsBuild())
	}
	available := rel != nil && IsNewer(rel.Tag, cur.Version)
	reason := u.refusal(mode, rel, available)
	var pkg *PackageInfo
	if mode == "package" {
		pkg = u.packageInfo()
	}
	pkgCommand := ""
	if pkg != nil && available {
		pkgCommand = packageCommand(*pkg)
	}
	s := Status{Mode: mode, Current: cur, Policy: pol, Releases: []UpdateRelease{}, Available: available,
		CanInstall: mode == "app" && available && reason == nil, Reason: reason, Package: pkg,
		State: u.st.State, Progress: u.st.Progress, Error: u.st.Error, CheckedAt: u.loadSaved().CheckedAt,
		Offline: u.st.Offline, Versions: []UpdateVersion{}, StateFormat: u.state.StateFormat(), Snapshots: []Brief{}}
	if rel != nil {
		s.Latest = &latest{Version: rel.Tag, PublishedAt: rel.PublishedAt, Notes: rel.Notes, URL: rel.URL}
		if available {
			s.Releases = u.releasesUpTo(*rel, pol.Channel, cur.Version)
		}
	}
	if mode == "clone" && u.appBundle() == "" {
		s.Command = strp(cloneCommand)
	} else if pkgCommand != "" {
		s.Command = strp(pkgCommand)
	}
	if pkgCommand != "" && pkg != nil {
		what, these := "an .rpm package", "this"
		if pkg.Kind == "deb" {
			what, these = "a .deb package", "these"
		}
		s.CommandNote = strp("This copy was installed from " + what + ", so it does not update itself. To update, run " + these + " in a terminal, then quit and reopen ACTIVATE Studio:")
	}
	if s.Error == nil {
		s.Error = u.rollback
	}
	if p := u.previousVersion(); p != nil {
		s.Previous = p
	}
	if mode == "app" {
		s.Deployment = u.deployment()
		s.Reopen = !s.Deployment && u.reopenWanted(cur.Version)
		if r := u.stableBehind(pol, cur.Version); r != nil {
			s.MoveToStable = &struct {
				Version string `json:"version"`
			}{r.Tag}
		}
		for _, sn := range u.state.ListSnapshots() {
			s.Snapshots = append(s.Snapshots, brief(sn))
		}
	}
	for _, r := range u.installable(pol.Channel) {
		s.Versions = append(s.Versions, UpdateVersion{Version: r.Tag, PublishedAt: r.PublishedAt, URL: r.URL, Channel: releaseChannel(r),
			Current: sameVersion(r.Tag, cur.Version), Newer: IsNewer(r.Tag, cur.Version)})
	}
	return s
}

// ---- the change log ----

// Changelog is GET /api/changelog.
type Changelog struct {
	Included bool            `json:"included"`
	Current  string          `json:"current"`
	Versions []ChangeVersion `json:"versions"`
}

// ChangelogFile is CHANGELOG.md at the top of the app, or STUDIO_CHANGELOG_FILE.
func (u *Updater) ChangelogFile() string {
	if f := u.env("STUDIO_CHANGELOG_FILE"); f != "" {
		return f
	}
	return filepath.Join(u.o.ProjectRoot, "CHANGELOG.md")
}

// Changelog is the change log for this version: the one the build
// carries, and, in the app or a clone, the notes of releases up to the
// running version that it lacks; after keeps only versions newer than it.
func (u *Updater) Changelog(after string) Changelog {
	u.mu.Lock()
	defer u.mu.Unlock()
	cur := u.running().Version
	included, versions := u.changelogs.read(u.ChangelogFile())
	all := append([]ChangeVersion{}, versions...)
	if _, ok := ParseVersion(cur); u.Mode() != "hosted" && ok {
		for _, r := range u.loadSaved().Releases {
			if IsNewer(r.Tag, cur) {
				continue
			}
			have := false
			for _, v := range all {
				if sameVersion(v.Version, r.Tag) {
					have = true
					break
				}
			}
			if have {
				continue
			}
			var date *string
			if r.PublishedAt != nil {
				d := *r.PublishedAt
				if len(d) > 10 {
					d = d[:10]
				}
				date = &d
			}
			all = append(all, ChangeVersion{Version: r.Tag, Date: date, Entries: ParseNotes(r.Notes)})
		}
	}
	since := ""
	if _, ok := ParseVersion(after); after != "" && ok {
		since = after
	}
	out := []ChangeVersion{}
	for _, v := range all {
		if len(v.Entries) > 0 && (since == "" || IsNewer(v.Version, since)) {
			out = append(out, v)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return CompareVersions(out[i].Version, out[j].Version) > 0 })
	return Changelog{Included: included, Current: cur, Versions: out}
}

// ---- download ----

// statusErr is a download that answered with an HTTP status outside 2xx.
type statusErr struct {
	msg  string
	code int
}

func (e *statusErr) Error() string { return e.msg }

// worthAnotherWay: a failure the release API's address may not share, a
// network error or a 5xx; never a refusal (size, hash, signature) or a 4xx.
func worthAnotherWay(err error) bool {
	if err == nil || isRefused(err) {
		return false
	}
	var se *statusErr
	if errors.As(err, &se) {
		return se.code >= 500
	}
	return true
}

// apiURL is a's address on the release API when it is one of this feed's
// (the host and repository the release list came from), else "".
func (u *Updater) apiURL(a asset) string {
	prefix := u.releaseBase() + "/repos/" + repo + "/releases/assets/"
	rest, ok := strings.CutPrefix(a.API, prefix)
	if !ok || rest == "" || strings.ContainsAny(rest, "/?#") {
		return ""
	}
	return a.API
}

// fetchAsset runs get on a's download address and, when that fails with a
// network error or a 5xx, once more on its release API address, which
// GitHub serves apart from the download host. What is fetched is checked
// the same either way (size, SHA-256, signature); the first failure is the
// one reported when both fail.
func (u *Updater) fetchAsset(a asset, get func(rawURL, accept string) error) error {
	err := get(a.URL, "")
	if !worthAnotherWay(err) {
		return err
	}
	api := u.apiURL(a)
	if api == "" {
		return err
	}
	u.log("update: " + plain(err) + "; trying the release API")
	if err2 := get(api, "application/octet-stream"); err2 != nil {
		return err
	}
	return nil
}

func (u *Updater) fetchSmallAsset(a asset, limit int) ([]byte, error) {
	var out []byte
	err := u.fetchAsset(a, func(rawURL, accept string) error {
		b, err := u.fetchSmall(rawURL, accept, limit)
		out = b
		return err
	})
	return out, err
}

func (u *Updater) fetchSmall(rawURL, accept string, limit int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", u.userAgent())
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	res, err := u.o.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	name := path.Base(req.URL.Path)
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, &statusErr{fmt.Sprintf("%s: HTTP %d", name, res.StatusCode), res.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > limit {
		return nil, refuse("%s is larger than a manifest can be.", name)
	}
	return b, nil
}

// stallReader cancels the request when no data has come for a while.
type stallReader struct {
	r      io.Reader
	timer  *time.Timer
	period time.Duration
}

func (s *stallReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if n > 0 {
		s.timer.Reset(s.period)
	}
	return n, err
}

var errStalled = errors.New("no data for 15 seconds")

func (u *Updater) fetchArchive(a asset, file string, man manifest) error {
	return u.fetchAsset(a, func(rawURL, accept string) error { return u.fetchArchiveFrom(rawURL, accept, file, man) })
}

func (u *Updater) fetchArchiveFrom(rawURL, accept, file string, man manifest) error {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	timer := time.AfterFunc(requestTimout, func() { cancel(errStalled) })
	defer timer.Stop()
	u.mu.Lock()
	u.st.Progress = &Progress{Bytes: 0, Total: man.Size}
	u.mu.Unlock()
	part := file + ".part"
	fh, err := os.Create(part)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			fh.Close()
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", u.userAgent())
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	res, err := u.o.Client.Do(req)
	if err != nil {
		if c := context.Cause(ctx); errors.Is(c, errStalled) {
			return c
		}
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return &statusErr{fmt.Sprintf("the download answered HTTP %d", res.StatusCode), res.StatusCode}
	}
	h := sha256.New()
	var bytesIn int64
	body := &stallReader{r: res.Body, timer: timer, period: requestTimout}
	buf := make([]byte, 256<<10)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			bytesIn += int64(n)
			if bytesIn > man.Size {
				return refuse("The download is larger than the %d bytes the manifest gives.", man.Size)
			}
			h.Write(buf[:n])
			if _, err := fh.Write(buf[:n]); err != nil {
				return err
			}
			u.mu.Lock()
			u.st.Progress = &Progress{Bytes: bytesIn, Total: man.Size}
			u.mu.Unlock()
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if c := context.Cause(ctx); errors.Is(c, errStalled) {
				return c
			}
			return rerr
		}
	}
	closed = true
	if err := fh.Close(); err != nil {
		return err
	}
	if bytesIn != man.Size {
		return refuse("The download is %d bytes; the manifest says %d.", bytesIn, man.Size)
	}
	if hex.EncodeToString(h.Sum(nil)) != man.SHA256 {
		return refuse("The download's SHA-256 does not match the manifest.")
	}
	return os.Rename(part, file)
}

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (u *Updater) parseManifest(b []byte, rel release, from, channel string, allowOlder bool) (manifest, error) {
	var raw any
	if json.Unmarshal(b, &raw) != nil {
		return manifest{}, refuse("The manifest is not valid JSON.")
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return manifest{}, refuse("The manifest is not a JSON object.")
	}
	k := u.kit()
	if a, _ := m["asset"].(string); a != k.asset {
		return manifest{}, refuse("The manifest names the asset %s; this app installs %s.", jsUndefString(m["asset"], m), k.asset)
	}
	version := ""
	if v, ok := m["version"]; ok && v != nil {
		version = jsString(v)
	}
	if _, ok := ParseVersion(version); !ok {
		shown := version
		if shown == "" {
			shown = "(none)"
		}
		return manifest{}, refuse("The manifest's version %s is not a version.", shown)
	}
	if allowOlder {
		if sameVersion(version, from) {
			return manifest{}, refuse("%s is the version running.", version)
		}
	} else if !IsNewer(version, from) {
		return manifest{}, refuse("The manifest's version %s is not newer than the running %s.", version, from)
	}
	if !sameVersion(version, rel.Tag) {
		return manifest{}, refuse("The manifest is for %s, but the release is %s.", version, rel.Tag)
	}
	// The channel is signed: a release marked Preview that was published as
	// a stable release is refused, since older Studios go by the flag alone.
	ch, hasCh := m["channel"]
	if hasCh && ch != "stable" && ch != "preview" {
		return manifest{}, refuse("The manifest's channel %s is not stable or preview.", jsString(ch))
	}
	if ch == "preview" && !rel.Prerelease {
		return manifest{}, refuse("%s is a Preview release, but it was published as a stable release.", version)
	}
	relChannel := "stable"
	if ch == "preview" || rel.Prerelease {
		relChannel = "preview"
	}
	if relChannel == "preview" && channel != "preview" {
		return manifest{}, refuse("%s is a Preview release; this Studio is on Stable.", version)
	}
	stateFormat := 1
	if sf, ok := m["stateFormat"]; ok {
		f, isNum := sf.(float64)
		if !isNum || f != math.Trunc(f) || f < 1 || f > 9007199254740991 {
			return manifest{}, refuse("The manifest's stateFormat is not a whole number of 1 or more.")
		}
		stateFormat = int(f)
	}
	relaunch := false
	if rr, ok := m["requiresRelaunch"]; ok {
		bv, isBool := rr.(bool)
		if !isBool {
			return manifest{}, refuse("The manifest's requiresRelaunch is not true or false.")
		}
		relaunch = bv
	}
	sum, _ := m["sha256"].(string)
	if !sha256Re.MatchString(sum) {
		return manifest{}, refuse("The manifest has no valid SHA-256.")
	}
	size, isNum := m["size"].(float64)
	if !isNum || size != math.Trunc(size) || size <= 0 || size > 9007199254740991 {
		return manifest{}, refuse("The manifest has no valid size.")
	}
	man := manifest{Version: version, Asset: k.asset, SHA256: sum, Size: int64(size), Channel: relChannel,
		StateFormat: stateFormat, RequiresRelaunch: relaunch}
	if v, ok := m["minMacos"]; ok && truthy(v) {
		man.MinMacos = jsString(v)
	}
	if v, ok := m["minGlibc"]; ok && truthy(v) {
		man.MinGlibc = jsString(v)
	}
	if v, ok := m["builtAt"].(string); ok {
		man.BuiltAt = v
	}
	have := u.systemVersion()
	if k.flavor == "macos" {
		if man.MinMacos != "" && have != "" && !dottedAtMost(man.MinMacos, have) {
			return manifest{}, refuse("%s needs macOS %s; this Mac has %s.", version, man.MinMacos, have)
		}
	} else if man.MinGlibc != "" && have != "" && !dottedAtMost(man.MinGlibc, have) {
		return manifest{}, refuse("%s needs glibc %s; this computer has %s.", version, man.MinGlibc, have)
	}
	return man, nil
}

// jsUndefString is String(m.asset), "undefined" when the key is missing.
func jsUndefString(v any, m map[string]any) string {
	if _, ok := m["asset"]; !ok {
		return "undefined"
	}
	if v == nil {
		return "null"
	}
	return jsString(v)
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func executable(p string) bool { return isFile(p) && accessOK(p, 0x1) }

func plistString(plist, key string) string {
	m := regexp.MustCompile(`<key>` + regexp.QuoteMeta(key) + `</key>\s*<string>([^<]*)</string>`).FindStringSubmatch(plist)
	if m == nil {
		return ""
	}
	return m[1]
}

// hasServer: the copy holds a server to run, Node's or Go's. A Go copy
// carries Go's (app/bin/studio-server); the Node files a Node updater looks
// for may be placeholders there (docs/UPDATES.md, "The Go version").
func hasServer(res string, nodeExec bool) bool {
	if executable(filepath.Join(res, "app", "bin", "studio-server")) {
		return true
	}
	node := filepath.Join(res, "node", "bin", "node")
	ok := isFile(node) && isFile(filepath.Join(res, "app", "server", "dist", "main.js"))
	if nodeExec {
		ok = ok && executable(node)
	}
	return ok
}

// checkPayload: the unpacked Linux payload is this app, complete, and the
// version the manifest says. One that replaces the whole app
// (requiresRelaunch) need not hold a run loop: its own launcher starts it.
func (u *Updater) checkPayload(dir, version string, relaunch bool) error {
	if relaunch {
		if !executable(filepath.Join(dir, "bin", "activate-studio")) {
			return refuse("The new app has no bin/activate-studio.")
		}
		for _, f := range []string{"update-signing.pub", "manifest.json"} {
			if !isFile(filepath.Join(dir, f)) {
				return refuse("The new app has no %s.", f)
			}
		}
	} else {
		if !executable(filepath.Join(dir, "studio-run.sh")) {
			return refuse("The new app has no studio-run.sh.")
		}
		for _, f := range []string{"update-signing.pub", "manifest.json"} {
			if !isFile(filepath.Join(dir, f)) {
				return refuse("The new app has no %s.", f)
			}
		}
		if !hasServer(dir, true) {
			if !isFile(filepath.Join(dir, "node", "bin", "node")) {
				return refuse("The new app has no node/bin/node.")
			}
			if !isFile(filepath.Join(dir, "app", "server", "dist", "main.js")) {
				return refuse("The new app has no app/server/dist/main.js.")
			}
			return refuse("The new app has no executable node.")
		}
	}
	if v := u.versionOf(dir); v == "" || !sameVersion(v, version) {
		return refuse("The new app is %s, not %s.", orText(v, "of no known version"), version)
	}
	return nil
}

func orText(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func (u *Updater) checkBundle(dir, version string, relaunch bool) error {
	if u.kit().flavor == "linux" {
		return u.checkPayload(dir, version, relaunch)
	}
	b, err := os.ReadFile(filepath.Join(dir, "Contents", "Info.plist"))
	if err != nil {
		return refuse("The new app has no Info.plist.")
	}
	plist := string(b)
	if id := plistString(plist, "CFBundleIdentifier"); id != bundleID {
		return refuse("The new app's bundle identifier is %s, not %s.", orText(id, "missing"), bundleID)
	}
	exe := plistString(plist, "CFBundleExecutable")
	if exe == "" || strings.Contains(exe, "/") || !executable(filepath.Join(dir, "Contents", "MacOS", exe)) {
		return refuse("The new app has no executable.")
	}
	res := filepath.Join(dir, "Contents", "Resources")
	if relaunch {
		if !isFile(filepath.Join(res, "update-signing.pub")) {
			return refuse("The new app has no Resources/update-signing.pub.")
		}
	} else {
		if !isFile(filepath.Join(res, "studio-run.sh")) {
			return refuse("The new app has no Resources/studio-run.sh.")
		}
		if !hasServer(res, false) {
			for _, f := range []string{"node/bin/node", "app/server/dist/main.js"} {
				if !isFile(filepath.Join(res, f)) {
					return refuse("The new app has no Resources/%s.", f)
				}
			}
		}
	}
	if v := u.versionOf(dir); v == "" || !sameVersion(v, version) {
		return refuse("The new app is %s, not %s.", orText(v, "of no known version"), version)
	}
	return nil
}

func fileSHA(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// shellHash is the window's program: for the Linux payload the file
// manifest.json's shell names, for the macOS app its executable.
func (u *Updater) shellHash(bundle string) string {
	if u.kit().flavor == "linux" {
		var m map[string]any
		if !readJSON(filepath.Join(bundle, "manifest.json"), &m) {
			return ""
		}
		rel := ""
		if s, ok := m["shell"]; ok && s != nil {
			rel = jsString(s)
		}
		if rel == "" || filepath.IsAbs(rel) || containsDotDot(rel) {
			return ""
		}
		return fileSHA(filepath.Join(bundle, rel))
	}
	b, err := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
	if err != nil {
		return ""
	}
	return fileSHA(filepath.Join(bundle, "Contents", "MacOS", plistString(string(b), "CFBundleExecutable")))
}

func containsDotDot(rel string) bool {
	for _, p := range strings.Split(rel, "/") {
		if p == ".." {
			return true
		}
	}
	return false
}

// unpack checks every entry is under the one app folder and unpacks it
// beside the app as .<name>.new.
func (u *Updater) unpack(archive string) (string, error) {
	top := u.kit().top
	out, err := exec.Command("tar", "-tzf", archive).Output()
	if err != nil {
		return "", fmt.Errorf("tar could not list the archive: %w", err)
	}
	for _, raw := range strings.Split(string(out), "\n") {
		if raw == "" {
			continue
		}
		e := strings.TrimPrefix(raw, "./")
		if e == top || e == top+"/" {
			continue
		}
		if !strings.HasPrefix(e, top+"/") || containsDotDot(e) {
			return "", refuse("The archive holds %s, outside %s.", raw, top)
		}
	}
	work, next := u.beside("unpack"), u.beside("new")
	if err := rmTree(work); err != nil {
		return "", err
	}
	if err := rmTree(next); err != nil {
		return "", err
	}
	if err := os.Mkdir(work, 0o755); err != nil {
		return "", err
	}
	args := []string{"-xzf", archive}
	if u.kit().flavor == "linux" {
		// Files belong to whoever unpacks them, whatever the archive records.
		args = append(args, "--no-same-owner")
	}
	args = append(args, "-C", work)
	if msg, err := exec.Command("tar", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("tar could not unpack the archive: %v %s", err, strings.TrimSpace(string(msg)))
	}
	if err := os.Rename(filepath.Join(work, top), next); err != nil {
		return "", err
	}
	_ = rmTree(work)
	return next, nil
}

// rmTree deletes a tree, read-only directories in it included.
func rmTree(p string) error {
	err := os.RemoveAll(p)
	if err == nil || !(errors.Is(err, os.ErrPermission)) {
		return err
	}
	makeWritable(p)
	return os.RemoveAll(p)
}

func makeWritable(dir string) {
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() {
		return
	}
	_ = os.Chmod(dir, st.Mode().Perm()|0o700)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			makeWritable(filepath.Join(dir, e.Name()))
		}
	}
}

func (u *Updater) cleanUp() {
	if u.installRoot() != "" {
		for _, s := range []string{"new", "unpack", "failed", "retired"} {
			_ = rmTree(u.beside(s))
		}
	}
	if k := u.kit(); k != nil {
		os.Remove(u.inUpdates(k.asset))
		os.Remove(u.inUpdates(k.asset + ".part"))
	}
}

// verifiedManifest is the release's manifest, its signature verified with
// this app's key and its fields checked against the release, the channel
// and the running version.
func (u *Updater) verifiedManifest(rel release, allowOlder bool) (manifest, error) {
	u.mu.Lock()
	from := u.running().Version
	key := u.publicKey()
	k := u.kit()
	channel := u.policy().Channel
	u.mu.Unlock()
	if key == nil {
		return manifest{}, refuse("This copy of the app carries no update signing key.")
	}
	a, m, s := rel.Assets[k.asset], rel.Assets[k.manifest], rel.Assets[k.signature]
	if a.URL == "" || m.URL == "" || s.URL == "" {
		return manifest{}, refuse("The release %s has no signed %s.", rel.Tag, k.label)
	}
	body, err := u.fetchSmallAsset(m, 64*1024)
	if err != nil {
		return manifest{}, err
	}
	sigText, err := u.fetchSmallAsset(s, 4096)
	if err != nil {
		return manifest{}, err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigText)))
	if err != nil {
		sig = nil
	}
	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(key, body, sig) {
		return manifest{}, refuse("The manifest's signature does not verify with this app's key.")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.parseManifest(body, rel, from, channel, allowOlder)
}

// stateNeeds: what the state needs for a version reading formats up to
// format: nothing, a snapshot to put back, or a refusal when none is kept.
func (u *Updater) stateNeeds(format int) (readable bool, snap *state.Snapshot) {
	if u.state.StateFormat() <= format {
		return true, nil
	}
	return false, u.state.CompatibleSnapshot(format)
}

func (u *Updater) prepare(rel release, o installOpts) (*prepared, error) {
	u.mu.Lock()
	from := u.running().Version
	k := u.kit()
	u.st.State = "downloading"
	u.st.Progress = nil
	u.mu.Unlock()
	if err := os.MkdirAll(u.UpdatesDir(), 0o755); err != nil {
		return nil, err
	}
	man, err := u.verifiedManifest(rel, o.allowOlder)
	if err != nil {
		return nil, err
	}
	readable, snap := u.stateNeeds(man.StateFormat)
	if !readable && snap == nil {
		return nil, refuse("%s reads state up to format %d; the state here is in format %d, and no snapshot in a format it reads is kept.", man.Version, man.StateFormat, u.state.StateFormat())
	}
	if !readable && !o.restoreState {
		return nil, refuse("%s cannot read the state as it is now; installing it puts back the state saved %s, which has to be asked for.", man.Version, snap.At)
	}
	archive := u.inUpdates(k.asset)
	if err := u.fetchArchive(rel.Assets[k.asset], archive, man); err != nil {
		return nil, err
	}
	u.mu.Lock()
	u.st.State = "verifying"
	u.st.Progress = nil
	u.mu.Unlock()
	next, err := u.unpack(archive)
	if err != nil {
		return nil, err
	}
	os.Remove(archive)
	// A deployment has no window: its run loop starts whatever payload is
	// in place, so every release restarts the server, and the window's
	// files are left out as the deployment leaves them out.
	relaunch := man.RequiresRelaunch && !u.deployment()
	if err := u.checkBundle(next, man.Version, relaunch); err != nil {
		return nil, err
	}
	if u.deployment() {
		if err := withoutWindow(next); err != nil {
			return nil, err
		}
	}
	if k.flavor == "linux" {
		if err := u.markUser(next); err != nil {
			return nil, err
		}
	}
	return &prepared{Version: man.Version, From: from, ShellChanged: !u.deployment() && u.shellHash(next) != u.shellHash(u.installRoot()),
		StateFormat: man.StateFormat, Relaunch: relaunch, Older: CompareVersions(man.Version, from) < 0}, nil
}

// markUser: the new payload says it was installed per user, as the one it
// replaces does.
func (u *Updater) markUser(dir string) error {
	_, name := readMarker(u.installRoot())
	line := "user"
	if name != "" && name != defaultPkg {
		line += " " + name
	}
	return os.WriteFile(filepath.Join(dir, installKind), []byte(line+"\n"), 0o644)
}

// restart: the LaunchAgent restarted, so its run loop starts from the app
// now in place; where there is no agent (the Linux payload, or a macOS run
// loop the app started itself) the server exits with RelaunchExitCode and
// the run loop hands over to the new copy's own.
func (u *Updater) restart() {
	u.st.State = "restarting"
	failed := func() {
		u.st.State = "error"
		u.st.Error = strp("The new version is in place, but the Studio could not restart itself. Quit and reopen the app.")
		if u.deployment() {
			u.st.Error = strp("The new version is in place, but the Studio could not restart itself. Launch its workflow again to start it.")
		}
	}
	if k := u.kit(); (k != nil && k.flavor == "linux") || u.env("STUDIO_SUPERVISOR") == "detached" {
		if !u.o.Restart(RelaunchExitCode) {
			failed()
		}
		return
	}
	cmd := exec.Command("launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/%s", os.Getuid(), agentLabel))
	detach(cmd)
	if err := cmd.Start(); err != nil {
		if !u.o.Restart(RestartExitCode) {
			failed()
		}
		return
	}
	u.spawn(func() {
		if err := cmd.Wait(); err != nil {
			u.mu.Lock()
			defer u.mu.Unlock()
			if !u.o.Restart(RestartExitCode) {
				failed()
			}
		}
	})
}

// refreshIcon has LaunchServices look at the new copy (macOS only;
// STUDIO_LSREGISTER names another tool, for tests).
func (u *Updater) refreshIcon(bundle string) {
	if k := u.kit(); k == nil || k.flavor != "macos" {
		return
	}
	tool := u.env("STUDIO_LSREGISTER")
	if tool == "" && u.o.GOOS == "darwin" {
		tool = lsregister
	}
	if tool == "" {
		return
	}
	u.spawn(func() {
		now := time.Now()
		_ = os.Chtimes(bundle, now, now)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := exec.CommandContext(ctx, tool, "-f", bundle).Run(); err != nil {
			u.log("update: LaunchServices did not take the new icon: " + err.Error())
		}
	})
}

func (u *Updater) swapIn(p *prepared, restoreState bool) error {
	app, next, prev := u.installRoot(), u.beside("new"), u.beside("previous")
	u.mu.Lock()
	u.st.State = "installing"
	u.mu.Unlock()
	readable, snap := u.stateNeeds(p.StateFormat)
	if !readable && (!restoreState || snap == nil) {
		return refuse("%s cannot read the state here, and no snapshot it reads was chosen.", p.Version)
	}
	reason := "before an update"
	if p.Older {
		reason = "before installing an older version"
	}
	if _, err := u.state.TakeSnapshot(state.SnapshotOpts{From: p.From, To: strp(p.Version), Reason: reason}); err != nil {
		return refuse("The state could not be saved before the change, so nothing was changed: %s.", err.Error())
	}
	if err := rmTree(prev); err != nil {
		return err
	}
	linux := u.kit().flavor == "linux"
	journal := u.inUpdates("swap.json")
	if linux {
		reopen := "no"
		if p.ShellChanged {
			reopen = "yes"
		}
		if err := writeJSON(journal, map[string]string{"from": p.From, "to": p.Version, "at": state.ISO(time.Now()), "reopen": reopen}); err != nil {
			return err
		}
	}
	if err := os.Rename(app, prev); err != nil {
		os.Remove(journal)
		return err
	}
	if err := os.Rename(next, app); err != nil {
		_ = os.Rename(prev, app)
		os.Remove(journal)
		return err
	}
	u.refreshIcon(app)
	if err := writeJSON(u.inUpdates("pending.json"), map[string]string{"from": p.From, "to": p.Version, "at": state.ISO(time.Now())}); err != nil {
		return err
	}
	os.Remove(u.inUpdates("rolled-back.json"))
	if p.ShellChanged || p.Relaunch {
		_ = os.WriteFile(u.inUpdates("reopen"), []byte(p.Version+"\n"), 0o644)
	}
	if p.Relaunch {
		_ = writeJSON(u.inUpdates("relaunch.json"), map[string]string{"from": p.From, "to": p.Version, "at": state.ISO(time.Now())})
	}
	os.Remove(journal)
	if !readable && snap != nil {
		s, err := u.state.RestoreSnapshot(snap.ID, p.From)
		if err != nil {
			return err
		}
		u.log(fmt.Sprintf("update: the state saved %s (format %d) was put back for %s", s.At, s.Format, p.Version))
	}
	return nil
}

func (u *Updater) fail(err error) {
	u.st.State = "error"
	u.st.Progress = nil
	u.wanted = false
	var msg string
	if isRefused(err) {
		msg = err.Error()
	} else if conn := netx.ConnectionProblem(err, "github.com", u.env); conn != "" {
		msg = "The update failed. " + conn
	} else {
		msg = "The update failed: " + plain(err) + "."
		if netx.Code(err) != "" {
			msg += " The connection to the release server was lost; try again once this computer is online."
		}
	}
	u.st.Error = &msg
}

// beginOp marks an operation under way; endOp ends it.
func (u *Updater) beginOp() chan struct{} {
	c := make(chan struct{})
	u.op = c
	return c
}

func (u *Updater) endOp(c chan struct{}) {
	if u.op == c {
		u.op = nil
	}
	close(c)
}

func (u *Updater) install(rel release, o installOpts) {
	u.mu.Lock()
	var p *prepared
	if !o.allowOlder && u.prepared != nil && u.prepared.Version == rel.Tag && exists(u.beside("new")) {
		p = u.prepared
	}
	u.mu.Unlock()
	err := func() error {
		if p == nil {
			var err error
			if p, err = u.prepare(rel, o); err != nil {
				return err
			}
		}
		u.mu.Lock()
		u.prepared = nil
		u.mu.Unlock()
		return u.swapIn(p, o.restoreState)
	}()
	u.mu.Lock()
	defer u.mu.Unlock()
	if err != nil {
		u.prepared = nil
		if isRefused(err) {
			u.refused = rel.Tag
		}
		u.cleanUp()
		u.fail(err)
		if o.rollback && !isRefused(err) {
			size := ""
			if a, ok := rel.Assets[u.kit().asset]; ok && a.Size > 0 {
				size = fmt.Sprintf(" (%.0f MB)", float64(a.Size)/1e6)
			}
			msg := fmt.Sprintf("Rolling back downloads %s%s from the release server again, and the download failed: %s. This version is still installed; try again once this computer is online.", rel.Tag, size, plain(err))
			if conn := netx.ConnectionProblem(err, "github.com", u.env); conn != "" {
				msg = fmt.Sprintf("Rolling back downloads %s%s from the release server again, and it could not be reached. %s This version is still installed.", rel.Tag, size, conn)
			}
			u.st.Error = &msg
		}
		u.log("update to " + rel.Tag + " not installed: " + *u.st.Error)
		return
	}
	if o.rollback {
		to := p.Version
		_ = writeJSON(u.inUpdates("rolled-back.json"), struct {
			From   string  `json:"from"`
			To     *string `json:"to"`
			At     string  `json:"at"`
			Reason string  `json:"reason"`
			Noted  bool    `json:"noted"`
		}{p.From, &to, state.ISO(time.Now()), "rolled back from Settings", true})
	}
	if p.Relaunch {
		// The new copy replaces the whole app: this server keeps running
		// from the old copy until the app is reopened, and never restarts
		// into it.
		u.st.State = "relaunch"
		u.st.Progress = nil
		u.log("update: " + p.From + " replaced by " + p.Version + ", which runs once the app is reopened")
		return
	}
	u.log("update: " + p.From + " replaced by " + p.Version + "; restarting")
	u.restart()
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// startInstall begins an install (the caller holds the lock).
func (u *Updater) startInstall(rel release, o installOpts) {
	u.stopIdleWait()
	u.wanted = false
	u.st.State = "downloading"
	u.st.Error = nil
	u.rollback = nil
	c := u.beginOp()
	u.spawn(func() {
		u.install(rel, o)
		u.mu.Lock()
		u.endOp(c)
		u.mu.Unlock()
	})
}

// Check asks the release server now and returns once it answered (the
// caller holds no lock).
func (u *Updater) Check() {
	u.mu.Lock()
	c := u.startCheck()
	u.mu.Unlock()
	<-c
}

// startCheck begins a check, or returns the operation already under way
// (the caller holds the lock); the status says "checking" at once.
func (u *Updater) startCheck() chan struct{} {
	if u.op != nil {
		return u.op
	}
	u.lastTry = time.Now()
	u.st.State = "checking"
	c := u.beginOp()
	u.spawn(func() {
		u.mu.Lock()
		err := u.fetchReleases()
		if err == nil {
			u.st.State, u.st.Error, u.st.Offline = "idle", nil, false
		} else if conn := netx.ConnectionProblem(err, u.releaseHost(), u.env); conn != "" {
			u.st.State, u.st.Error, u.st.Offline = "error", strp("The check for updates failed. "+conn), false
			u.log("update check: " + conn)
		} else if code := netx.Code(err); code != "" {
			// Offline is a state of the computer, not a fault of the check.
			u.st.State, u.st.Error, u.st.Offline = "idle", nil, true
			u.log("update check: release server not reachable (" + code + "); checking again later")
		} else {
			u.st.State, u.st.Error, u.st.Offline = "error", strp("The check for updates failed: "+plain(err)+"."), false
		}
		u.endOp(c)
		u.mu.Unlock()
		u.autoInstall()
	})
	return c
}

// autoInstall: with install set to auto, a verified update waits until the
// Studio has been idle (no chat answering, no indexing) for Timings.Idle.
func (u *Updater) autoInstall() {
	u.mu.Lock()
	if u.op != nil || u.policy().Install != "auto" {
		u.mu.Unlock()
		return
	}
	s := u.status()
	relp := u.latestRelease(s.Policy.Channel, u.needsBuild())
	if relp == nil || !s.CanInstall || relp.Tag == u.refused {
		u.mu.Unlock()
		return
	}
	rel := *relp
	var back struct {
		From string `json:"from"`
	}
	if readJSON(u.inUpdates("rolled-back.json"), &back) && back.From == rel.Tag {
		u.mu.Unlock()
		return
	}
	if !(u.prepared != nil && u.prepared.Version == rel.Tag && exists(u.beside("new"))) {
		c := u.beginOp()
		u.mu.Unlock()
		p, err := u.prepare(rel, installOpts{})
		u.mu.Lock()
		if err != nil {
			u.prepared = nil
			u.wanted = false
			if isRefused(err) {
				u.refused = rel.Tag
			}
			u.cleanUp()
			u.fail(err)
		} else {
			u.prepared = p
			u.st.State = "idle"
			u.st.Progress = nil
			u.log("update: " + rel.Tag + " verified; it installs once the Studio is idle")
		}
		u.endOp(c)
		if u.prepared == nil {
			u.mu.Unlock()
			return
		}
		if u.wanted {
			u.startInstall(rel, installOpts{})
			u.mu.Unlock()
			return
		}
	}
	// A release that replaces the whole app waits for Update now: the app
	// has to be reopened, which only a person in front of it can do.
	if u.prepared.Relaunch || u.idleStop != nil {
		u.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	u.idleStop = stop
	t := u.o.Timings
	u.mu.Unlock()
	u.spawn(func() {
		quietSince := time.Now()
		tick := time.NewTicker(t.IdlePoll)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-u.stop:
				return
			case <-tick.C:
			}
			u.mu.Lock()
			if u.idleStop != stop {
				u.mu.Unlock()
				return
			}
			if u.policy().Install != "auto" || u.prepared == nil || u.op != nil {
				u.stopIdleWait()
				u.mu.Unlock()
				return
			}
			u.mu.Unlock()
			if u.o.Busy() {
				quietSince = time.Now()
				continue
			}
			if time.Since(quietSince) >= t.Idle {
				u.mu.Lock()
				if u.idleStop == stop {
					u.stopIdleWait()
					u.startInstall(rel, installOpts{})
				}
				u.mu.Unlock()
				return
			}
		}
	})
}

func (u *Updater) stopIdleWait() {
	if u.idleStop != nil {
		close(u.idleStop)
		u.idleStop = nil
	}
}

// ---- schedule ----

func (u *Updater) waitAfterCheck() time.Duration {
	if (u.needsBuild() && u.appPending(u.policy().Channel, time.Now())) || u.st.Offline {
		return u.o.Timings.Soon
	}
	return u.o.Timings.Interval
}

// checkDue: that long since the last check started or succeeded, whichever
// is later, time asleep included (the caller holds the lock).
func (u *Updater) checkDue(now time.Time) bool {
	if !u.policy().Check || u.Mode() == "hosted" || u.op != nil || u.inProgress() {
		return false
	}
	last := u.lastTry
	if s := u.loadSaved().CheckedAt; s != nil {
		if t, err := time.Parse(time.RFC3339, *s); err == nil && t.After(last) {
			last = t
		}
	}
	return now.Sub(last) >= u.waitAfterCheck()
}

// Start does the bookkeeping after an update and schedules the checks: a
// minute after start, then whenever Interval has passed by the clock,
// looked at every Poll and when the window asks for the status.
func (u *Updater) Start() {
	u.mu.Lock()
	mode := u.Mode()
	if mode == "hosted" {
		u.mu.Unlock()
		return
	}
	if mode == "app" {
		u.cleanUp()
		// This copy runs: a relaunch an update asked for has happened.
		os.Remove(u.inUpdates("relaunch.json"))
		var back map[string]any
		if readJSON(u.inUpdates("rolled-back.json"), &back) && back != nil && !truthy(back["noted"]) {
			from, to, why := "The update", "the previous version", "it kept stopping"
			if v, ok := back["from"].(string); ok {
				from = v
			}
			if v, ok := back["to"].(string); ok {
				to = v
			}
			if v, ok := back["reason"].(string); ok {
				why = v
			}
			note := fmt.Sprintf("%s did not start properly (%s), so %s was put back.", from, why, to)
			u.rollback = &note
			u.log("update: " + note)
			back["noted"] = true
			_ = setKey(u.inUpdates("rolled-back.json"), "noted", true)
		}
		// Up for a minute after an update: the run loop no longer rolls back.
		if exists(u.inUpdates("pending.json")) {
			healthy := u.o.Timings.Healthy
			u.spawn(func() {
				select {
				case <-u.stop:
					return
				case <-time.After(healthy):
				}
				var p struct{ From, To string }
				readJSON(u.inUpdates("pending.json"), &p)
				os.Remove(u.inUpdates("pending.json"))
				// Roll back offers no Node release, so the line names one
				// only when the version before was a Go release.
				if _, ok := ParseVersion(p.From); ok && CompareVersions(p.From, u.o.LastNodeRelease) <= 0 {
					u.log(fmt.Sprintf("update: %s is running", orText(p.To, u.running().Version)))
				} else {
					u.log(fmt.Sprintf("update: %s is running; Roll back goes to %s", orText(p.To, u.running().Version), orText(p.From, "the previous version")))
				}
				u.retireLoop()
			})
		} else {
			u.spawn(u.retireLoop)
		}
	}
	u.scheduled = true
	t := u.o.Timings
	u.mu.Unlock()
	u.spawn(func() {
		select {
		case <-u.stop:
			return
		case <-time.After(t.FirstCheck):
		}
		u.mu.Lock()
		first := u.policy().Check && u.Mode() != "hosted" && (u.lastTry.IsZero() || u.checkDue(time.Now()))
		u.mu.Unlock()
		if first {
			u.Check()
		}
		tick := time.NewTicker(t.Poll)
		defer tick.Stop()
		for {
			select {
			case <-u.stop:
				return
			case <-tick.C:
			}
			u.mu.Lock()
			due := u.checkDue(time.Now())
			u.mu.Unlock()
			if due {
				u.Check()
			}
		}
	})
}

// setKey sets one key of the JSON object in file, keeping the others and
// their order, as {...back, noted: true} written with JSON.stringify(_, null, 1).
func setKey(file, key string, value any) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	o, err := jsonobj.Parse(b)
	if err != nil {
		return err
	}
	if err := o.SetValue(key, value); err != nil {
		return err
	}
	out, err := o.Indent(" ")
	if err != nil {
		return err
	}
	return writeRename(file, append(out, '\n'))
}

// Stop ends the background work.
// spawn runs f in a goroutine that wait counts.
func (u *Updater) spawn(f func()) {
	u.bg.Add(1)
	go func() {
		defer u.bg.Done()
		f()
	}()
}

// wait stops the updater and waits for what it started: a check, an
// install, the retire loop, and launchctl or lsregister still running.
func (u *Updater) wait() {
	u.Stop()
	u.bg.Wait()
}

func (u *Updater) Stop() {
	u.mu.Lock()
	defer u.mu.Unlock()
	select {
	case <-u.stop:
	default:
		close(u.stop)
	}
	u.stopIdleWait()
}

// reopenWanted: updates/reopen asks for the window to be reopened. A mark
// for the version running, under a window that is this copy's own program,
// is stale (the window was reopened, and a shell from before v1.189 never
// removed the mark); it is removed.
func (u *Updater) reopenWanted(running string) bool {
	mark := u.inUpdates("reopen")
	b, err := os.ReadFile(mark)
	if err != nil {
		return false
	}
	if !sameVersion(strings.TrimSpace(string(b)), running) {
		return true
	}
	root := u.payloadRoot()
	exe := u.shellExe()
	if root == "" || exe == "" {
		return true
	}
	if rel, err := filepath.Rel(root, exe); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true
	}
	_ = os.Remove(mark)
	return false
}

// shellExe is the program of the window shell among this server's
// ancestors (a process whose program is named activate-studio*), or "".
func (u *Updater) shellExe() string {
	if u.o.ShellExe != nil {
		return u.o.ShellExe()
	}
	if u.o.GOOS != "linux" {
		return ""
	}
	pid := os.Getppid()
	for i := 0; i < 12 && pid > 1; i++ {
		exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		if err == nil && strings.HasPrefix(filepath.Base(exe), "activate-studio") && !strings.HasSuffix(exe, " (deleted)") {
			return exe
		}
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return ""
		}
		st := string(b)
		f := strings.Fields(st[strings.LastIndexByte(st, ')')+1:])
		if len(f) < 2 {
			return ""
		}
		next, err := strconv.Atoi(f[1])
		if err != nil {
			return ""
		}
		pid = next
	}
	return ""
}
