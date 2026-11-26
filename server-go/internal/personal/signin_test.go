package personal

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCLI is a pw CLI for tests: device sign-in waits for $CTL/approve,
// then saves .credentials in PW_CREDENTIALS_DIR, as the real CLI does;
// whoami prints $CTL/who, token prints a numbered token and notes overlap,
// logout is counted. It records the PW_API_KEY and PW_CONTEXT it saw.
const fakeCLI = `#!/bin/bash
ctl="$FAKE_CTL"
echo "key=${PW_API_KEY-unset} ctx=${PW_CONTEXT-unset} dbus=${DBUS_SESSION_BUS_ADDRESS-unset} home=$HOME" >> "$ctl/env"
case "$1 $2" in
"auth login")
  echo "To sign in, open this page on any device:"
  echo
  echo "  https://$4/device"
  echo
  echo "and enter the code:"
  echo
  echo "  ABCD-EFGH"
  echo
  echo "Waiting for approval..."
  for i in $(seq 1 200); do
    if [ -e "$ctl/approve" ]; then
      if [ -e "$ctl/fail" ]; then echo "Error: the platform refused the device code" >&2; exit 1; fi
      if [ -e "$ctl/oldcli" ]; then
        mkdir -p "$HOME/.config/pw"; echo '{"identities":{}}' > "$HOME/.config/pw/credentials"; exit 0
      fi
      echo '{"identities":{}}' > "$PW_CREDENTIALS_DIR/.credentials"
      exit 0
    fi
    sleep 0.05
  done
  exit 1 ;;
"auth whoami") cat "$ctl/who" ;;
"--version ") echo "pw version v0.0.1" ;;
"auth token")
  if [ -e "$ctl/refuse" ]; then echo x >> "$ctl/token-calls"; echo "Error: refresh failed: the platform refused refresh token rt_0123456789abcdef0123456789abcdef (401)" >&2; exit 1; fi
  if [ -e "$ctl/same" ]; then echo x >> "$ctl/token-calls"; echo '{"access_token":"tok-1","expires_in":3600}'; exit 0; fi
  if [ -e "$ctl/busy" ]; then echo overlap >> "$ctl/overlap"; fi
  touch "$ctl/busy"; echo x >> "$ctl/token-calls"; sleep 0.2; rm -f "$ctl/busy"
  n=$(wc -l < "$ctl/token-calls")
  echo "{\"access_token\":\"tok-$n\",\"token_type\":\"Bearer\",\"expires_in\":3600}" ;;
"auth logout") echo x >> "$ctl/logouts" ;;
esac
`

