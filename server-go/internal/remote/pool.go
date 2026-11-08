package remote

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

const (
	idleTimeout = 10 * time.Minute
	stderrTail  = 4000
	// socketName is a socket's name when %C does not fit: 12 hex
	// characters. ssh binds the socket at its path plus 17 characters, and
	// macOS allows 104 bytes, so the whole path stays within 86.
	socketName    = 12
	socketPathMax = 86
	percentC      = 40
	optionsTagLen = 8
)

// loop is the remote end of a session (remoteExec.ts LOOP), byte for byte.
var loop = strings.Join([]string{
	"set -f",
	`d=-d; [ "$(printf eA== | base64 -d 2>/dev/null)" = x ] || d=-D`,
	"os=$(uname -s 2>/dev/null)",
	"while IFS= read -r line; do",
	`  args=(); for a in $line; do args+=("$(printf %s "${a#x}" | base64 $d)"); done`,
	`  if [ "$os" = Darwin ]; then`,
	`    case "${args[0]}" in /*/*/*) case "${args[0]##*/}" in gufi_*) args=(env "DYLD_LIBRARY_PATH=${args[0]%/*/*}/lib" "${args[@]}") ;; esac ;; esac`,
	"  fi",
	"  o=$(mktemp); e=$(mktemp)",
	`  "${args[@]}" </dev/null >"$o" 2>"$e"; rc=$?`,
	"  printf '%s o%s e%s\\n' \"$rc\" \"$(base64 <\"$o\" | tr -d '\\n')\" \"$(tail -c " + strconv.Itoa(stderrTail) + " \"$e\" | base64 | tr -d '\\n')\"",
	`  rm -f "$o" "$e"`,
	"done",
}, "\n")

// BootstrapCommand is bootstrapCommand: the command that starts the loop.
func BootstrapCommand(site string) string {
	b64 := base64.StdEncoding.EncodeToString([]byte(loop))
	if IsSSH(site) {
		return `bash -c "$(echo ` + b64 + ` | base64 -D 2>/dev/null || echo ` + b64 + ` | base64 -d)"`
	}
	return `bash -c "$(echo ` + b64 + ` | base64 -d)"`
}

// Result is a command's answer (RemoteResult).
type Result struct {
	Code      int
	Stdout    string
	Raw       []byte
	Stderr    string
	StderrCut bool
}

// Options configure a Pool. Zero values take remoteExec.ts's defaults from
// the environment (Getenv).
type Options struct {
	Getenv func(string) string // nil: os.Getenv
	// IndexBase is the data folder; INDEX_BASE/ssh is the last place a
	// socket may go.
	IndexBase string
	// PwCLI is the pw CLI to run (config.ts's PW_CLI, found again at each
	// call); nil: FindPwCLI.
	PwCLI func() string
	// GatewayBase is the model gateway's URL, whose host pw ssh is pointed
	// at (--platform-host); nil: PW_GATEWAY_URL or OPENAI_BASE_URL.
	GatewayBase func() string
	// Spawn starts a session or a direct call; nil: ssh or pw ssh. Tests
	// replace it (remoteExec.ts setRemoteSpawn). STUDIO_REMOTE_SPAWN names
	// a program run as `<program> <site> <command>` with PW_API_KEY set,
	// for tests that run the Go server as a separate process.
	Spawn func(site, key, command string) *exec.Cmd
	// SSHLocal runs ssh on this computer for -G, -O check and -N -f; nil:
	// ssh itself (setSshLocal).
	SSHLocal func(args []string, timeout time.Duration) LocalResult
}

// LocalResult is a local ssh's exit and output.
type LocalResult struct {
	Code           int
	Stdout, Stderr string
}

// Pool holds the sessions to each host and what is known of the hosts.
type Pool struct {
	o       Options
	poolMax int

	mu       sync.Mutex
	channels map[string][]*channel
	direct   map[string]bool
	cpus     map[string]*cpuEntry
	reached  map[string]reachedEntry
	warming  map[string]*warmEntry
	stop     chan struct{}
}

type cpuEntry struct {
	done chan struct{}
	n    int
}

