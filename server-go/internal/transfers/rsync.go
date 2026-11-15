package transfers

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// rsyncMover runs rsync: on this computer between two folders here or to
// or from a host (over the host's own ssh or pw ssh, as rsync's remote
// shell), on a host between two folders there, or on the source host
// straight to a destination host it can reach over ssh.
//
// The Studio lists the source first and gives rsync that list of plain
// files (--files-from, NUL separated), so names go over rsync's own
// protocol, never through a shell, and hidden files and links stay out:
// neither -l nor -L is given, so a link put there since is skipped and then
// missing at the check. rsync writes only into the job's staging folder,
// and nothing takes its name until the Studio has walked that folder.
// A sync's change detection is the Studio's own, the same rule as rsync's
// quick check (size and modification time, or the file's sha256 with
// checksum), made at the plan so the plan can list what is replaced; rsync
// is then given just those files.
//
// Studio uses the rsync installed on each end, and the two ends often run
// different releases. Each end's rsync is read once (--version, --help and
// a --trust-sender probe, kept five minutes) and the options for a
// transfer are chosen from what the ends that must understand each one
// offer (rsyncDecide). Pulling from a host needs an rsync here that
// refuses names the sender adds to the list it was given (CVE-2022-29154);
// where that is missing the plan says so and studio-copy runs.
type rsyncMover struct {
	s *Service
	inProcess

	mu    sync.Mutex
	known map[string]rsyncKnown
}

type rsyncKnown struct {
	at   time.Time
	caps rsyncCaps
	why  string
}

const rsyncKnownFor = 5 * time.Minute

func newRsyncMover(s *Service) *rsyncMover { return &rsyncMover{s: s, known: map[string]rsyncKnown{}} }

func (m *rsyncMover) Name() string             { return MoverRsync }
func (m *rsyncMover) PollEvery() time.Duration { return 250 * time.Millisecond }

// FindRsync is the rsync program on this computer for a setting: an
// absolute path as it is, else the name on PATH, else in the folders a
// package manager puts it (a desktop launcher can start the Studio with a
// short PATH); the name itself when none has it.
func FindRsync(getenv func(string) string, name string) string {
	if name == "" {
		name = "rsync"
	}
	if strings.Contains(name, "/") {
		return name
	}
	for _, d := range filepath.SplitList(getenv("PATH")) {
		if d != "" && runnable(filepath.Join(d, name)) {
			return filepath.Join(d, name)
		}
	}
	for _, d := range []string{"/usr/bin", "/bin", "/usr/local/bin", "/opt/homebrew/bin", "/opt/local/bin"} {
		if runnable(filepath.Join(d, name)) {
			return filepath.Join(d, name)
		}
	}
	return name
}

func runnable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0
}

// rsyncGOOS and brewRsync are variables so tests can stand in for a Mac.
var (
	rsyncGOOS = runtime.GOOS
	// brewRsync is where Homebrew installs rsync: Apple silicon, then
	// Intel.
	brewRsync = []string{"/opt/homebrew/bin/rsync", "/usr/local/bin/rsync"}
)

// rsyncCandidates are the programs the rsync setting may mean, in order.
// On a Mac with the default setting ("rsync"), Homebrew's rsync is looked
// for before PATH: macOS's own /usr/bin/rsync is 2.6.9 or openrsync, and a
// login PATH that puts Homebrew first is not always what the app gets. An
// explicit path is used as it is.
func rsyncCandidates(getenv func(string) string, name string) []string {
	if name == "" {
		name = "rsync"
	}
	var out []string
	if name == "rsync" && rsyncGOOS == "darwin" {
		for _, p := range brewRsync {
			if runnable(p) {
				out = append(out, p)
			}
		}
	}
	if p := FindRsync(getenv, name); !slices.Contains(out, p) {
		out = append(out, p)
	}
	return out
}

// local is the rsync this computer uses under the settings a plan was
// made with: of the candidates, the best (rsyncCaps.better); why says what
// is wrong when there is none.
func (m *rsyncMover) local(ctx context.Context, set Settings) (string, rsyncCaps, string) {
	var path, why string
	var best rsyncCaps
	for _, p := range rsyncCandidates(m.s.d.Getenv, set.Movers.Rsync.Path) {
		c, w := m.caps(ctx, "", "", "this computer", p)
		if w != "" {
			if why == "" {
				why = w
			}
			continue
		}
		if path == "" || c.better(best) {
			path, best = p, c
		}
	}
	if path == "" {
		return "", rsyncCaps{}, why
	}
	return path, best, ""
}