func testEnviron(set map[string]string) []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := set[k]; ok {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range set {
		if v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}

func newTest(t *testing.T) (*SignIns, string) {
	t.Helper()
	dir := t.TempDir()
	ctl := filepath.Join(dir, "ctl")
	os.MkdirAll(ctl, 0o700)
	cli := filepath.Join(dir, "pw")
	if err := os.WriteFile(cli, []byte(fakeCLI), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_CTL", ctl)
	t.Setenv("PW_API_KEY", "deployment-key")
	t.Setenv("PW_CONTEXT", "deployment-context")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/bus")
	s := &SignIns{
		Base:    filepath.Join(dir, "index"),
		Host:    func() string { return "platform.example.com" },
		CLI:     func() string { return cli },
		Environ: testEnviron,
	}
	return s, ctl
}

func waitState(t *testing.T, s *SignIns, owner, want string) State {
	t.Helper()
	for i := 0; i < 200; i++ {
		if st := s.Status(owner); st.State == want {
			return st
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("state never became %s: %+v", want, s.Status(owner))
	return State{}
}

func lines(t *testing.T, path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "\n")
}

func TestSignInIsKeptForTheSamePerson(t *testing.T) {
	s, ctl := newTest(t)
	os.WriteFile(filepath.Join(ctl, "who"), []byte("Alice.Smith\n"), 0o600)
	st, err := s.Start("user:alice", "alice.smith", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "waiting" || st.Code != "ABCD-EFGH" || st.URL != "https://platform.example.com/device" {
		t.Fatalf("prompt: %+v", st)
	}
	os.WriteFile(filepath.Join(ctl, "approve"), nil, 0o600)
	st = waitState(t, s, "user:alice", "signed-in")
	if st.Account != "Alice.Smith" {
		t.Fatalf("account %q", st.Account)
	}
	fi, err := os.Stat(s.Dir("user:alice"))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("folder: %v %v", err, fi)
	}
	tok, err := s.Token(context.Background(), "user:alice")
	if err != nil || tok != "tok-1" {
		t.Fatalf("token %q %v", tok, err)
	}
	// Another person has no sign-in and so no token.
	if tok, err := s.Token(context.Background(), "user:bob"); tok != "" || err != nil {
		t.Fatalf("bob: %q %v", tok, err)
	}
	b, _ := os.ReadFile(filepath.Join(ctl, "env"))
	if strings.Contains(string(b), "deployment-key") || strings.Contains(string(b), "deployment-context") || strings.Contains(string(b), "/run/bus") {
		t.Fatalf("the CLI saw the deployment's credential or keyring:\n%s", b)
	}
	// Every run was in the person's own home, so the ssh key the CLI makes
	// and registers is theirs.
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if !strings.HasSuffix(l, " home="+s.Home("user:alice")) {
			t.Fatalf("a CLI run outside the person's home: %q", l)
		}
	}
	// The token maps back to the person while it lasts, and to no one after.
	if s.OwnerOfToken("tok-1") != "user:alice" || s.OwnerOfToken("tok-x") != "" {
		t.Fatal("token owner lookup")
	}
	s.SignOut("user:alice")
	if s.OwnerOfToken("tok-1") != "" {
		t.Fatal("a signed-out token still maps to its owner")
	}
	if _, err := os.Stat(s.Home("user:alice")); err == nil {
		t.Fatal("the person's CLI home (and its ssh key) was kept after sign out")
	}
}

func TestSignInForAnotherAccountIsDropped(t *testing.T) {
	s, ctl := newTest(t)
	os.WriteFile(filepath.Join(ctl, "who"), []byte("mallory\n"), 0o600)
	if _, err := s.Start("user:alice", "alice", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(ctl, "approve"), nil, 0o600)
	st := waitState(t, s, "user:alice", "failed")
	if !strings.Contains(st.Message, "mallory") || !strings.Contains(st.Message, "alice") {
		t.Fatalf("message: %q", st.Message)
	}
	if lines(t, filepath.Join(ctl, "logouts")) != 1 {
		t.Fatal("the other account's sign-in was not signed out")
	}
	if _, err := os.Stat(s.Dir("user:alice")); err == nil {
		t.Fatal("a sign-in for another account was kept")
	}
	entries, _ := os.ReadDir(filepath.Dir(s.Dir("user:alice")))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "pw.new-") {
			t.Fatalf("staging left behind: %s", e.Name())
		}
	}
	if tok, _ := s.Token(context.Background(), "user:alice"); tok != "" {
		t.Fatalf("token from a dropped sign-in: %q", tok)
	}
}

func TestTokenRenewsOnceForConcurrentCallers(t *testing.T) {
	s, ctl := newTest(t)
	os.WriteFile(filepath.Join(ctl, "who"), []byte("alice\n"), 0o600)
	s.Start("user:alice", "alice", 5*time.Second)
	os.WriteFile(filepath.Join(ctl, "approve"), nil, 0o600)
	waitState(t, s, "user:alice", "signed-in")
	var wg sync.WaitGroup
	got := make([]string, 12)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], _ = s.Token(context.Background(), "user:alice")
		}(i)
	}
	wg.Wait()
	for _, g := range got {
		if g != "tok-1" {
			t.Fatalf("tokens: %v", got)
		}
	}
	if n := lines(t, filepath.Join(ctl, "token-calls")); n != 1 {
		t.Fatalf("the CLI was asked %d times", n)
	}
	if _, err := os.Stat(filepath.Join(ctl, "overlap")); err == nil {
		t.Fatal("two renewals ran at once")
	}
	// A token near its end is renewed on the next ask.
	s.mu.Lock()
	tk := s.tokens["user:alice"]
	tk.until, tk.asked = time.Now().Add(time.Minute), time.Now().Add(-time.Minute)
	s.tokens["user:alice"] = tk
	s.mu.Unlock()
	if tok, _ := s.Token(context.Background(), "user:alice"); tok != "tok-2" {
		t.Fatalf("not renewed: %q", tok)
	}
}

