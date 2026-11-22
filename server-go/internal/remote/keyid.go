package remote

import (
	"crypto/sha256"
	"encoding/hex"
)

// KeyID stands for a key in the maps that keep something per key: a kept
// session, the account a key reaches, a probe that passed. It is a hash of
// the whole key, so two keys that end alike never share an entry (the last
// 16 characters were used before, which a key chosen to end like another's
// would have matched), and the key itself is not kept in the map.
func KeyID(key string) string {
	if key == "" {
		return ""
	}
	s := sha256.Sum256([]byte(key))
	return hex.EncodeToString(s[:16])
}