// New makes a Pool and starts its idle sweeper (every 60 s).
func New(o Options) *Pool {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.PwCLI == nil {
		get := o.Getenv
		o.PwCLI = func() string { return FindPwCLI(get, o.IndexBase) }
	}
	if o.GatewayBase == nil {
		get := o.Getenv
		o.GatewayBase = func() string {
			if v := get("PW_GATEWAY_URL"); v != "" {
				return v
			}
			return get("OPENAI_BASE_URL")
		}
	}
	if o.SSHLocal == nil {
		o.SSHLocal = runSSHLocal
	}
	n, err := strconv.Atoi(o.Getenv("STUDIO_REMOTE_SESSIONS"))
	if err != nil || n == 0 {
		n = 3
	}
	p := &Pool{o: o, poolMax: min(max(n, 1), 8), channels: map[string][]*channel{}, direct: map[string]bool{},
		cpus: map[string]*cpuEntry{}, reached: map[string]reachedEntry{}, warming: map[string]*warmEntry{}, stop: make(chan struct{})}
	go p.sweep()
	return p
}

func (p *Pool) sweep() {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.mu.Lock()
			for id, pool := range p.channels {
				for _, c := range pool {
					c.mu.Lock()
					idle := time.Since(c.last) > idleTimeout && len(c.queue) == 0
					c.mu.Unlock()
					if idle {
						c.kill()
						p.dropLocked(id, c)
					}
				}
			}
			p.mu.Unlock()
		}
	}
}

// Close ends every session and forgets what was learned of the hosts
// (closeRemoteChannels).
func (p *Pool) Close() {
	p.mu.Lock()
	for _, pool := range p.channels {
		for _, c := range pool {
			c.kill()
		}
	}
	p.channels = map[string][]*channel{}
	p.cpus = map[string]*cpuEntry{}
	p.direct = map[string]bool{}
	p.warming = map[string]*warmEntry{}
	p.mu.Unlock()
}

// Shutdown is Close and stops the sweeper.
func (p *Pool) Shutdown() {
	p.Close()
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
}

// ---- connection sharing ----

// PersistSeconds is persistSeconds: STUDIO_SSH_PERSIST, default 600, at most a day.
func (p *Pool) PersistSeconds() int {
	v := p.o.Getenv("STUDIO_SSH_PERSIST")
	if v == "" {
		return 600
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || n != math.Trunc(n) || n < 0 {
		return 600
	}
	return int(min(n, 86400))
}

// SocketDirs is socketDirs: where ssh's sockets may go, in order.
func (p *Pool) SocketDirs() []string {
	var out []string
	if xdg := p.o.Getenv("XDG_RUNTIME_DIR"); xdg != "" && filepath.IsAbs(xdg) {
		if _, err := os.Stat(xdg); err == nil {
			out = append(out, filepath.Join(xdg, "activate-studio"))
		}
	}
	out = append(out, "/tmp/activate-studio-"+strconv.Itoa(os.Getuid()))
	return append(out, filepath.Join(p.o.IndexBase, "ssh"))
}

// SocketDir is socketDir: the first of dirs a socket fits in that is the
// user's alone (a directory, not a link, owned by uid, mode 700).
func SocketDir(dirs []string, uid int) string {
	if uid < 0 {
		return ""
	}
	for _, dir := range dirs {
		if len(dir)+1+socketName > socketPathMax || strings.ContainsAny(dir, `"%`) {
			continue
		}
		os.MkdirAll(dir, 0o700)
		st, err := os.Lstat(dir)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if ownedBy(st, uid) && st.Mode().Perm() == 0o700 {
			return dir
		}
	}
	return ""
}

// ControlArgs is controlArgs: ssh's connection sharing for a host.
func (p *Pool) ControlArgs(h SSHHost) []string {
	if p.o.Getenv("STUDIO_SSH_SHARE") == "0" {
		return nil
	}
	dir := SocketDir(p.SocketDirs(), os.Getuid())
	if dir == "" {
		return nil
	}
	port := ""
	if h.Port != 0 {
		port = strconv.Itoa(h.Port)
	}
	id := h.Destination + ":" + port
	if len(h.Options) > 0 {
		id += ":" + strings.Join(h.Options, "\x00")
	}
	tag := OptionsTag(h.Options)
	percent := "%C"
	extra := 0
	if tag != "" {
		percent = "%C-" + tag
		extra = 1 + optionsTagLen
	}
	name := percent
	if len(dir)+1+percentC+extra > socketPathMax {
		sum := sha1.Sum([]byte(id))
		name = hex.EncodeToString(sum[:])[:socketName]
	}
	sock := filepath.Join(dir, name)
	persist := "no"
	if n := p.PersistSeconds(); n > 0 {
		persist = strconv.Itoa(n)
	}
	return []string{"-o", "ControlMaster=auto", "-o", `ControlPath="` + sock + `"`, "-o", "ControlPersist=" + persist}
}

func (p *Pool) sshBase(site string, extra ...string) ([]string, error) {
	h, err := ParseSSHSite(site)
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, apierr.New(400, "%s is not an ssh site", site)
	}
	out := append([]string{"-T"}, h.Options...)
	out = append(out, "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=30", "-o", "Compression=yes")
	out = append(out, p.ControlArgs(*h)...)
	out = append(out, extra...)
	if h.Port != 0 {
		out = append(out, "-p", strconv.Itoa(h.Port))
	}
	return append(out, h.Destination), nil
}