func TestSignOutEndsTheSignIn(t *testing.T) {
	s, ctl := newTest(t)
	os.WriteFile(filepath.Join(ctl, "who"), []byte("alice\n"), 0o600)
	s.Start("user:alice", "alice", 5*time.Second)
	os.WriteFile(filepath.Join(ctl, "approve"), nil, 0o600)
	waitState(t, s, "user:alice", "signed-in")
	if st := s.SignOut("user:alice"); st.State != "none" {
		t.Fatalf("after sign out: %+v", st)
	}
	if lines(t, filepath.Join(ctl, "logouts")) != 1 {
		t.Fatal("not signed out on the platform")
	}
	if tok, _ := s.Token(context.Background(), "user:alice"); tok != "" {
		t.Fatalf("token after sign out: %q", tok)
	}
}

func TestStartingAgainReplacesTheWaitingCode(t *testing.T) {
	s, ctl := newTest(t)
	os.WriteFile(filepath.Join(ctl, "who"), []byte("alice\n"), 0o600)
	if st, _ := s.Start("user:alice", "alice", 5*time.Second); st.State != "waiting" {
		t.Fatalf("first: %+v", st)
	}
	if st, _ := s.Start("user:alice", "alice", 5*time.Second); st.State != "waiting" {
		t.Fatalf("second: %+v", st)
	}
	os.WriteFile(filepath.Join(ctl, "approve"), nil, 0o600)
	waitState(t, s, "user:alice", "signed-in")
	time.Sleep(200 * time.Millisecond)
	entries, _ := os.ReadDir(filepath.Dir(s.Dir("user:alice")))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "pw.new-") {
			t.Fatalf("staging left behind: %s", e.Name())
		}
	}
}

// TestSignInFailureSaysWhy: when the CLI stops after the prompt, the page
// gets what it said, never the prompt.
func TestSignInFailureSaysWhy(t *testing.T) {
	s, ctl := newTest(t)
	os.WriteFile(filepath.Join(ctl, "who"), []byte("alice\n"), 0o600)
	os.WriteFile(filepath.Join(ctl, "fail"), nil, 0o600)
	if _, err := s.Start("user:alice", "alice", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(ctl, "approve"), nil, 0o600)
	st := waitState(t, s, "user:alice", "failed")
	if st.Message != "The pw CLI stopped: Error: the platform refused the device code" {
		t.Fatalf("message: %q", st.Message)
	}
}

// TestSignInSavedUnderHomeIsKept: a CLI that saves the sign-in under its
// home, not PW_CREDENTIALS_DIR, still leaves the person signed in.
func TestSignInSavedUnderHomeIsKept(t *testing.T) {
	s, ctl := newTest(t)
	os.WriteFile(filepath.Join(ctl, "who"), []byte("alice\n"), 0o600)
	os.WriteFile(filepath.Join(ctl, "oldcli"), nil, 0o600)
	if _, err := s.Start("user:alice", "alice", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(ctl, "approve"), nil, 0o600)
	st := waitState(t, s, "user:alice", "signed-in")
	if st.Account != "alice" {
		t.Fatalf("account %q", st.Account)
	}
	if tok, err := s.Token(context.Background(), "user:alice"); err != nil || tok == "" {
		t.Fatalf("token %q %v", tok, err)
	}
}

func TestFailureOf(t *testing.T) {
	prompt := "To sign in, open this page on any device:\n\n  https://platform.example.com/device\n\nand enter the code:\n\n  ABCD-EFGH\n\nWaiting for approval...\n"
	if got := failureOf(prompt); got != "" {
		t.Errorf("the prompt alone: %q", got)
	}
	if got := failureOf(prompt + "\x1b[31mError: expired_token\x1b[0m\n"); got != "Error: expired_token" {
		t.Errorf("after the prompt: %q", got)
	}
}

