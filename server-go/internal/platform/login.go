package platform

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/netx"
)

/* ---- the CLI itself ---- */

// CLIInfo is cliInfo's answer.
type CLIInfo struct {
	Available bool
	Version   string
	Path      string
	Source    string
}

// JSON is the object the routes send: {available, version, path, source}.
func (c CLIInfo) JSON() *jsonv.Object {
	return jsonv.NewObject("available", c.Available, "version", nullable(c.Version), "path", nullable(c.Path), "source", c.Source)
}

type cliInfoCache struct {
	at time.Time
	v  CLIInfo
}

var versionRe = regexp.MustCompile(`v?\d+\.\d+\.\d+`)

// Info is cliInfo: whether the pw CLI runs here, where, and its version;
// fresh looks again at once rather than within five minutes.
func (p *Platform) Info(fresh bool) CLIInfo {
	p.mu.Lock()
	c := p.cliInfo
	p.mu.Unlock()
	if !fresh && c != nil && time.Since(c.at) < 5*time.Minute {
		return c.v
	}
	path, source := p.FindCLI()
	out, err := p.Exec(context.Background(), []string{"--version"}, ExecOptions{Timeout: 8 * time.Second, Env: tokenEnv})
	missing := IsMissing(err)
	version := versionRe.FindString(out)
	v := CLIInfo{Available: !missing && (err == nil || version != ""), Version: version, Path: path, Source: source}
	if missing {
		v.Path, v.Source = "", SourceNone
	}
	p.mu.Lock()
	p.cliInfo = &cliInfoCache{at: time.Now(), v: v}
	p.mu.Unlock()
	return v
}

/* ---- signing in with the CLI ---- */

// LoginState is access.ts's LoginState.
type LoginState struct {
	Host, State, URL, Code, Context, Message, StartedAt string
}

// JSON is the state as the routes send it.
func (s *LoginState) JSON() any {
	if s == nil {
		return nil
	}
	return jsonv.NewObject("host", s.Host, "state", s.State, "url", nullable(s.URL), "code", nullable(s.Code),
		"context", nullable(s.Context), "message", nullable(s.Message), "startedAt", s.StartedAt)
}

type loginSlot struct {
	mu  sync.Mutex
	cur *loginRun
}

type loginRun struct {
	st    LoginState
	cmd   *exec.Cmd
	timer *time.Timer
	done  bool
}

var hostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:\d{1,5})?$`)
var schemeRe = regexp.MustCompile(`(?i)^https?://`)

// NormalizeHost is normalizeHost: a platform host as typed, or "".
func NormalizeHost(in string) string {
	h := schemeRe.ReplaceAllString(strings.TrimSpace(in), "")
	if i := strings.Index(h, "/"); i >= 0 {
		h = h[:i]
	}
	h = strings.ToLower(h)
	if !hostRe.MatchString(h) {
		return ""
	}
	return h
}

var (
	promptURL  = regexp.MustCompile(`(https?://[^\s"'<>]+)`)
	promptCode = regexp.MustCompile(`(?m)^\s*([A-Z0-9]{3,}(?:-[A-Z0-9]{3,})+)\s*$`)
)

// ParseDevicePrompt is parseDevicePrompt: the address and code the CLI prints.
func ParseDevicePrompt(text string) (url, code string) {
	clean := StripANSI(text)
	if m := promptURL.FindStringSubmatch(clean); m != nil {
		url = m[1]
	}
	if m := promptCode.FindStringSubmatch(clean); m != nil {
		code = m[1]
	}
	return
}

// Login is loginState().
func (p *Platform) Login() *LoginState {
	p.login.mu.Lock()
	defer p.login.mu.Unlock()
	if p.login.cur == nil {
		return nil
	}
	st := p.login.cur.st
	return &st
}

// CancelLogin is cancelCliLogin.
func (p *Platform) CancelLogin() *LoginState {
	p.login.mu.Lock()
	defer p.login.mu.Unlock()
	return p.cancelLocked()
}

func (p *Platform) cancelLocked() *LoginState {
	l := p.login.cur
	if l == nil {
		return nil
	}
	if l.timer != nil {
		l.timer.Stop()
	}
	if l.cmd != nil && !l.done && l.cmd.Process != nil {
		l.cmd.Process.Signal(syscall.SIGTERM)
	}
	if l.st.State == "starting" || l.st.State == "waiting" {
		l.st.State, l.st.Message = "canceled", "Sign-in canceled."
	}
	l.cmd = nil
	st := l.st
	return &st
}

// syncBuf collects the CLI's output from both pipes.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
	on func(string)
}

func (s *syncBuf) Write(b []byte) (int, error) {
	s.mu.Lock()
	s.b.Write(b)
	text := s.b.String()
	s.mu.Unlock()
	if s.on != nil {
		s.on(text)
	}
	return len(b), nil
}

