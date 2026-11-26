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
	"regexp"
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
	// Kept is told whose sign-in was just kept; nil for none.
	Kept func(owner string)
	// Check asks the platform about key (GET /api/auth/whoami) and gives
	// the status it answered, to confirm a refusal before a sign-in is
	// said to have stopped working; nil, and a refusal is taken as said.
	Check func(ctx context.Context, key string) (int, error)
	// Retry is how long a sign-in that stopped working waits before it is
	// tried again; 0 is a minute.
	Retry time.Duration

	mu       sync.Mutex
	runs     map[string]*run
	tokens   map[string]token
	renewing map[string]*renewCall
	// issued is whose each token handed out is, until it expires.
	issued map[string]issue
	// broken is each kept sign-in that stopped working, until it works
	// again or the person signs in again or out.
	broken map[string]broken
	// checking is whose refused token is being checked.
	checking map[string]bool
	// checked is when Status last had each person's token checked.
	checked map[string]time.Time
}

// broken is why a kept sign-in stopped working: the pw CLI could not
// renew it, or the platform refused its token.
type broken struct {
	why string
	// token is the token the platform refused; "" when renewing failed.
	token string
	at    time.Time
}

// BrokenError is Token's answer for a sign-in that stopped working: the
// person is asked to sign in again, and it is logged once, not per call.
type BrokenError struct{ Why string }

func (e *BrokenError) Error() string { return e.Why }

// Messages for a kept sign-in that stopped working.
const (
	notRenewed = "Your ACTIVATE sign-in could not be renewed"
	refused    = "ACTIVATE no longer accepts your sign-in"
	againTail  = ". Sign in again under Settings, Accounts."
)

// secretish is anything in a message that might be a token: a JWT, a
// pw key, or a long unbroken run of key characters.
var secretish = regexp.MustCompile(`(?i)\b(?:ey[\w-]+\.[\w-]+\.[\w-]*|pw[tk]_\S+|[a-z0-9_\-]{32,})`)

// redact is msg without anything that looks like a token.
func redact(msg string) string { return secretish.ReplaceAllString(msg, "[redacted]") }

