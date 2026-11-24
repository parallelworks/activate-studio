package modelkeys

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	key      = "pwt_owner.activate-1234"
	endpoint = "sk-owner-endpoint-5678"
	base     = "https://models.example.org/v1"
)

func vaultOf(t *testing.T, s *Store) map[string]map[string]any {
	t.Helper()
	b, err := os.ReadFile(s.vaultFile)
	if err != nil {
		return nil
	}
	var v map[string]map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// What v1.203.32 reads from the one owner's vault entry: its blob, as an
// endpoint when it has a base URL.
func oldRead(t *testing.T, s *Store) (string, bool) {
	t.Helper()
	e := vaultOf(t, s)[SingleOwner]
	if e == nil {
		return "", false
	}
	k, _ := s.open(e["blob"].(string))
	return k, e["baseUrl"] != nil
}

// A Studio of one keeps both keys in the vault, sealed, in one entry
// whose main value is the one earlier builds read.
func TestOneOwnerKeepsBothInTheVault(t *testing.T) {
	s := New(t.TempDir(), func() bool { return false })
	u := base
	if err := s.Set(SingleOwner, endpoint, &u, nil); err != nil {
		t.Fatal(err)
	}
	if k, isEndpoint := oldRead(t, s); k != endpoint || !isEndpoint {
		t.Fatalf("endpoint only, as before: %q %v", k, isEndpoint)
	}
	if err := s.Set(SingleOwner, key, nil, nil); err != nil {
		t.Fatal(err)
	}
	if c := s.ResolveKey(""); c == nil || c.Key != key {
		t.Fatalf("key: %+v", c)
	}
	if c := s.ResolveEndpoint(""); c == nil || c.Key != endpoint || c.Base() != base {
		t.Fatalf("endpoint: %+v", c)
	}
	if c := s.Resolve(""); c == nil || c.Key != endpoint {
		t.Fatalf("chat: %+v", c)
	}
	if c := s.ResolvePlatform(""); c == nil || c.Key != key {
		t.Fatalf("platform: %+v", c)
	}
	if k, isEndpoint := oldRead(t, s); k != key || isEndpoint {
		t.Fatalf("an earlier build reads %q %v", k, isEndpoint)
	}
	raw, _ := os.ReadFile(s.vaultFile)
	if strings.Contains(string(raw), key) || strings.Contains(string(raw), endpoint) {
		t.Fatal("a key in the clear")
	}
	if st, kst := s.Status(SingleOwner), s.KeyStatus(SingleOwner); st.Last4 != "5678" || kst.Last4 != "1234" || kst.BaseURL != nil {
		t.Fatalf("status %+v, key status %+v", st, kst)
	}
	// Removing the key leaves the endpoint, back where earlier builds
	// read it.
	s.ClearOne(SingleOwner, false)
	if k, isEndpoint := oldRead(t, s); k != endpoint || !isEndpoint || s.ResolveKey("") != nil {
		t.Fatalf("after the key went: %q %v", k, isEndpoint)
	}
	s.Set(SingleOwner, key, nil, nil)
	s.ClearOne(SingleOwner, true)
	if k, isEndpoint := oldRead(t, s); k != key || isEndpoint || s.ResolveEndpoint("") != nil || vaultOf(t, s)[SingleOwner]["endpoint"] != nil {
		t.Fatalf("after the endpoint went: %q %v", k, isEndpoint)
	}
	s.Set(SingleOwner, endpoint, &u, nil)
	s.Clear(SingleOwner)
	if vaultOf(t, s)[SingleOwner] != nil || s.Resolve("") != nil {
		t.Fatal("Clear left a key")
	}
}

// Both of a signed-in person's keys live in memory for the session's 12
// hours, and both go when it ends; a scrub removes nothing of theirs
// from disk because nothing is there.
func TestBothKeysExpireTogether(t *testing.T) {
	now := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	s := New(t.TempDir(), func() bool { return true })
	s.now = func() time.Time { return now }
	u := base
	s.Set("p1", key, nil, nil)
	s.Set("p1", endpoint, &u, nil)
	if s.ResolveKey("p1") == nil || s.ResolveEndpoint("p1") == nil {
		t.Fatal("not kept")
	}
	if _, err := os.Stat(s.vaultFile); err == nil {
		t.Fatal("a signed-in person's key was written to disk")
	}
	now = now.Add(sessionTTL + time.Minute)
	if s.ResolveKey("p1") != nil || s.ResolveEndpoint("p1") != nil || s.Status("p1").Mode != "none" || s.Seal("p1") != nil || s.SealEndpoint("p1") != nil {
		t.Fatal("a key outlived the session")
	}
}

// A sign-in comes before the ACTIVATE key for the platform and, without
// an endpoint, for chat; the endpoint is never the platform's.
func TestWhichKeyEachUses(t *testing.T) {
	s := New(t.TempDir(), func() bool { return true })
	signedIn := ""
	s.SignIn = func(string) string { return signedIn }
	u := base
	s.Set("p1", endpoint, &u, nil)
	if c := s.ResolvePlatform("p1"); c != nil {
		t.Fatalf("the platform was given the endpoint: %+v", c)
	}
	s.Set("p1", key, nil, nil)
	for _, c := range []struct {
		signIn, chat, plat string
	}{{"", endpoint, key}, {"sign-in-token", endpoint, "sign-in-token"}} {
		signedIn = c.signIn
		if got := s.ResolveOrSignIn("p1"); got == nil || got.Key != c.chat {
			t.Errorf("sign-in %q: chat %+v", c.signIn, got)
		}
		if got := s.ResolvePlatform("p1"); got == nil || got.Key != c.plat || got.Base() != "" {
			t.Errorf("sign-in %q: platform %+v", c.signIn, got)
		}
	}
	s.ClearOne("p1", true)
	signedIn = "sign-in-token"
	if got := s.ResolveOrSignIn("p1"); got == nil || got.Key != "sign-in-token" {
		t.Errorf("no endpoint, signed in: chat %+v", got)
	}
	signedIn = ""
	if got := s.ResolveOrSignIn("p1"); got == nil || got.Key != key {
		t.Errorf("no endpoint: chat %+v", got)
	}
}