// remoteRsync is the rsync program on a host: its saved location's, else
// rsync on its PATH.
func remoteRsync(r Remote) string {
	if f, ok := r.(*RemoteFS); ok && f.RsyncPath != "" {
		return f.RsyncPath
	}
	return "rsync"
}

var rsyncVer = regexp.MustCompile(`(?i)rsync\s+version\s+v?(\d+)\.(\d+)\.(\d+)`)

// parseRsyncVersion is the version in rsync --version's first line;
// openrsync says it is compatible with 2.6.9, and is taken as that.
func parseRsyncVersion(line string) ([3]int, bool) {
	if strings.Contains(strings.ToLower(line), "openrsync") {
		return [3]int{2, 6, 9}, true
	}
	x := rsyncVer.FindStringSubmatch(line)
	if x == nil {
		return [3]int{}, false
	}
	a, _ := strconv.Atoi(x[1])
	b, _ := strconv.Atoi(x[2])
	c, _ := strconv.Atoi(x[3])
	return [3]int{a, b, c}, true
}

func atLeast(v, want [3]int) bool {
	for i := 0; i < 3; i++ {
		if v[i] != want[i] {
			return v[i] > want[i]
		}
	}
	return true
}

// rsyncProbe is what one rsync program said: its --version and --help
// output, and whether it took --trust-sender.
type rsyncProbe struct {
	version, help string
	trust         bool
}

// probeLocalRsync runs a program here; ok is false when it does not run.
// openrsync may print its usage on standard error, so both are read.
func probeLocalRsync(ctx context.Context, program string) (rsyncProbe, bool) {
	out, err := exec.CommandContext(ctx, program, "--version").CombinedOutput()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		return rsyncProbe{}, false
	}
	p := rsyncProbe{version: string(out)}
	help, _ := exec.CommandContext(ctx, program, "--help").CombinedOutput()
	p.help = string(help)
	p.trust = exec.CommandContext(ctx, program, "--trust-sender", "--version").Run() == nil
	return p, true
}

// caps is rsync's capabilities on this computer (site "") or a host, kept
// five minutes; why says what is wrong when there is no usable rsync.
func (m *rsyncMover) caps(ctx context.Context, site, key, where, program string) (rsyncCaps, string) {
	k := site + "|" + keyTag(key) + "|" + program
	m.mu.Lock()
	e, ok := m.known[k]
	m.mu.Unlock()
	if ok && time.Since(e.at) < rsyncKnownFor {
		return e.caps, e.why
	}
	var p rsyncProbe
	found, clean := false, true
	if site == "" {
		p, found = probeLocalRsync(ctx, program)
	} else {
		p, found, clean = RsyncProbe(ctx, m.s.d.Runner, site, key, program)
	}
	e = rsyncKnown{at: time.Now()}
	if !found {
		e.why = "rsync is not on " + where
	} else if !clean {
		e.why = "the login on " + where + " prints text before a command runs, which rsync's protocol cannot pass"
	} else if c, ok := parseRsyncCaps(p.version, p.help, p.trust); !ok {
		e.why = "the rsync on " + where + " did not give its version"
	} else {
		e.caps = c
	}
	m.mu.Lock()
	m.known[k] = e
	m.mu.Unlock()
	return e.caps, e.why
}

// route is how one transfer runs.
type rsyncRoute struct {
	kind    string // here, pull, push, onhost, hostToHost
	srcSite string
	dstSite string
	key     string
	dstKey  string
	dstHost *remote.SSHHost
	// bin is the client program for here, pull and push.
	bin string
	o   rsyncOpts
}

// rsyncOpts are the options chosen for one route from both ends' rsync.
type rsyncOpts struct {
	protect  bool   // -s
	escapes  bool   // the client escapes remote file arguments itself
	noIncRec bool   // --no-inc-recursive
	maxSize  bool   // --max-size
	timeout  bool   // --timeout
	from0    string // --from0 or -0
	progress bool   // --info=progress2
}