// SSHArgv is sshArgv: ssh's arguments for a command on a host.
func (p *Pool) SSHArgv(site, command string) ([]string, error) {
	b, err := p.sshBase(site)
	if err != nil {
		return nil, err
	}
	return append(b, command), nil
}

// ---- spawning ----

func (p *Pool) spawn(site, key, command string) (*exec.Cmd, error) {
	if p.o.Spawn != nil {
		return p.o.Spawn(site, key, command), nil
	}
	if prog := p.o.Getenv("STUDIO_REMOTE_SPAWN"); prog != "" {
		cmd := exec.Command(prog, site, command)
		cmd.Env = append(os.Environ(), "PW_API_KEY="+key)
		return cmd, nil
	}
	if IsSSH(site) {
		argv, err := p.SSHArgv(site, command)
		if err != nil {
			return nil, err
		}
		return exec.Command("ssh", argv...), nil
	}
	args := []string{"ssh"}
	if u, err := url.Parse(p.o.GatewayBase()); err == nil && u.Hostname() != "" {
		args = append(args, "--platform-host", u.Hostname())
	}
	cmd := exec.Command(p.o.PwCLI(), append(args, site, command)...)
	// Without a key of its own the CLI uses its own sign-in.
	if key != "" {
		cmd.Env = append(os.Environ(), "PW_API_KEY="+key)
	}
	return cmd, nil
}

// ---- hosts that run only gufi_query ----

// SetDirect marks or clears a site whose login runs nothing but gufi_query.
func (p *Pool) SetDirect(site string, on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if on {
		p.direct[site] = true
	} else {
		delete(p.direct, site)
	}
}

// IsDirect reports whether a site runs only gufi_query.
func (p *Pool) IsDirect(site string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.direct[site]
}

var queryTool = regexp.MustCompile(`(?:^|/)gufi_(?:query|sqlite3)$`)

// NoShell is noShell: why a command other than gufi_query cannot run there.
func NoShell(site string) string {
	return SiteName(site) + " runs only gufi_query for this login (no shell), so this cannot run there."
}

type tailBuf struct {
	b     []byte
	total int
}

func (t *tailBuf) Write(d []byte) (int, error) {
	t.total += len(d)
	t.b = append(t.b, d...)
	if len(t.b) > stderrTail {
		t.b = append([]byte(nil), t.b[len(t.b)-stderrTail:]...)
	}
	return len(d), nil
}

func secs(d time.Duration) int { return int(math.Round(d.Seconds())) }

