package sessions

import "testing"

// Keys that end alike are two people: the cache keeps them apart, and holds
// no part of either key.
func TestCacheKeysAreWholeKeyHashes(t *testing.T) {
	a, b := "pwt_alice_xxxx0123456789abcdef", "pwt_bobby_yyyy0123456789abcdef"
	if cacheKey(a) == cacheKey(b) {
		t.Fatalf("keys ending alike share the cache entry %q", cacheKey(a))
	}
	for _, k := range []string{cacheKey(a), cacheKey(b)} {
		if len(k) < 32 || k[len(k)-4:] == "cdef" || k[len(k)-6:] == "cdef30" {
			t.Fatalf("the cache key %q carries part of the key", k)
		}
	}
}