type issue struct {
	owner string
	until time.Time
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

// Home is the home folder owner's pw CLI runs in, here: its ssh key
// (~/.ssh/pwcli), which the CLI registers with the account it acts for, is
// the person's own, never the service account's (internal/pwhome).
func (s *SignIns) Home(owner string) string {
	return filepath.Join(s.Base, "users", libraries.OwnerHash(owner), "home")
}

// homeFor is the person's home beside a sign-in folder (Dir, or a staging
// or old one next to it), made if missing.
func homeFor(dir string) string {
	h := filepath.Join(filepath.Dir(dir), "home")
	os.MkdirAll(h, 0o700)
	return h
}

// env is the CLI's environment for the sign-in in dir: no deployment key
// or context, no desktop keyring (the sign-in must stay in dir's file),
// no host other than the one named on the command line, and the person's
// own home.
func (s *SignIns) env(dir string) []string {
	return s.Environ(map[string]string{
		"HOME":                     homeFor(dir),
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

// codeLine is a line holding only a device code, such as ABCD-EFGH.
var codeLine = regexp.MustCompile(`^[A-Z0-9]{3,}(?:-[A-Z0-9]{3,})+$`)

// failureOf is what the CLI said went wrong in a sign-in's output: its
// last line that is not the device prompt or an update notice, "" when it
// said nothing else.
func failureOf(out string) string {
	last := ""
	for _, l := range strings.Split(platform.StripANSI(out), "\n") {
		l = strings.TrimSpace(l)
		switch {
		case l == "", strings.HasPrefix(l, "To sign in"), strings.HasPrefix(l, "and enter the code"),
			strings.HasPrefix(l, "Waiting for approval"), strings.Contains(l, "new version of the PW CLI"),
			strings.HasPrefix(l, "Run 'pw upgrade'"), strings.HasPrefix(l, "http://"), strings.HasPrefix(l, "https://"),
			codeLine.MatchString(l):
			continue
		}
		last = l
	}
	return last
}

// misplaced is a sign-in the CLI saved under its home folder instead of
// PW_CREDENTIALS_DIR, as a pw CLI older than that setting does, written
// since the sign-in started; "" for none.
func misplaced(staging string, since time.Time) string {
	home := homeFor(staging)
	for _, p := range []string{filepath.Join(home, ".config", "pw", "credentials"), filepath.Join(home, "pw", ".credentials")} {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && !fi.ModTime().Before(since.Add(-time.Second)) {
			return p
		}
	}
	return ""
}

// has: dir holds a sign-in the CLI saved there as PW_CREDENTIALS_DIR.
func (s *SignIns) has(dir string) bool {
	fi, err := os.Stat(platform.CredentialsIn(dir))
	return err == nil && fi.Mode().IsRegular()
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
		st := State{State: "signed-in", Host: rec.Host, Account: rec.Account, SignedIn: rec.SignedIn}
		// A kept sign-in still has to work: asking for its token (kept, or
		// renewed when near its end) finds one the CLI cannot renew, and
		// asking the platform about it one it no longer accepts (as after
		// the platform dropped its tokens). Only Settings, Accounts asks.
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		tok, err := s.Token(ctx, owner)
		if err == nil && tok != "" {
			err = s.checkKept(ctx, owner, tok)
		}
		var b *BrokenError
		if errors.As(err, &b) {
			st.State, st.Message = "expired", b.Why
		}
		return st
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
	started := time.Now()
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
		st := s.finish(owner, username, host, staging, started, err, out.String())
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
func (s *SignIns) finish(owner, username, host, staging string, started time.Time, waitErr error, out string) State {
	fail := func(msg string) State {
		os.RemoveAll(staging)
		return State{State: "failed", Host: host, Message: msg}
	}
	if waitErr == nil && !s.has(staging) {
		// A CLI that does not know PW_CREDENTIALS_DIR saves the sign-in in
		// the person's home here, where it also looks for it later; a copy
		// in the sign-in folder marks it as kept.
		if p := misplaced(staging, started); p != "" {
			if b, err := os.ReadFile(p); err == nil && os.WriteFile(filepath.Join(staging, ".credentials"), b, 0o600) == nil {
				s.logOutput("personal sign-in: the pw CLI saved it under its home folder, not PW_CREDENTIALS_DIR, and it was kept from there", staging, out)
			}
		}
	}
	if waitErr != nil || !s.has(staging) {
		s.logOutput("personal sign-in did not finish", staging, out)
		said := failureOf(out)
		switch {
		case waitErr != nil && said != "":
			return fail("The pw CLI stopped: " + said)
		case waitErr != nil:
			return fail("The pw CLI stopped before the sign-in finished (" + waitErr.Error() + "). The server log has its output.")
		default:
			return fail("The pw CLI reported the sign-in as finished, but the Studio did not find it where the CLI saves it. The server log lists what the CLI left and its version.")
		}
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
	delete(s.broken, owner)
	delete(s.checked, owner)
	s.mu.Unlock()
	if hadOld {
		go func() { s.signOut(old) }()
	}
	if s.Log != nil {
		s.Log.Info("personal sign-in kept", "owner", libraries.OwnerHash(owner), "host", host)
	}
	if s.Kept != nil {
		s.Kept(owner)
	}
	return State{State: "signed-in", Host: host, Account: who, SignedIn: rec.SignedIn}
}

// logOutput writes the CLI's output from a sign-in to the server log with
// its version, so an operator can see what the page summarizes.
func (s *SignIns) logOutput(msg, dir, out string) {
	if s.Log == nil {
		return
	}
	version, _ := s.exec(context.Background(), dir, 10*time.Second, "--version")
	// The names of what the CLI left in the sign-in folder, never contents.
	var left []string
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			left = append(left, e.Name())
		}
	}
	s.Log.Warn(msg, "cli", s.CLI(), "version", strings.TrimSpace(firstLine(version)), "left", strings.Join(left, " "), "output", strings.TrimSpace(platform.StripANSI(out)))
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
	delete(s.broken, owner)
	delete(s.checked, owner)
	for k, v := range s.issued {
		if v.owner == owner {
			delete(s.issued, k)
		}
	}
	s.mu.Unlock()
	// The ssh key the CLI made for the person goes too; the public half it
	// registered stays on their account, where it can be removed.
	os.RemoveAll(s.Home(owner))
	return s.Status(owner)
}

// OwnerOfToken is whose sign-in issued key, "" for a key it did not issue
// or one that has expired.
func (s *SignIns) OwnerOfToken(key string) string {
	if key == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.issued[key]; ok && v.until.After(s.now()) {
		return v.owner
	}
	return ""
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
	// A sign-in that stopped working is tried again after a while, in
	// case the platform was only away; until then it answers why.
	if b, ok := s.broken[owner]; ok {
		if now.Sub(b.at) < s.retry() {
			s.mu.Unlock()
			return "", &BrokenError{Why: b.why}
		}
		delete(s.tokens, owner)
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
	b, wasBroken := s.broken[owner]
	switch {
	case err != nil:
		err = s.breakLocked(owner, dir, notRenewed+" ("+redact(err.Error())+")"+againTail, "")
	case wasBroken && b.token != "" && value == b.token:
		// The CLI still holds the token the platform refused.
		b.at = s.now()
		s.broken[owner] = b
		value, err = "", &BrokenError{Why: b.why}
	case wasBroken:
		delete(s.broken, owner)
		if s.Log != nil {
			s.Log.Info("personal sign-in works again", "owner", libraries.OwnerHash(owner))
		}
	}
	if err == nil {
		s.tokens[owner] = token{value: value, until: until, asked: s.now()}
		if s.issued == nil {
			s.issued = map[string]issue{}
		}
		for k, v := range s.issued {
			if !v.until.After(s.now()) {
				delete(s.issued, k)
			}
		}
		s.issued[value] = issue{owner: owner, until: until}
	}
	delete(s.renewing, owner)
	s.mu.Unlock()
	c.value, c.err = value, err
	close(c.done)
	return value, err
}

func (s *SignIns) retry() time.Duration {
	if s.Retry > 0 {
		return s.Retry
	}
	return time.Minute
}

// breakLocked marks owner's sign-in as stopped working, logging it once
// (never the token), and is the error to answer with.
func (s *SignIns) breakLocked(owner, dir, why, token string) error {
	if s.broken == nil {
		s.broken = map[string]broken{}
	}
	if _, was := s.broken[owner]; !was && s.Log != nil {
		host := ""
		if rec := readRecord(dir); rec != nil {
			host = rec.Host
		}
		s.Log.Warn("personal sign-in stopped working; the person is asked to sign in again", "owner", libraries.OwnerHash(owner), "host", host, "why", why)
	}
	s.broken[owner] = broken{why: why, token: token, at: s.now()}
	delete(s.tokens, owner)
	return &BrokenError{Why: why}
}

// Refused hears that the platform answered 401 to key. A key one of the
// kept sign-ins issued is checked once more (Check), and if the platform
// refuses it again, that sign-in has stopped working: the person is asked
// to sign in again. Keys it did not issue are not its concern.
func (s *SignIns) Refused(key string) {
	owner := s.OwnerOfToken(key)
	if owner == "" {
		return
	}
	s.mu.Lock()
	if _, ok := s.broken[owner]; ok || s.checking[owner] {
		s.mu.Unlock()
		return
	}
	if s.checking == nil {
		s.checking = map[string]bool{}
	}
	s.checking[owner] = true
	s.mu.Unlock()
	status := 401
	if s.Check != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		st, err := s.Check(ctx, key)
		cancel()
		if err != nil {
			st = 0
		}
		status = st
	}
	s.mu.Lock()
	delete(s.checking, owner)
	if status == 401 {
		if t, ok := s.tokens[owner]; !ok || t.value == key {
			s.breakLocked(owner, s.Dir(owner), refused+againTail, key)
		}
	}
	s.mu.Unlock()
}

// checkKept asks the platform about owner's token (Check) unless it was
// asked in the last ten seconds; a refusal means the sign-in stopped
// working.
func (s *SignIns) checkKept(ctx context.Context, owner, tok string) error {
	if s.Check == nil {
		return nil
	}
	s.mu.Lock()
	if s.checked == nil {
		s.checked = map[string]time.Time{}
	}
	if s.now().Sub(s.checked[owner]) < 10*time.Second {
		s.mu.Unlock()
		return nil
	}
	s.checked[owner] = s.now()
	s.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	status, err := s.Check(cctx, tok)
	cancel()
	if err != nil || status != 401 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.broken[owner]; ok {
		return &BrokenError{Why: b.why}
	}
	return s.breakLocked(owner, s.Dir(owner), refused+againTail, tok)
}

// Stopped is why owner's kept sign-in stopped working, "" while it works
// or with none.
func (s *SignIns) Stopped(owner string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.broken[owner].why
}

func (s *SignIns) renew(dir string) (string, time.Time, error) {
	out, err := s.exec(context.Background(), dir, 30*time.Second, "auth", "token", "--print", "-o", "json")
	if err != nil {
		return "", time.Time{}, err
	}
	var d struct {
		AccessToken string   `json:"access_token"`
		ExpiresIn   *float64 `json:"expires_in"`
	}
	if i, j := strings.Index(out, "{"), strings.LastIndex(out, "}"); i >= 0 && j > i {
		json.Unmarshal([]byte(out[i:j+1]), &d)
	}
	if strings.TrimSpace(d.AccessToken) == "" {
		return "", time.Time{}, errors.New("the pw CLI gave no token")
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
		var b *BrokenError
		if s.Log != nil && !errors.As(err, &b) {
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