// logBuf is a server log a test can read while sign-ins write to it.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func signedIn(t *testing.T) (*SignIns, string, *logBuf) {
	t.Helper()
	s, ctl := newTest(t)
	log := &logBuf{}
	s.Log = slog.New(slog.NewTextHandler(log, nil))
	os.WriteFile(filepath.Join(ctl, "who"), []byte("alice\n"), 0o600)
	s.Start("user:alice", "alice", 5*time.Second)
	os.WriteFile(filepath.Join(ctl, "approve"), nil, 0o600)
	waitState(t, s, "user:alice", "signed-in")
	if tok, err := s.Token(context.Background(), "user:alice"); tok != "tok-1" || err != nil {
		t.Fatalf("token %q %v", tok, err)
	}
	return s, ctl, log
}

// nearEnd makes the kept token one the next ask renews.
func nearEnd(s *SignIns, owner string) {
	s.mu.Lock()
	tk := s.tokens[owner]
	tk.until, tk.asked = time.Now().Add(time.Minute), time.Now().Add(-time.Minute)
	s.tokens[owner] = tk
	s.mu.Unlock()
}

func stoppedLines(log *logBuf) int {
	return strings.Count(log.String(), "personal sign-in stopped working")
}

// A kept sign-in whose renewal the platform refuses (as after a platform
// that dropped its refresh tokens) says so, and asks to sign in again,
// instead of still saying Signed in; the server logs it once, without the
// token; the CLI is not asked again on every request; and it works again
// once the platform does, or after signing in again.
func TestASignInWhoseRenewalIsRefusedAsksToSignInAgain(t *testing.T) {
	s, ctl, log := signedIn(t)
	nearEnd(s, "user:alice")
	os.WriteFile(filepath.Join(ctl, "refuse"), nil, 0o600)
	_, err := s.Token(context.Background(), "user:alice")
	var b *BrokenError
	if !errors.As(err, &b) || !strings.HasPrefix(b.Why, "Your ACTIVATE sign-in could not be renewed (") || !strings.HasSuffix(b.Why, ". Sign in again under Settings, Accounts.") {
		t.Fatalf("error: %v", err)
	}
	st := s.Status("user:alice")
	if st.State != "expired" || st.Account != "alice" || st.Message != b.Why {
		t.Fatalf("status: %+v", st)
	}
	if s.Stopped("user:alice") != b.Why || s.Key("user:alice") != "" {
		t.Fatal("Stopped or Key")
	}
	calls := lines(t, filepath.Join(ctl, "token-calls"))
	for i := 0; i < 5; i++ {
		s.Token(context.Background(), "user:alice")
		s.Status("user:alice")
	}
	if n := lines(t, filepath.Join(ctl, "token-calls")); n != calls {
		t.Fatalf("the CLI was asked again %d times within the minute", n-calls)
	}
	if n := stoppedLines(log); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, log)
	}
	if l := log.String(); strings.Contains(l, "rt_0123456789") || strings.Contains(l, "tok-") || !strings.Contains(l, "[redacted]") || !strings.Contains(l, "host=platform.example.com") {
		t.Fatalf("log:\n%s", l)
	}
	if strings.Contains(b.Why, "rt_0123456789") {
		t.Fatalf("the message shows the token: %s", b.Why)
	}
	// The platform answers again: after the wait, it works again.
	os.Remove(filepath.Join(ctl, "refuse"))
	s.Retry = 20 * time.Millisecond
	time.Sleep(30 * time.Millisecond)
	if tok, err := s.Token(context.Background(), "user:alice"); err != nil || tok == "" || tok == "tok-1" {
		t.Fatalf("again: %q %v", tok, err)
	}
	if st := s.Status("user:alice"); st.State != "signed-in" || s.Stopped("user:alice") != "" {
		t.Fatalf("status after: %+v", st)
	}
	if !strings.Contains(log.String(), "personal sign-in works again") {
		t.Fatal("no log of it working again")
	}
	// Stopped again, then signing in again clears it at once.
	s.Retry = time.Hour
	nearEnd(s, "user:alice")
	os.WriteFile(filepath.Join(ctl, "refuse"), nil, 0o600)
	if st := s.Status("user:alice"); st.State != "expired" {
		t.Fatalf("not expired: %+v", st)
	}
	if n := stoppedLines(log); n != 2 {
		t.Fatalf("a second time stopped is logged once more, got %d", n)
	}
	os.Remove(filepath.Join(ctl, "refuse"))
	os.Remove(filepath.Join(ctl, "approve"))
	s.Start("user:alice", "alice", 5*time.Second)
	os.WriteFile(filepath.Join(ctl, "approve"), nil, 0o600)
	waitState(t, s, "user:alice", "signed-in")
	if s.Stopped("user:alice") != "" {
		t.Fatal("still stopped after signing in again")
	}
}