// rsyncDecide chooses a route's options from the capabilities of the rsync
// that runs the command (client: this computer for here, push and pull;
// the source host for onhost and hostToHost) and of the far end's (far:
// the rsync the client starts over a remote shell; nil for here and
// onhost, where no shell stands between the ends). It returns a reason
// when rsync cannot carry the route. where names client and far for
// messages.
//
// What the client passes to the far end follows server_options() in
// rsync's options.c (checked against 3.0.9 and 3.4.1):
//   - -s (protect args) goes in the server's flags: both ends need it.
//   - -t goes in the server's flags; every release has it.
//   - --timeout is always passed on: both ends need it.
//   - --max-size is passed on only when the client sends (push,
//     hostToHost), since the receiver applies it.
//   - --files-from and --from0: a sending client reads the list itself and
//     passes nothing; a receiving client (pull) passes --files-from=- and
//     --from0 and sends the list over the connection, so the far end needs
//     both.
//   - --chmod is never passed on: the sender applies it as it lists files
//     (send_file_name) and the receiver as it reads them (recv_file_entry),
//     so the client's own is enough.
//   - --no-inc-recursive only clears the "i" the client puts in its -e.
//     capability string (protocol 30); it is not passed on.
//   - --info=progress2 is the client's own output.
//   - --trust-sender is only detected, never given.
func rsyncDecide(kind string, client rsyncCaps, far *rsyncCaps, clientWhere, farWhere string) (rsyncOpts, string) {
	if o := client.lacks(rsyncNeeds...); o != "" {
		return rsyncOpts{}, "the rsync on " + clientWhere + " (" + client.name() + ") has no " + o + ", which Studio needs; studio-copy carries the data"
	}
	if kind == "pull" {
		if !client.checksSender() {
			return rsyncOpts{}, "the rsync on " + clientWhere + " (" + client.name() + ") trusts what a host sends (CVE-2022-29154); a pull " + pullNeedsFix + ", so studio-copy carries the transfer"
		}
		if o := far.lacks(pullNeeds...); o != "" {
			return rsyncOpts{}, "the rsync on " + farWhere + " (" + far.name() + ") has no " + o + ", which a pull needs there; studio-copy carries the data"
		}
	}
	o := rsyncOpts{
		escapes:  client.escapesArgs(),
		noIncRec: client.incRecursive(),
		from0:    client.from0(),
		progress: client.has("info") && (kind == "here" || kind == "push" || kind == "pull"),
	}
	if far != nil {
		o.protect = client.protectArgs() && far.protectArgs()
		o.timeout = client.has("timeout") && far.has("timeout")
		o.maxSize = client.has("max-size") && (kind == "pull" || far.has("max-size"))
	} else {
		o.timeout = client.has("timeout")
		o.maxSize = client.has("max-size")
	}
	return o, ""
}

// quotedNote is the plan's note on a route through a remote shell without
// protect args.
func quotedNote(o rsyncOpts, client rsyncCaps, far *rsyncCaps, clientWhere, farWhere string) string {
	if far == nil || o.protect {
		return ""
	}
	who := "the rsync on " + farWhere + " (" + far.name() + ")"
	if !client.protectArgs() {
		who = "the rsync on " + clientWhere + " (" + client.name() + ")"
	}
	return "; " + who + " has no --protect-args, so the folder on " + farWhere + " is quoted for its shell and file names go in the file list"
}

