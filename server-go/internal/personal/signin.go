// Package personal gives each person signed in to a shared Studio a
// platform sign-in of their own, so the work in their libraries runs as
// their own account on the cluster: a pw CLI device sign-in kept in a
// folder of theirs under INDEX_BASE (users/<hash>/pw, mode 700), and the
// hour-long access tokens the CLI renews from it. The sign-in is checked
// against the person signed in to the Studio before it is kept. Go only;
// see docs/MULTI-USER.md, Personal spaces and folder sharing.
package personal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// SignIns keeps every person's pw CLI sign-in.
type SignIns struct {
	// Base is INDEX_BASE.
	Base string
	// Host is the platform host people sign in to; "" when unknown.
	Host func() string
	// CLI is the pw CLI's path.
	CLI func() string
	// Environ is the environment for the CLI with set laid over it ("" removes).
	Environ func(set map[string]string) []string
	Log     *slog.Logger
	Now     func() time.Time
	// Expire ends a code nobody approved.
	Expire time.Duration

	mu       sync.Mutex
	runs     map[string]*run
	tokens   map[string]token
	renewing map[string]*renewCall
}

// State is one person's sign-in as the Settings page shows it.
type State struct {
	// State is none, starting, waiting, signed-in or failed.
	State     string `json:"state"`
	Host      string `json:"host,omitempty"`
	URL       string `json:"url,omitempty"`
	Code      string `json:"code,omitempty"`
	Account   string `json:"account,omitempty"`
	SignedIn  string `json:"signedInAt,omitempty"`
	Message   string `json:"message,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
}

type run struct {
	st    State
	cmd   *exec.Cmd
	timer *time.Timer
	done  bool
}

type token struct {
	value string
	until time.Time
	asked time.Time
}

type renewCall struct {
	done  chan struct{}
	value string
	err   error
}

// record is what the Studio keeps beside the CLI's own file.
type record struct {
	Account  string `json:"account"`
	Host     string `json:"host"`
	SignedIn string `json:"signedInAt"`
}

const recordFile = "studio-sign-in.json"

// New is the sign-ins of the Studio at indexBase on p.
func New(p *platform.Platform, indexBase string, log *slog.Logger) *SignIns {
	return &SignIns{
		Base: indexBase,
		Host: func() string {
			if h := platform.NormalizeHost(os.Getenv("PW_PLATFORM_HOST")); h != "" {
				return h
			}
			return platform.NormalizeHost(p.GatewayBase())
		},
		CLI:     p.CLI,
		Environ: p.Environ,
		Log:     log,
	}
}

func (s *SignIns) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Dir is the folder that holds owner's sign-in.
func (s *SignIns) Dir(owner string) string {
	return filepath.Join(s.Base, "users", libraries.OwnerHash(owner), "pw")
}

// env is the CLI's environment for the sign-in in dir: no deployment key
// or context, no desktop keyring (the sign-in must stay in dir's file),
// and no host other than the one named on the command line.
func (s *SignIns) env(dir string) []string {
	return s.Environ(map[string]string{
		"PW_CREDENTIALS_DIR":       dir,
		"PW_API_KEY":               "",
		"PW_CONTEXT":               "",
		"PW_PLATFORM_HOST":         "",
		"DBUS_SESSION_BUS_ADDRESS": "",
		"DISPLAY":                  "",
		"WAYLAND_DISPLAY":          "",
		"BROWSER":                  "",
	})
}

func (s *SignIns) exec(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.CLI(), args...)
	cmd.Env = s.env(dir)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(platform.StripANSI(errb.String()))
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), errors.New(firstLine(msg))
	}
	return out.String(), nil
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.Contains(l, "new version of the PW CLI") && !strings.HasPrefix(l, "Run 'pw upgrade'") {
			return l
		}
	}
	return strings.TrimSpace(s)
}

func (s *SignIns) has(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "credentials"))
	return err == nil
}

func readRecord(dir string) *record {
	b, err := os.ReadFile(filepath.Join(dir, recordFile))
	if err != nil {
		return nil
	}
	var r record
	if json.Unmarshal(b, &r) != nil || r.Account == "" {
		return nil
	}
	return &r
}

// Status is owner's sign-in: a code waiting for approval, a finished
// sign-in, or none.
func (s *SignIns) Status(owner string) State {
	s.mu.Lock()
	r := s.runs[owner]
	var st State
	if r != nil {
		st = r.st
	}
	s.mu.Unlock()
	if r != nil && st.State != "signed-in" {
		return st
	}
	dir := s.Dir(owner)
	if rec := readRecord(dir); rec != nil && s.has(dir) {
		return State{State: "signed-in", Host: rec.Host, Account: rec.Account, SignedIn: rec.SignedIn}
	}
	if r != nil {
		return st
	}
	return State{State: "none", Host: s.Host()}
}

// Start begins a device sign-in for owner, who is username on the
// platform. It answers once the CLI prints its address and code, or after
// wait. A sign-in that turns out to be for another account is signed out
// and dropped.
func (s *SignIns) Start(owner, username string, wait time.Duration) (State, error) {
	host := s.Host()
	if host == "" {
		return State{}, errors.New("This Studio does not know its platform host, so it cannot sign you in. Ask the operator to set PW_PLATFORM_HOST.")
	}
	if owner == "" || username == "" {
		return State{}, errors.New("Sign in to the Studio through the platform first.")
	}
	if wait <= 0 {
		wait = 20 * time.Second
	}
	expire := s.Expire
	if expire <= 0 {
		expire = 11 * time.Minute
	}
	dir := s.Dir(owner)
	s.mu.Lock()
	if s.runs == nil {
		s.runs = map[string]*run{}
	}
	if old := s.runs[owner]; old != nil && !old.done {
		old.st.State = "canceled"
		s.stopLocked(old)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		s.mu.Unlock()
		return State{}, err
	}
	os.Chmod(filepath.Dir(dir), 0o700)
	// Each attempt stages in its own folder, so an attempt being stopped
	// cannot remove the next one's.
	staging, err := os.MkdirTemp(filepath.Dir(dir), "pw.new-")
	if err != nil {
		s.mu.Unlock()
		return State{}, err
	}
	r := &run{st: State{State: "starting", Host: host, StartedAt: s.now().UTC().Format(time.RFC3339)}}
	s.runs[owner] = r
	cmd := exec.Command(s.CLI(), "auth", "login", "--device", host)
	cmd.Env = s.env(staging)
	answered := make(chan struct{})
	var once sync.Once
	answer := func() { once.Do(func() { close(answered) }) }
	out := &promptBuf{on: func(text string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.st.State != "starting" {
			return
		}
		if u, c := platform.ParseDevicePrompt(text); u != "" && c != "" {
			r.st.State, r.st.URL, r.st.Code = "waiting", u, c
			answer()
		}
	}}
	cmd.Stdout, cmd.Stderr = out, out
	r.cmd = cmd
	if err := cmd.Start(); err != nil {
		r.st.State, r.st.Message = "failed", "The pw CLI could not start here: "+err.Error()
		r.cmd, r.done = nil, true
		st := r.st
		s.mu.Unlock()
		os.RemoveAll(staging)
		return st, nil
	}
	r.timer = time.AfterFunc(expire, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.st.State == "waiting" || r.st.State == "starting" {
			r.st.State, r.st.Message = "failed", "The code expired before it was approved. Start again."
			s.stopLocked(r)
		}
	})
	s.mu.Unlock()
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		r.done, r.cmd = true, nil
		if r.timer != nil {
			r.timer.Stop()
		}
		ended := r.st.State == "failed" || r.st.State == "canceled"
		s.mu.Unlock()
		if ended {
			os.RemoveAll(staging)
			answer()
			return
		}
		st := s.finish(owner, username, host, staging, err, out.String())
		s.mu.Lock()
		r.st = st
		s.mu.Unlock()
		answer()
	}()
	select {
	case <-answered:
	case <-time.After(wait):
	}
	s.mu.Lock()
	st := r.st
	s.mu.Unlock()
	return st, nil
}

// finish keeps a sign-in that belongs to username and drops any other.
func (s *SignIns) finish(owner, username, host, staging string, waitErr error, out string) State {
	fail := func(msg string) State {
		os.RemoveAll(staging)
		return State{State: "failed", Host: host, Message: msg}
	}
	if waitErr != nil || !s.has(staging) {
		msg := firstLine(platform.StripANSI(out))
		if msg == "" || strings.HasPrefix(msg, "To sign in") {
			msg = "The sign-in did not finish."
		}
		return fail(msg)
	}
	who, err := s.exec(context.Background(), staging, 30*time.Second, "auth", "whoami")
	who = strings.TrimSpace(firstLine(who))
	if err != nil || who == "" {
		s.signOut(staging)
		return fail("The platform did not say whose sign-in this is, so it was not kept. Try again.")
	}
	if !strings.EqualFold(who, username) {
		s.signOut(staging)
		return fail("That code was approved as " + who + ", and you are signed in to the Studio as " + username + ". It was signed out and not kept. Approve the code while signed in to the platform as yourself.")
	}
	rec := record{Account: who, Host: host, SignedIn: s.now().UTC().Format(time.RFC3339)}
	b, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(staging, recordFile), b, 0o600); err != nil {
		s.signOut(staging)
		return fail("The sign-in could not be saved: " + err.Error())
	}
	dir := s.Dir(owner)
	old := dir + ".old"
	os.RemoveAll(old)
	hadOld := s.has(dir)
	if hadOld {
		if err := os.Rename(dir, old); err != nil {
			s.signOut(staging)
			return fail("The sign-in could not be saved: " + err.Error())
		}
	} else {
		os.RemoveAll(dir)
	}
	if err := os.Rename(staging, dir); err != nil {
		if hadOld {
			os.Rename(old, dir)
		}
		s.signOut(staging)
		return fail("The sign-in could not be saved: " + err.Error())
	}
	s.mu.Lock()
	delete(s.tokens, owner)
	s.mu.Unlock()
	if hadOld {
		go func() { s.signOut(old) }()
	}
	if s.Log != nil {
		s.Log.Info("personal sign-in kept", "owner", libraries.OwnerHash(owner), "host", host)
	}
	return State{State: "signed-in", Host: host, Account: who, SignedIn: rec.SignedIn}
}

// signOut ends the sign-in in dir on the platform, best effort, and removes it.
func (s *SignIns) signOut(dir string) {
	if s.has(dir) {
		if _, err := s.exec(context.Background(), dir, 20*time.Second, "auth", "logout"); err != nil && s.Log != nil {
			s.Log.Warn("personal sign-in: sign out on the platform failed", "err", err.Error())
		}
	}
	os.RemoveAll(dir)
}

func (s *SignIns) stopLocked(r *run) {
	if r.cmd != nil && r.cmd.Process != nil && !r.done {
		r.cmd.Process.Signal(syscall.SIGTERM)
	}
	if r.timer != nil {
		r.timer.Stop()
	}
}

// Cancel ends owner's waiting code.
func (s *SignIns) Cancel(owner string) State {
	s.mu.Lock()
	if r := s.runs[owner]; r != nil && !r.done {
		r.st.State, r.st.Message = "canceled", ""
		s.stopLocked(r)
	}
	delete(s.runs, owner)
	s.mu.Unlock()
	return s.Status(owner)
}

// SignOut ends owner's sign-in on the platform and forgets it.
func (s *SignIns) SignOut(owner string) State {
	s.Cancel(owner)
	s.signOut(s.Dir(owner))
	s.mu.Lock()
	delete(s.tokens, owner)
	s.mu.Unlock()
	return s.Status(owner)
}

// Token is an access token of owner's sign-in with at least a few minutes
// left, renewed through the CLI when needed. Only this asks the CLI for
// the person's tokens, one run at a time per person, so a refresh token is
// never spent twice; the pw processes that use a token get it in
// PW_API_KEY, with which the CLI never renews. "" with no error: no
// sign-in.
func (s *SignIns) Token(ctx context.Context, owner string) (string, error) {
	if owner == "" {
		return "", nil
	}
	dir := s.Dir(owner)
	if !s.has(dir) || readRecord(dir) == nil {
		return "", nil
	}
	now := s.now()
	s.mu.Lock()
	if s.tokens == nil {
		s.tokens = map[string]token{}
	}
	t, ok := s.tokens[owner]
	// Fresh enough, or asked moments ago (the CLI renews only close to
	// the end, so a token near its end is used until it is renewed).
	if ok && (t.until.Sub(now) > 5*time.Minute || (now.Sub(t.asked) < 30*time.Second && t.until.After(now))) {
		s.mu.Unlock()
		return t.value, nil
	}
	if s.renewing == nil {
		s.renewing = map[string]*renewCall{}
	}
	if c, ok := s.renewing[owner]; ok {
		s.mu.Unlock()
		select {
		case <-c.done:
			return c.value, c.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	c := &renewCall{done: make(chan struct{})}
	s.renewing[owner] = c
	s.mu.Unlock()
	value, until, err := s.renew(dir)
	s.mu.Lock()
	if err == nil {
		s.tokens[owner] = token{value: value, until: until, asked: s.now()}
	}
	delete(s.renewing, owner)
	s.mu.Unlock()
	c.value, c.err = value, err
	close(c.done)
	return value, err
}

func (s *SignIns) renew(dir string) (string, time.Time, error) {
	out, err := s.exec(context.Background(), dir, 30*time.Second, "auth", "token", "--print", "-o", "json")
	if err != nil {
		return "", time.Time{}, errors.New("Your platform sign-in could not be renewed (" + err.Error() + "). Sign in again under Settings, Model access.")
	}
	var d struct {
		AccessToken string   `json:"access_token"`
		ExpiresIn   *float64 `json:"expires_in"`
	}
	if i, j := strings.Index(out, "{"), strings.LastIndex(out, "}"); i >= 0 && j > i {
		json.Unmarshal([]byte(out[i:j+1]), &d)
	}
	if strings.TrimSpace(d.AccessToken) == "" {
		return "", time.Time{}, errors.New("Your platform sign-in gave no token. Sign in again under Settings, Model access.")
	}
	until := s.now().Add(10 * time.Minute)
	if d.ExpiresIn != nil {
		until = s.now().Add(time.Duration(*d.ExpiresIn * float64(time.Second)))
	}
	return strings.TrimSpace(d.AccessToken), until, nil
}

// Key is Token without a caller's context, for a background build or a
// credential lookup: "" for none or on failure.
func (s *SignIns) Key(owner string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	k, err := s.Token(ctx, owner)
	if err != nil {
		if s.Log != nil {
			s.Log.Warn("personal sign-in: no token", "owner", libraries.OwnerHash(owner), "err", err.Error())
		}
		return ""
	}
	return k
}

// promptBuf collects the CLI's output and reports it as it grows.
type promptBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
	on func(string)
}

func (p *promptBuf) Write(b []byte) (int, error) {
	p.mu.Lock()
	p.b.Write(b)
	text := p.b.String()
	p.mu.Unlock()
	if p.on != nil {
		p.on(text)
	}
	return len(b), nil
}

func (p *promptBuf) String() string { p.mu.Lock(); defer p.mu.Unlock(); return p.b.String() }
