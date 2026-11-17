package kb

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// StateKey names what the Studio keeps on this host for a library: its
// labels, the copies its previews are made from, its index settings. A
// person's library (index root remote:<owner hash>:<id>) is named by its
// id and its owner, since two people's libraries can have one id (two
// spaces made of folders called studio, or two people adding one shared
// folder), and what one keeps must never reach the other. Every other
// library, and the libraries of the one user of a Studio with sign-in
// off, keep the id alone, as before.
func StateKey(id, indexRoot string) string {
	rest, ok := strings.CutPrefix(indexRoot, "remote:")
	if !ok {
		return id
	}
	hash, rid, ok := strings.Cut(rest, ":")
	if !ok || rid != id || hash == singleOwnerHash {
		return id
	}
	return id + "@" + hash
}

var singleOwnerHash = func() string {
	s := sha256.Sum256([]byte("single"))
	return hex.EncodeToString(s[:])[:16]
}()

// StateKey is the library's StateKey.
func (l Library) StateKey() string { return StateKey(l.ID, l.IndexRoot) }