func (m *rsyncMover) route(ctx context.Context, p *planRec) (rsyncRoute, string) {
	if p.src.fs == nil {
		return rsyncRoute{}, "it reads files Studio can list, and Studio cannot list " + p.src.Label
	}
	// rsync runs a program on each host it reaches; an account that takes
	// SFTP only runs none.
	for _, f := range []FS{p.src.fs, p.dst.fs} {
		if isSFTPOnly(ctx, f) {
			return rsyncRoute{}, f.Where() + " takes SFTP only, so rsync cannot run there; studio-copy carries the data over SFTP"
		}
	}
	if m.s.d.Shell == nil && (isRemote(p.src.fs) || isRemote(p.dst.fs)) {
		return rsyncRoute{}, "no remote shell is set up for it"
	}
	sr, srcRemote := p.src.fs.(Remote)
	dr, dstRemote := p.dst.fs.(Remote)
	on := func(r Remote) (rsyncCaps, string) {
		return m.caps(ctx, r.Site(), r.Key(), r.Where(), remoteRsync(r))
	}
	const here = "this computer"
	if !srcRemote || !dstRemote {
		bin, lc, why := m.local(ctx, p.set)
		if why != "" {
			return rsyncRoute{}, why
		}
		switch {
		case !srcRemote && !dstRemote:
			o, why := rsyncDecide("here", lc, nil, here, "")
			if why != "" {
				return rsyncRoute{}, why
			}
			return rsyncRoute{kind: "here", bin: bin, o: o}, "both ends are on this computer"
		case srcRemote:
			fc, why := on(sr)
			if why != "" {
				return rsyncRoute{}, why
			}
			o, why := rsyncDecide("pull", lc, &fc, here, sr.Where())
			if why != "" {
				return rsyncRoute{}, why
			}
			return rsyncRoute{kind: "pull", srcSite: sr.Site(), key: sr.Key(), bin: bin, o: o},
				"rsync on this computer pulls from " + sr.Where() + quotedNote(o, lc, &fc, here, sr.Where())
		default:
			fc, why := on(dr)
			if why != "" {
				return rsyncRoute{}, why
			}
			o, why := rsyncDecide("push", lc, &fc, here, dr.Where())
			if why != "" {
				return rsyncRoute{}, why
			}
			return rsyncRoute{kind: "push", dstSite: dr.Site(), key: dr.Key(), bin: bin, o: o},
				"rsync on this computer sends to " + dr.Where() + quotedNote(o, lc, &fc, here, dr.Where())
		}
	}
	if sr.Site() == dr.Site() && sr.Key() == dr.Key() {
		sc, why := on(sr)
		if why != "" {
			return rsyncRoute{}, why
		}
		o, why := rsyncDecide("onhost", sc, nil, sr.Where(), "")
		if why != "" {
			return rsyncRoute{}, why
		}
		return rsyncRoute{kind: "onhost", srcSite: sr.Site(), key: sr.Key(), o: o}, "rsync runs on " + sr.Where() + ", so the data stays on that host"
	}
	// Two hosts: rsync on the source sends straight to the destination when
	// the source can reach it over plain ssh with no options of the
	// Studio's own (its keys and options are on this computer).
	h, err := remote.ParseSSHSite(dr.Site())
	if err != nil || h == nil {
		return rsyncRoute{}, dr.Where() + " is reached through pw ssh, which " + sr.Where() + " cannot use; studio-copy carries the data through Studio"
	}
	for _, o := range h.Options {
		if o != "-p" && !strings.HasPrefix(o, "Port") {
			return rsyncRoute{}, dr.Where() + " is reached with ssh options of this computer; studio-copy carries the data through Studio"
		}
	}
	sc, why := on(sr)
	if why != "" {
		return rsyncRoute{}, why
	}
	dc, why := on(dr)
	if why != "" {
		return rsyncRoute{}, why
	}
	o, why := rsyncDecide("hostToHost", sc, &dc, sr.Where(), dr.Where())
	if why != "" {
		return rsyncRoute{}, why
	}
	if why := m.reaches(ctx, sr, h); why != "" {
		return rsyncRoute{}, why
	}
	return rsyncRoute{kind: "hostToHost", srcSite: sr.Site(), key: sr.Key(), dstHost: h, o: o},
		"rsync on " + sr.Where() + " sends straight to " + dr.Where() + quotedNote(o, sc, &dc, sr.Where(), dr.Where())
}

func isRemote(f FS) bool { _, ok := f.(Remote); return ok }

// reaches checks with a dry ssh in batch mode from the source host.
func (m *rsyncMover) reaches(ctx context.Context, from Remote, to *remote.SSHHost) string {
	argv := []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10"}
	if to.Port != 0 {
		argv = append(argv, "-p", strconv.Itoa(to.Port))
	}
	argv = append(argv, to.Destination, "true")
	res, err := m.s.d.Runner.RunOnce(ctx, from.Site(), from.Key(), argv, 30*time.Second, nil)
	if err != nil || res.Code != 0 {
		return from.Where() + " cannot reach " + to.Destination + " over ssh without a password; studio-copy carries the data through Studio"
	}
	return ""
}

func (m *rsyncMover) CanReach(ctx context.Context, p *planRec) (bool, string) {
	r, why := m.route(ctx, p)
	return r.kind != "", why
}

// groups splits a job's files by the source folder each is sent from:
// the part of Rel above Out.
type rsyncGroup struct {
	base  string
	files []item
}

func groupsOf(files []item) []rsyncGroup {
	var out []rsyncGroup
	at := map[string]int{}
	for _, f := range files {
		base := ""
		if f.Rel != f.Out {
			base = strings.TrimSuffix(f.Rel, "/"+f.Out)
		}
		i, ok := at[base]
		if !ok {
			i = len(out)
			at[base] = i
			out = append(out, rsyncGroup{base: base})
		}
		out[i].files = append(out[i].files, f)
	}
	return out
}

