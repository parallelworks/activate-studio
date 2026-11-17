package personal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCLI is a pw CLI for tests: device sign-in waits for $CTL/approve,
// whoami prints $CTL/who, token prints a numbered token and notes overlap,
// logout is counted. It records the PW_API_KEY and PW_CONTEXT it saw.
const fakeCLI = `#!/bin/bash
ctl="$FAKE_CTL"
echo "key=${PW_API_KEY-unset} ctx=${PW_CONTEXT-unset} dbus=${DBUS_SESSION_BUS_ADDRESS-unset}" >> "$ctl/env"
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
      echo '{"identities":{}}' > "$PW_CREDENTIALS_DIR/credentials"
      exit 0
    fi
    sleep 0.05
  done
  exit 1 ;;
"auth whoami") cat "$ctl/who" ;;
"auth token")
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