// RunOnce is remoteRunOnce: argv as one ssh (or pw ssh) call with no
// session loop. input, when not nil, is the command's standard input. An
// error is ssh itself failing (exit 255 over plain ssh); a command that ran
// and exited nonzero is a Result with its code.
func (p *Pool) RunOnce(ctx context.Context, site, key string, argv []string, timeout time.Duration, input io.Reader) (Result, error) {
	if len(argv) == 0 {
		return Result{}, apierr.New(400, "no command")
	}
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	cmd, err := p.spawn(site, key, DirectCommand(argv))
	if err != nil {
		return Result{}, apierr.New(502, "%s: %s", SiteName(site), err.Error())
	}
	var out bytes.Buffer
	var errb tailBuf
	cmd.Stdout, cmd.Stderr = &out, &errb
	if input != nil {
		cmd.Stdin = input
	}
	if err := cmd.Start(); err != nil {
		return Result{}, apierr.New(502, "%s: %s", SiteName(site), err.Error())
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		cmd.Process.Kill()
		<-done
		return Result{}, apierr.New(504, "%s: the command did not finish in %d s", SiteName(site), secs(timeout))
	case <-ctx.Done():
		cmd.Process.Kill()
		<-done
		return Result{}, ctx.Err()
	}
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	stderr := QuietStderr(string(errb.b))
	if code == 255 && IsSSH(site) {
		last := lastLine(stderr)
		if last == "" {
			last = "the ssh connection failed"
		}
		return Result{}, apierr.New(502, "%s: %s", SiteName(site), last)
	}
	raw := out.Bytes()
	return Result{Code: code, Stdout: string(raw), Raw: raw, Stderr: stderr, StderrCut: errb.total > len(errb.b)}, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

var refusedRe = regexp.MustCompile(`(?i)bash[:,]? .*(?:not found|No such file)|command not found|restricted|not allowed|not permitted|only gufi_query`)

func refusedLoop(site string, code int, stderr string) bool {
	if IsSSH(site) {
		return code != -1 && code != 255
	}
	return refusedRe.MatchString(stderr)
}

// ---- sessions ----

type pending struct {
	argv    []string
	timeout time.Duration
	ch      chan pendingResult
	timer   *time.Timer
}

type pendingResult struct {
	r   Result
	err error
}

// deliver hands a request its answer once: a timeout and a late answer
// may both try.
func deliver(pd *pending, r pendingResult) {
	select {
	case pd.ch <- r:
	default:
	}
}

type channel struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	// writeMu keeps the queue's order and the order requests are written
	// in the same: answers are matched to the queue first in, first out.
	writeMu sync.Mutex

	mu      sync.Mutex
	queue   []*pending
	last    time.Time
	stderr  string
	dead    bool
	started bool
	// killed: a command outlived its timeout and the session is being
	// ended; no new request goes to it.
	killed bool
}

func (c *channel) kill() {
	if c.cmd.Process != nil {
		c.cmd.Process.Kill()
	}
}

func (p *Pool) dropLocked(id string, ch *channel) {
	var keep []*channel
	for _, c := range p.channels[id] {
		if c != ch {
			keep = append(keep, c)
		}
	}
	if len(keep) > 0 {
		p.channels[id] = keep
	} else {
		delete(p.channels, id)
	}
}

var answerRe = regexp.MustCompile(`^(\d+) o(\S*) e(\S*)$`)

func (p *Pool) open(id, site, key string) (*channel, error) {
	cmd, err := p.spawn(site, key, BootstrapCommand(site))
	if err != nil {
		return nil, err
	}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return nil, apierr.New(502, "%s: %s", SiteName(site), err.Error())
	}
	ch := &channel{cmd: cmd, stdin: stdin, last: time.Now()}
	errDone := make(chan struct{})
	go func() {
		defer close(errDone)
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				ch.mu.Lock()
				ch.stderr += string(buf[:n])
				if len(ch.stderr) > 2000 {
					ch.stderr = ch.stderr[len(ch.stderr)-2000:]
				}
				ch.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		r := bufio.NewReaderSize(stdout, 64<<10)
		for {
			line, err := r.ReadString('\n')
			if line != "" && strings.HasSuffix(line, "\n") {
				p.answer(ch, strings.TrimSpace(line))
			}
			if err != nil {
				break
			}
		}
		<-errDone
		werr := cmd.Wait()
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		_ = werr
		p.fail(id, site, key, ch, fmt.Sprintf("the ssh session ended (%d)", code), code)
	}()
	return ch, nil
}