func (m *rsyncMover) Start(ctx context.Context, j *MoveJob) (string, error) {
	p := &planRec{src: j.SrcLoc, dst: j.DstLoc, set: j.Set}
	r, why := m.route(ctx, p)
	if r.kind == "" {
		return "", errors.New(why)
	}
	return m.start(func(ctx context.Context, pj *procJob) error {
		for _, g := range groupsOf(j.Files) {
			if err := m.runGroup(ctx, j, r, g, pj); err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		return nil
	}), nil
}

func absUnder(root, rel string) string {
	if rel == "" {
		return root
	}
	return path.Join(root, rel)
}

// rsyncCmd is one rsync command line, built by argv.
type rsyncCmd struct {
	kind     string
	o        rsyncOpts
	program  string // the client: this computer's rsync, or the source host's
	listFile string // the file list here; "" reads it from standard input
	rsh      string // the remote shell script (push, pull)
	farRsync string // the far end's rsync program
	dstHost  *remote.SSHHost
	from, to string
	largest  int64
	preserve bool
}

// shQuote quotes s for a POSIX shell: in single quotes, each single quote
// written as '\”.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// escapeWild puts a backslash before each of rsync's wildcard characters.
// rsync 3.2.4 and later backslash-escapes the shell's characters in a
// remote file argument but leaves *?[] for the remote shell to expand,
// and keeps a backslash that stands before one (safe_arg in options.c).
func escapeWild(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`*?[]`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// remotePath is a folder on the far end as the client is given it.
//
// Without protect args the client puts the folder on the remote shell's
// command line. rsync 2.6.9 through 3.2.3 (and openrsync) pass it as it is
// (2.6.9 handles remote arguments the same way 3.0 does without -s), so
// it is quoted here for a POSIX shell. rsync 3.2.4 and later escapes it
// itself, so it gets only the wildcard escapes, which that escaping
// leaves out. With protect args it goes over the protocol as it is. File
// names never take this path: they go in the --files-from list, which the
// sender reads itself, or which a receiving client sends over the
// connection.
func (o rsyncOpts) remotePath(p string) string {
	switch {
	case o.protect:
		return p
	case o.escapes:
		return escapeWild(p)
	}
	return shQuote(p)
}

// argv is the command line, program first. For onhost and hostToHost it
// runs on the source host through env, which drops RSYNC_OLD_ARGS and
// RSYNC_PROTECT_ARGS so a login's setting cannot change how the folder is
// quoted; here the environment is cleaned the same way (rsyncEnv).
func (c rsyncCmd) argv() []string {
	var a []string
	if c.kind == "onhost" || c.kind == "hostToHost" {
		a = append(a, "env", "-u", "RSYNC_OLD_ARGS", "-u", "RSYNC_PROTECT_ARGS")
	}
	a = append(a, c.program)
	if c.o.protect {
		a = append(a, "-s")
	}
	a = append(a, "--chmod=Du+rwx,Fu+rw")
	if c.o.maxSize {
		a = append(a, fmt.Sprintf("--max-size=%d", max(c.largest, 1)))
	}
	if c.o.timeout {
		a = append(a, "--timeout=120")
	}
	if c.o.noIncRec {
		a = append(a, "--no-inc-recursive")
	}
	a = append(a, c.o.from0)
	if c.preserve {
		a = append(a, "-t")
	}
	list := c.listFile
	if list == "" {
		list = "-"
	}
	a = append(a, "--files-from="+list)
	if c.o.progress {
		a = append(a, "--info=progress2")
	}
	if c.rsh != "" {
		a = append(a, "--rsh="+c.rsh)
	}
	if c.farRsync != "" && c.farRsync != "rsync" {
		a = append(a, "--rsync-path="+c.farRsync)
	}
	switch c.kind {
	case "pull":
		return append(a, "studio:"+c.o.remotePath(c.from), c.to)
	case "push":
		return append(a, c.from, "studio:"+c.o.remotePath(c.to))
	case "hostToHost":
		sh := "ssh -o BatchMode=yes"
		if c.dstHost.Port != 0 {
			sh += " -p " + strconv.Itoa(c.dstHost.Port)
		}
		return append(a, "-e", sh, c.from, c.dstHost.Destination+":"+c.o.remotePath(c.to))
	}
	return append(a, c.from, c.to)
}

// rsyncEnv is the environment rsync runs with here: RSYNC_OLD_ARGS and
// RSYNC_PROTECT_ARGS dropped, since they change how a remote folder is
// quoted, and messages in the C locale.
func rsyncEnv(env []string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k == "RSYNC_OLD_ARGS" || k == "RSYNC_PROTECT_ARGS" || k == "LC_ALL" {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "LC_ALL=C")
}

func (m *rsyncMover) runGroup(ctx context.Context, j *MoveJob, r rsyncRoute, g rsyncGroup, pj *procJob) error {
	var list bytes.Buffer
	var largest, total int64
	for _, f := range g.files {
		list.WriteString(f.Out)
		list.WriteByte(0)
		largest = max(largest, f.Size)
		total += f.Size
	}
	c := rsyncCmd{
		kind:     r.kind,
		o:        r.o,
		program:  r.bin,
		from:     absUnder(j.Src.Root(), g.base) + "/",
		to:       absUnder(j.Dst.Root(), j.Stage) + "/",
		largest:  largest,
		preserve: j.Preserve,
	}
	base := struct{ files, bytes int64 }{pj.files.Load(), pj.bytes.Load()}
	switch r.kind {
	case "onhost", "hostToHost":
		c.program = remoteRsync(j.Src.(Remote))
		c.farRsync = remoteRsync(j.Dst.(Remote))
		c.dstHost = r.dstHost
		res, err := m.s.d.Runner.RunOnce(ctx, r.srcSite, r.key, c.argv(), 24*time.Hour, &list)
		if err != nil {
			return err
		}
		if res.Code != 0 {
			return rsyncFailed(res.Stderr, res.Code)
		}
		pj.files.Add(int64(len(g.files)))
		pj.bytes.Add(total)
		return nil
	}
	dir, err := os.MkdirTemp("", "studio-rsync-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	c.listFile = filepath.Join(dir, "files")
	if err := os.WriteFile(c.listFile, list.Bytes(), 0o600); err != nil {
		return err
	}
	env := os.Environ()
	switch r.kind {
	case "pull", "push":
		site := r.srcSite
		far := j.Src
		if r.kind == "push" {
			site, far = r.dstSite, j.Dst
		}
		argv, extra, err := m.s.d.Shell(site, r.key)
		if err != nil {
			return err
		}
		c.rsh = filepath.Join(dir, "rsh")
		// rsync runs the remote shell as `rsh <host> <command words>`; the
		// script drops the host and hands the words to ssh or pw ssh as one
		// command, as each takes it.
		script := "#!/bin/sh\nshift\nexec " + remote.DirectCommand(argv) + " \"$*\"\n"
		if err := os.WriteFile(c.rsh, []byte(script), 0o700); err != nil {
			return err
		}
		env = append(env, extra...)
		c.farRsync = remoteRsync(far.(Remote))
	}
	args := c.argv()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = rsyncEnv(env)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Split(scanCRLF)
	for sc.Scan() {
		if b, f, ok := parseProgress(sc.Text()); ok {
			pj.bytes.Store(base.bytes + b)
			pj.files.Store(base.files + f)
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		code := -1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return rsyncFailed(stderr.String(), code)
	}
	pj.files.Store(base.files + int64(len(g.files)))
	pj.bytes.Store(base.bytes + total)
	return nil
}

func rsyncFailed(stderr string, code int) error {
	var keep []string
	for _, l := range strings.Split(remote.QuietStderr(stderr), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "rsync error:") && !strings.HasPrefix(l, "(see the rsync manpage") {
			keep = append(keep, l)
		}
	}
	if len(keep) > 0 {
		return errors.New("rsync: " + short(keep[len(keep)-1]))
	}
	return fmt.Errorf("rsync ended with status %d", code)
}

var progressRe = regexp.MustCompile(`^\s*([\d,]+)\s+\d+%`)
var xfrRe = regexp.MustCompile(`xfr#(\d+)`)

// parseProgress reads a --info=progress2 line: bytes so far and files done.
func parseProgress(line string) (int64, int64, bool) {
	m := progressRe.FindStringSubmatch(line)
	if m == nil {
		return 0, 0, false
	}
	b, _ := strconv.ParseInt(strings.ReplaceAll(m[1], ",", ""), 10, 64)
	var f int64
	if x := xfrRe.FindStringSubmatch(line); x != nil {
		f, _ = strconv.ParseInt(x[1], 10, 64)
	}
	return b, f, true
}

func scanCRLF(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func (m *rsyncMover) Status(ctx context.Context, ref string) (MoverStatus, error) {
	return m.status(ref), nil
}

func (m *rsyncMover) Cancel(ref string) bool { return m.cancel(ref) }
