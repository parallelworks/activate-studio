package remote

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Two keys that end in the same 16 characters are two people: each gets a
// session of its own, and neither key is kept in the pool's map.
func TestKeysThatEndAlikeKeepSessionsApart(t *testing.T) {
	a := "pwt_alice_" + strings.Repeat("x", 4) + "0123456789abcdef"
	b := "pwt_bobby_" + strings.Repeat("y", 4) + "0123456789abcdef"
	if channelID("c", a) == channelID("c", b) {
		t.Fatalf("keys ending alike share the session id %q", channelID("c", a))
	}
	p := localPool(t, nil)
	for _, k := range []string{a, b} {
		if r, err := p.Run(context.Background(), "c", k, []string{"true"}, 30*time.Second); err != nil || r.Code != 0 {
			t.Fatalf("run: %v %+v", err, r)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.channels) != 2 {
		t.Fatalf("%d session groups for two keys, want 2", len(p.channels))
	}
	for id := range p.channels {
		if strings.Contains(id, "0123456789abcdef") {
			t.Fatalf("the session id %q carries part of the key", id)
		}
	}
}

func TestKeyID(t *testing.T) {
	if KeyID("") != "" {
		t.Fatal("no key should stand for nothing")
	}
	if KeyID("k1") == KeyID("k2") || KeyID("k1") != KeyID("k1") || len(KeyID("k1")) != 32 {
		t.Fatalf("KeyID: %q %q", KeyID("k1"), KeyID("k2"))
	}
}