func (p *Pool) answer(ch *channel, line string) {
	m := answerRe.FindStringSubmatch(line)
	if m == nil {
		return // a login banner or motd before the loop starts
	}
	ch.mu.Lock()
	ch.started = true
	if len(ch.queue) == 0 {
		ch.mu.Unlock()
		return
	}
	pd := ch.queue[0]
	ch.queue = ch.queue[1:]
	ch.mu.Unlock()
	pd.timer.Stop()
	raw, _ := base64.StdEncoding.DecodeString(m[2])
	e, _ := base64.StdEncoding.DecodeString(m[3])
	code, _ := strconv.Atoi(m[1])
	deliver(pd, pendingResult{r: Result{Code: code, Stdout: string(raw), Raw: raw, Stderr: string(e), StderrCut: len(e) >= stderrTail}})
}

func (p *Pool) fail(id, site, key string, ch *channel, why string, code int) {
	ch.mu.Lock()
	if ch.dead {
		ch.mu.Unlock()
		return
	}
	ch.dead = true
	waiting := ch.queue
	ch.queue = nil
	started := ch.started
	stderr := ch.stderr
	ch.mu.Unlock()
	p.mu.Lock()
	p.dropLocked(id, ch)
	p.mu.Unlock()
	said := ""
	for _, l := range strings.Split(strings.TrimSpace(QuietStderr(stderr)), "\n") {
		if l != "" {
			said = l
		}
	}
	if !started && refusedLoop(site, code, stderr) {
		p.SetDirect(site, true)
		for _, pd := range waiting {
			pd.timer.Stop()
			if queryTool.MatchString(pd.argv[0]) {
				go func(pd *pending) {
					r, err := p.RunOnce(context.Background(), site, key, pd.argv, pd.timeout, nil)
					deliver(pd, pendingResult{r, err})
				}(pd)
			} else {
				deliver(pd, pendingResult{err: apierr.New(409, "%s", NoShell(site))})
			}
		}
		return
	}
	if said == "" {
		said = why
	}
	for _, pd := range waiting {
		pd.timer.Stop()
		deliver(pd, pendingResult{err: apierr.New(502, "%s: %s", SiteName(site), said)})
	}
}

func channelID(site, key string) string {
	k := key
	if len(k) > 16 {
		k = k[len(k)-16:]
	}
	return k + "|" + site
}

// pick is a session for the next request: an idle one, else a new one
// while the host has fewer than the pool's most, else the shortest queue.
func (p *Pool) pick(id, site, key string) (*channel, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var live []*channel
	for _, c := range p.channels[id] {
		c.mu.Lock()
		dead := c.dead || c.killed
		c.mu.Unlock()
		if !dead {
			live = append(live, c)
		}
	}
	for _, c := range live {
		c.mu.Lock()
		n := len(c.queue)
		c.mu.Unlock()
		if n == 0 {
			return c, nil
		}
	}
	if len(live) < p.poolMax {
		ch, err := p.open(id, site, key)
		if err != nil {
			return nil, err
		}
		p.channels[id] = append(live, ch)
		return ch, nil
	}
	best, bestN := live[0], -1
	for _, c := range live {
		c.mu.Lock()
		n := len(c.queue)
		c.mu.Unlock()
		if bestN < 0 || n < bestN {
			best, bestN = c, n
		}
	}
	return best, nil
}

