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
// Pulling from a host needs rsync 3.2.5 or later here, which refuses names
// the sender adds to the list it was given; where that is missing the plan
// says so and studio-copy runs. A sync's change detection is the Studio's
// own, the same rule as rsync's quick check (size and modification time,
// or the file's sha256 with checksum), made at the plan so the plan can
// list what is replaced; rsync is then given just those files.
type rsyncMover struct {
	s *Service
	inProcess

	mu    sync.Mutex
	known map[string]rsyncKnown
}

type rsyncKnown struct {
	at  time.Time
	ver [3]int
	why string
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

// bin is the rsync program here under the settings a plan was made with.
func (m *rsyncMover) bin(set Settings) string {
	return FindRsync(m.s.d.Getenv, set.Movers.Rsync.Path)
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

// version is rsync's version on this computer (site "") or a host, kept
// five minutes; why says what is wrong when there is none.
func (m *rsyncMover) version(ctx context.Context, site, key, where, program string) ([3]int, string) {
	k := site + "|" + keyTag(key) + "|" + program
	m.mu.Lock()
	e, ok := m.known[k]
	m.mu.Unlock()
	if ok && time.Since(e.at) < rsyncKnownFor {
		return e.ver, e.why
	}
	var line string
	clean := true
	if site == "" {
		out, err := exec.CommandContext(ctx, program, "--version").Output()
		if err == nil {
			line, _, _ = strings.Cut(string(out), "\n")
		}
	} else {
		line, clean = RsyncVersion(ctx, m.s.d.Runner, site, key, program)
	}
	e = rsyncKnown{at: time.Now()}
	if line == "" {
		e.why = "rsync is not on " + where
	} else if !clean {
		e.why = "the login on " + where + " prints text before a command runs, which rsync's protocol cannot pass"
	} else if v, ok := parseRsyncVersion(line); !ok {
		e.why = "the rsync on " + where + " did not give its version"
	} else {
		e.ver = v
	}
	m.mu.Lock()
	m.known[k] = e
	m.mu.Unlock()
	return e.ver, e.why
}

// route is how one transfer runs.
type rsyncRoute struct {
	kind    string // here, pull, push, onhost, hostToHost
	srcSite string
	dstSite string
	key     string
	dstKey  string
	dstHost *remote.SSHHost
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
	min3, min325 := [3]int{3, 0, 0}, [3]int{3, 2, 5}
	here := func(want [3]int) string {
		v, why := m.version(ctx, "", "", "this computer", m.bin(p.set))
		if why != "" {
			return why
		}
		if !atLeast(v, want) {
			return fmt.Sprintf("the rsync on this computer (%d.%d.%d) is older than %d.%d.%d", v[0], v[1], v[2], want[0], want[1], want[2])
		}
		return ""
	}
	on := func(r Remote) string {
		v, why := m.version(ctx, r.Site(), r.Key(), r.Where(), remoteRsync(r))
		if why != "" {
			return why
		}
		if !atLeast(v, min3) {
			return fmt.Sprintf("the rsync on %s (%d.%d.%d) is older than 3.0.0", r.Where(), v[0], v[1], v[2])
		}
		return ""
	}
	switch {
	case !srcRemote && !dstRemote:
		if why := here(min3); why != "" {
			return rsyncRoute{}, why
		}
		return rsyncRoute{kind: "here"}, "both ends are on this computer"
	case srcRemote && !dstRemote:
		if why := here(min325); why != "" {
			return rsyncRoute{}, why + ", which checks what a host sends"
		}
		if why := on(sr); why != "" {
			return rsyncRoute{}, why
		}
		return rsyncRoute{kind: "pull", srcSite: sr.Site(), key: sr.Key()}, "rsync on this computer pulls from " + sr.Where()
	case !srcRemote && dstRemote:
		if why := here(min3); why != "" {
			return rsyncRoute{}, why
		}
		if why := on(dr); why != "" {
			return rsyncRoute{}, why
		}
		return rsyncRoute{kind: "push", dstSite: dr.Site(), key: dr.Key()}, "rsync on this computer sends to " + dr.Where()
	case sr.Site() == dr.Site() && sr.Key() == dr.Key():
		if why := on(sr); why != "" {
			return rsyncRoute{}, why
		}
		return rsyncRoute{kind: "onhost", srcSite: sr.Site(), key: sr.Key()}, "rsync runs on " + sr.Where() + ", so the data stays on that host"
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
	if why := on(sr); why != "" {
		return rsyncRoute{}, why
	}
	if why := on(dr); why != "" {
		return rsyncRoute{}, why
	}
	if why := m.reaches(ctx, sr, h); why != "" {
		return rsyncRoute{}, why
	}
	return rsyncRoute{kind: "hostToHost", srcSite: sr.Site(), key: sr.Key(), dstHost: h}, "rsync on " + sr.Where() + " sends straight to " + dr.Where()
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

func (m *rsyncMover) options(j *MoveJob, largest int64) []string {
	o := []string{"-s", "--chmod=Du+rwx,Fu+rw", fmt.Sprintf("--max-size=%d", max(largest, 1)), "--timeout=120", "--no-inc-recursive", "--from0"}
	if j.Preserve {
		o = append(o, "-t")
	}
	return o
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
	from := absUnder(j.Src.Root(), g.base) + "/"
	to := absUnder(j.Dst.Root(), j.Stage) + "/"
	opts := m.options(j, largest)
	base := struct{ files, bytes int64 }{pj.files.Load(), pj.bytes.Load()}
	switch r.kind {
	case "onhost", "hostToHost":
		argv := append([]string{remoteRsync(j.Src.(Remote))}, opts...)
		argv = append(argv, "--files-from=-")
		if p := remoteRsync(j.Dst.(Remote)); p != "rsync" {
			argv = append(argv, "--rsync-path="+p)
		}
		if r.kind == "hostToHost" {
			sh := "ssh -o BatchMode=yes"
			if r.dstHost.Port != 0 {
				sh += " -p " + strconv.Itoa(r.dstHost.Port)
			}
			argv = append(argv, "-e", sh, from, r.dstHost.Destination+":"+to)
		} else {
			argv = append(argv, from, to)
		}
		res, err := m.s.d.Runner.RunOnce(ctx, r.srcSite, r.key, argv, 24*time.Hour, &list)
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
	listFile := filepath.Join(dir, "files")
	if err := os.WriteFile(listFile, list.Bytes(), 0o600); err != nil {
		return err
	}
	args := append(opts, "--files-from="+listFile)
	if v, _ := m.version(ctx, "", "", "this computer", m.bin(j.Set)); atLeast(v, [3]int{3, 1, 0}) {
		args = append(args, "--info=progress2")
	}
	env := os.Environ()
	switch r.kind {
	case "pull", "push":
		site := r.srcSite
		if r.kind == "push" {
			site = r.dstSite
		}
		argv, extra, err := m.s.d.Shell(site, r.key)
		if err != nil {
			return err
		}
		rsh := filepath.Join(dir, "rsh")
		// rsync runs the remote shell as `rsh <host> <command words>`; the
		// script drops the host and hands the words to ssh or pw ssh as one
		// command, as each takes it.
		script := "#!/bin/sh\nshift\nexec " + remote.DirectCommand(argv) + " \"$*\"\n"
		if err := os.WriteFile(rsh, []byte(script), 0o700); err != nil {
			return err
		}
		env = append(env, extra...)
		args = append(args, "--rsh="+rsh)
		far := j.Dst
		if r.kind == "pull" {
			far = j.Src
		}
		if p := remoteRsync(far.(Remote)); p != "rsync" {
			args = append(args, "--rsync-path="+p)
		}
		if r.kind == "pull" {
			args = append(args, "studio:"+from, to)
		} else {
			args = append(args, from, "studio:"+to)
		}
	default:
		args = append(args, from, to)
	}
	cmd := exec.CommandContext(ctx, m.bin(j.Set), args...)
	cmd.Env = append(env, "LC_ALL=C")
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