// A token the platform refuses while the CLI still holds it (its refresh
// not yet due) is checked once more; refused again, the sign-in has
// stopped working, and stays so while the CLI gives that token.
func TestATokenThePlatformRefusesAsksToSignInAgain(t *testing.T) {
	s, ctl, log := signedIn(t)
	status := 200
	var checks int
	var mu sync.Mutex
	s.Check = func(ctx context.Context, key string) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		checks++
		if key != "tok-1" {
			t.Errorf("checked %q", key)
		}
		return status, nil
	}
	// A refusal the platform does not repeat changes nothing.
	s.Refused("tok-1")
	if s.Stopped("user:alice") != "" || checks != 1 {
		t.Fatal("stopped on a refusal the check did not repeat")
	}
	// A key no sign-in issued is not checked.
	s.Refused("someone-elses-key")
	if checks != 1 {
		t.Fatal("checked a key no sign-in issued")
	}
	status = 401
	s.Refused("tok-1")
	want := "ACTIVATE no longer accepts your sign-in. Sign in again under Settings, Accounts."
	if s.Stopped("user:alice") != want {
		t.Fatalf("stopped: %q", s.Stopped("user:alice"))
	}
	if st := s.Status("user:alice"); st.State != "expired" || st.Message != want {
		t.Fatalf("status: %+v", st)
	}
	s.Refused("tok-1")
	if checks != 2 || stoppedLines(log) != 1 || strings.Contains(log.String(), "tok-1") {
		t.Fatalf("checks %d, log:\n%s", checks, log)
	}
	// After the wait the CLI is asked again; it still gives the refused
	// token, so the sign-in stays stopped.
	os.WriteFile(filepath.Join(ctl, "same"), nil, 0o600)
	s.Retry = 20 * time.Millisecond
	time.Sleep(30 * time.Millisecond)
	if _, err := s.Token(context.Background(), "user:alice"); err == nil || err.Error() != want {
		t.Fatalf("the refused token was used again: %v", err)
	}
	// A new token works again.
	os.Remove(filepath.Join(ctl, "same"))
	time.Sleep(30 * time.Millisecond)
	if tok, err := s.Token(context.Background(), "user:alice"); err != nil || tok == "tok-1" {
		t.Fatalf("again: %q %v", tok, err)
	}
	if s.Stopped("user:alice") != "" {
		t.Fatal("still stopped")
	}
}

// Accounts asks the platform about a kept sign-in's token, at most once in
// ten seconds, so it finds one the platform no longer accepts even when no
// other page has asked the platform anything yet.
func TestStatusFindsATokenThePlatformNoLongerAccepts(t *testing.T) {
	s, _, log := signedIn(t)
	status, checks := 200, 0
	s.Check = func(ctx context.Context, key string) (int, error) { checks++; return status, nil }
	if st := s.Status("user:alice"); st.State != "signed-in" || checks != 1 {
		t.Fatalf("%+v %d", st, checks)
	}
	status = 401
	if st := s.Status("user:alice"); st.State != "signed-in" || checks != 1 {
		t.Fatalf("checked again within ten seconds: %+v %d", st, checks)
	}
	s.mu.Lock()
	s.checked["user:alice"] = time.Now().Add(-11 * time.Second)
	s.mu.Unlock()
	if st := s.Status("user:alice"); st.State != "expired" || st.Message != "ACTIVATE no longer accepts your sign-in. Sign in again under Settings, Accounts." || checks != 2 {
		t.Fatalf("%+v %d", st, checks)
	}
	if stoppedLines(log) != 1 || s.Key("user:alice") != "" {
		t.Fatal("log or key")
	}
}
