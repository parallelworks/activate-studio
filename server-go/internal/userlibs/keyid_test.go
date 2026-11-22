package userlibs

import "testing"

// The account a key reached is kept per key: a second key that ends like
// the first must not find the first's entry.
func TestAccountCheckIsKeptPerWholeKey(t *testing.T) {
	a := "pwt_alice_xxxx0123456789abcdef"
	b := "pwt_bobby_yyyy0123456789abcdef"
	if keyID(a)+"|s" == keyID(b)+"|s" {
		t.Fatal("two keys ending alike share the account and identity entries")
	}
}