// Run is remoteRun: argv on the site as the key's user (or the ssh login).
// An error is the session failing, not the command exiting nonzero. On a
// host that runs only gufi_query, a query tool runs as its own call and
// anything else is refused.
func (p *Pool) Run(ctx context.Context, site, key string, argv []string, timeout time.Duration) (Result, error) {
	if len(argv) == 0 {
		return Result{}, apierr.New(400, "no command")
	}
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	if p.IsDirect(site) {
		if queryTool.MatchString(argv[0]) {
			return p.RunOnce(ctx, site, key, argv, timeout, nil)
		}
		return Result{}, apierr.New(409, "%s", NoShell(site))
	}
	id := channelID(site, key)
	c, err := p.pick(id, site, key)
	if err != nil {
		return Result{}, err
	}
	pd := &pending{argv: argv, timeout: timeout, ch: make(chan pendingResult, 1)}
	enc := make([]string, len(argv))
	for i, a := range argv {
		enc[i] = "x" + base64.StdEncoding.EncodeToString([]byte(a))
	}
	c.writeMu.Lock()
	c.mu.Lock()
	c.last = time.Now()
	if c.dead || c.killed {
		c.mu.Unlock()
		c.writeMu.Unlock()
		return p.Run(ctx, site, key, argv, timeout)
	}
	pd.timer = time.AfterFunc(timeout, func() {
		// A command that hangs leaves the loop busy: drop the session, and
		// every request behind it with it.
		c.mu.Lock()
		for i, x := range c.queue {
			if x == pd {
				c.queue = append(c.queue[:i:i], c.queue[i+1:]...)
				break
			}
		}
		c.killed = true
		c.mu.Unlock()
		deliver(pd, pendingResult{err: apierr.New(504, "%s: the command did not finish in %d s", SiteName(site), secs(timeout))})
		c.kill()
	})
	c.queue = append(c.queue, pd)
	c.mu.Unlock()
	c.stdin.Write([]byte(strings.Join(enc, " ") + "\n"))
	c.writeMu.Unlock()
	select {
	case r := <-pd.ch:
		return r.r, r.err
	case <-ctx.Done():
		// The answer still comes and is dropped; the session stays.
		return Result{}, ctx.Err()
	}
}

// ---- the host's processors ----

// CPUs is remoteCpus: nproc, else getconf, else sysctl; 4 when none can be
// read (asked again next time) and on a host that runs only gufi_query.
func (p *Pool) CPUs(ctx context.Context, site, key string) int {
	if p.IsDirect(site) {
		return 4
	}
	id := channelID(site, key)
	p.mu.Lock()
	e := p.cpus[id]
	if e == nil {
		e = &cpuEntry{done: make(chan struct{})}
		p.cpus[id] = e
		p.mu.Unlock()
		r, err := p.Run(context.Background(), site, key, []string{"sh", "-c", "nproc 2>/dev/null || getconf _NPROCESSORS_ONLN 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null"}, 30*time.Second)
		n := 0
		if err == nil {
			n = leadingInt(strings.Split(strings.TrimSpace(r.Stdout), "\n")[0])
		}
		if n <= 0 {
			p.mu.Lock()
			if p.cpus[id] == e {
				delete(p.cpus, id)
			}
			p.mu.Unlock()
			n = 4
		}
		e.n = n
		close(e.done)
		return n
	}
	p.mu.Unlock()
	select {
	case <-e.done:
		return e.n
	case <-ctx.Done():
		return 4
	}
}

// leadingInt is parseInt: the digits a string starts with, 0 for none.
func leadingInt(s string) int {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n, _ := strconv.Atoi(s[:i])
	return n
}

// Threads is remoteThreads: STUDIO_REMOTE_THREADS when set, else the
// host's processors up to 16.
func (p *Pool) Threads(ctx context.Context, site, key string) int {
	if v, err := strconv.ParseFloat(p.o.Getenv("STUDIO_REMOTE_THREADS"), 64); err == nil && v == math.Trunc(v) && v >= 1 {
		return int(min(v, 256))
	}
	return min(p.CPUs(ctx, site, key), 16)
}

// ---- ssh on this computer ----

func runSSHLocal(args []string, timeout time.Duration) LocalResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() >= 0 {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	return LocalResult{Code: code, Stdout: out.String(), Stderr: errb.String()}
}

func hostArgs(h *SSHHost) []string {
	out := append([]string{"-G"}, h.Options...)
	if h.Port != 0 {
		out = append(out, "-p", strconv.Itoa(h.Port))
	}
	return append(out, h.Destination)
}