func (s *syncBuf) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// StartLogin is startCliLogin: `pw auth login --device <host>`, answered
// once the CLI prints its address and code (or after wait), and followed
// to its end in the background. expire ends a code nobody approved.
func (p *Platform) StartLogin(hostInput string, wait, expire time.Duration) (*LoginState, error) {
	host := NormalizeHost(hostInput)
	if host == "" {
		return nil, errors.New("Enter a platform host, such as platform.example.com.")
	}
	if wait <= 0 {
		wait = 20 * time.Second
	}
	if expire <= 0 {
		expire = 11 * time.Minute
	}
	p.login.mu.Lock()
	p.cancelLocked()
	run := &loginRun{st: LoginState{Host: host, State: "starting", StartedAt: NowISO()}}
	p.login.cur = run
	cmd := exec.Command(p.CLI(), "auth", "login", "--device", host)
	cmd.Env = p.Environ(tokenEnv)
	answered := make(chan struct{})
	var once sync.Once
	answer := func() { once.Do(func() { close(answered) }) }
	out := &syncBuf{}
	out.on = func(text string) {
		p.login.mu.Lock()
		defer p.login.mu.Unlock()
		if run.st.State != "starting" {
			return
		}
		if u, c := ParseDevicePrompt(text); u != "" && c != "" {
			run.st.State, run.st.URL, run.st.Code = "waiting", u, c
			answer()
		}
	}
	cmd.Stdout, cmd.Stderr = out, out
	run.cmd = cmd
	if err := cmd.Start(); err != nil {
		msg := err.Error()
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, syscall.ENOENT) {
			msg = "The pw CLI is not installed here. Install it, or add a key or endpoint instead."
		}
		run.st.State, run.st.Message = "failed", msg
		run.cmd = nil
		st := run.st
		p.login.mu.Unlock()
		return &st, nil
	}
	run.timer = time.AfterFunc(expire, func() {
		p.login.mu.Lock()
		defer p.login.mu.Unlock()
		if run.cmd != nil && !run.done && run.cmd.Process != nil {
			run.cmd.Process.Signal(syscall.SIGTERM)
		}
		if run.st.State == "waiting" || run.st.State == "starting" {
			run.st.State, run.st.Message = "failed", "The code expired before it was approved. Start again."
		}
	})
	p.login.mu.Unlock()
	go func() {
		err := cmd.Wait()
		code := 0
		if err != nil {
			code = -1
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			}
		}
		p.login.mu.Lock()
		run.done = true
		run.cmd = nil
		if run.timer != nil {
			run.timer.Stop()
		}
		canceled := run.st.State == "canceled"
		p.login.mu.Unlock()
		if !canceled {
			p.finishLogin(run, host, code, out.String())
		}
		answer()
	}()
	select {
	case <-answered:
	case <-time.After(wait):
	}
	p.login.mu.Lock()
	st := run.st
	p.login.mu.Unlock()
	return &st, nil
}

func (p *Platform) finishLogin(run *loginRun, host string, code int, out string) {
	if code == 0 {
		s := p.ReadStore()
		made := ""
		if s != nil {
			if i, ok := s.identity(s.CurrentIdentity); ok && s.CurrentIdentity != "" && i.Server == host {
				made = s.CurrentIdentity
			} else {
				for _, n := range s.Names {
					if s.Identities[n].Server == host {
						made = n
						break
					}
				}
			}
		}
		// The CLI made the sign-in its current context. A Studio pinned to a
		// context (or started with PW_CONTEXT) is moved to it.
		if made != "" && (p.ChosenContext() != "" || p.startupContext != "") {
			p.ChooseContext(made)
		} else {
			p.syncGateway()
			// Node follows the CLI's new current context too (its gateway).
			if err := p.AccessChanged(false); err != nil {
				p.log.Warn("pw CLI sign-in: Node did not answer", "err", err.Error())
			}
		}
		msg := "Signed in."
		if made != "" {
			msg = "Signed in. The Studio now uses the pw CLI context " + made + "."
		}
		p.login.mu.Lock()
		run.st.State, run.st.Context, run.st.Message = "done", made, msg
		p.login.mu.Unlock()
		return
	}
	clean := strings.TrimSpace(StripANSI(out))
	said := ""
	for _, l := range strings.Split(clean, "\n") {
		t := strings.TrimSpace(l)
		if t != "" && !strings.HasPrefix(strings.ToLower(t), "waiting") {
			said = l
		}
	}
	msg := sliceRunes(said, 300)
	if netx.CLINetworkFailure(out) {
		msg = netx.UnreachableMessage(host, nil)
	} else if msg == "" {
		msg = "The sign-in did not finish (the CLI exited with " + exitCodeJS(code) + ")."
	}
	p.login.mu.Lock()
	run.st.State, run.st.Message = "failed", msg
	p.login.mu.Unlock()
}

// exitCodeJS is how a close code prints: a killed process's is null.
func exitCodeJS(code int) string {
	if code < 0 {
		return "null"
	}
	return strconv.Itoa(code)
}