// Route is sshRoute: the jump hosts and proxy command ssh would use.
func (p *Pool) Route(site string) (jump string, proxyCommand bool) {
	h, err := ParseSSHSite(site)
	if err != nil || h == nil {
		return "", false
	}
	r := p.o.SSHLocal(hostArgs(h), 5*time.Second)
	line := func(k string) string {
		for _, l := range strings.Split(r.Stdout, "\n") {
			if strings.HasPrefix(strings.ToLower(l), k+" ") {
				return strings.TrimSpace(l[len(k)+1:])
			}
		}
		return ""
	}
	j, proxy := line("proxyjump"), line("proxycommand")
	if j == "" || j == "none" {
		j = JumpOf(h.Options)
	}
	return j, proxy != "" && proxy != "none"
}

// Reached is where a site's connection lands once ssh has read its options
// and the user's ~/.ssh/config.
type Reached struct {
	Label string
	Key   string
}

type reachedEntry struct {
	at time.Time
	v  *Reached
}

const reachedTTL = 10 * time.Minute

// ReachedHost is reachedHost: from `ssh -G`, kept ten minutes; nil when
// ssh cannot say.
func (p *Pool) ReachedHost(site string) *Reached {
	h, err := ParseSSHSite(site)
	if err != nil || h == nil {
		return nil
	}
	p.mu.Lock()
	seen, ok := p.reached[site]
	p.mu.Unlock()
	if ok && time.Since(seen.at) < reachedTTL {
		return seen.v
	}
	r := p.o.SSHLocal(hostArgs(h), 5*time.Second)
	lines := map[string]string{}
	if r.Code == 0 {
		for _, l := range strings.Split(r.Stdout, "\n") {
			i := strings.Index(l, " ")
			if i > 0 {
				k := strings.ToLower(l[:i])
				if _, ok := lines[k]; !ok {
					lines[k] = strings.TrimSpace(l[i+1:])
				}
			}
		}
	}
	none := func(v string) string {
		if v == "none" {
			return ""
		}
		return v
	}
	user, host, port := lines["user"], lines["hostname"], lines["port"]
	if port == "" {
		port = "22"
	}
	var v *Reached
	if user != "" && host != "" {
		v = &Reached{Label: user + "@" + host, Key: user + "@" + host + ":" + port + "|" + none(lines["proxyjump"]) + "|" + none(lines["proxycommand"]) + "|" + OptionsTag(h.Options)}
	}
	p.mu.Lock()
	p.reached[site] = reachedEntry{at: time.Now(), v: v}
	p.mu.Unlock()
	return v
}

// ReachedKnown is reachedKnown: the last answer, without asking ssh.
func (p *Pool) ReachedKnown(site string) *Reached {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reached[site].v
}

type warmEntry struct {
	at   time.Time
	done chan struct{}
	v    string
}

// Warm is warmSsh: opens a site's shared connection ahead of a search,
// at most once a minute per site. "open", "opened", "failed" or "off".
// It returns at once; wait reports the outcome.
func (p *Pool) Warm(site string) (wait func() string) {
	h, err := ParseSSHSite(site)
	if err != nil || h == nil || len(p.ControlArgs(*h)) == 0 || p.PersistSeconds() == 0 {
		return func() string { return "off" }
	}
	p.mu.Lock()
	if e, ok := p.warming[site]; ok && time.Since(e.at) < time.Minute {
		p.mu.Unlock()
		return func() string { <-e.done; return e.v }
	}
	e := &warmEntry{at: time.Now(), done: make(chan struct{})}
	p.warming[site] = e
	p.mu.Unlock()
	go func() {
		defer close(e.done)
		check, _ := p.sshBase(site, "-O", "check")
		if p.o.SSHLocal(check, 5*time.Second).Code == 0 {
			e.v = "open"
			return
		}
		up, _ := p.sshBase(site, "-N", "-f")
		if p.o.SSHLocal(up, 30*time.Second).Code == 0 {
			e.v = "opened"
		} else {
			e.v = "failed"
		}
	}()
	return func() string { <-e.done; return e.v }
}
