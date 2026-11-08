// Package bridge is the transitional channel between the Go server and the
// Node server it fronts while both run, during the port. Ported modules use it
// for the few calls that cross between the two (Node asking Go for state Go
// now owns, Go asking Node for state Node still owns), and to tell a Node it
// spawns which background work Go has taken over. It goes away with Node: the
// release runs with no Node (STUDIO_NODE=off) and nothing here is used.
//
// One secret guards every bridge call in both directions. The Go server makes
// it at start, or takes STUDIO_BRIDGE_SECRET when set (the test harness sets
// it), and hands it to the Node it spawns. Paths under a registered private
// prefix are refused to any request without the secret, so a browser can
// never reach them through the front.
package bridge

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
)

// Header carries the secret on every bridge request.
const Header = "X-Studio-Bridge-Secret"

var (
	mu       sync.Mutex
	secret   string
	envFuncs = map[string]EnvFunc{}
	prefixes []string
)

// Secret returns the process's bridge secret, made on first use.
func Secret() string {
	mu.Lock()
	defer mu.Unlock()
	if secret == "" {
		if s := os.Getenv("STUDIO_BRIDGE_SECRET"); s != "" {
			secret = s
		} else {
			b := make([]byte, 32)
			if _, err := rand.Read(b); err != nil {
				panic("bridge: no randomness: " + err.Error())
			}
			secret = hex.EncodeToString(b)
		}
	}
	return secret
}

// An EnvFunc gives the environment a ported module wants a spawned Node to
// have, given the Go server's own loopback URL.
type EnvFunc func(frontURL string) map[string]string

// RegisterNodeEnv adds a module's environment for the Node the Go server
// spawns. Call it from the module's init or Register, before Node starts.
func RegisterNodeEnv(name string, f EnvFunc) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := envFuncs[name]; dup {
		panic("bridge: node env registered twice: " + name)
	}
	envFuncs[name] = f
}

// NodeEnv is the environment for a spawned Node: the secret, the Go server's
// URL, and every registered module's variables, merged into base (which wins
// on a clash, so identity settings cannot be overridden by a module).
func NodeEnv(base map[string]string, frontURL string) map[string]string {
	out := map[string]string{}
	mu.Lock()
	names := slices.Sorted(maps.Keys(envFuncs))
	funcs := make([]EnvFunc, 0, len(names))
	for _, n := range names {
		funcs = append(funcs, envFuncs[n])
	}
	mu.Unlock()
	for _, f := range funcs {
		maps.Copy(out, f(frontURL))
	}
	out["STUDIO_BRIDGE_SECRET"] = Secret()
	out["STUDIO_FRONT_URL"] = frontURL
	maps.Copy(out, base)
	return out
}

// RegisterPrivatePrefix marks a path prefix as bridge only.
func RegisterPrivatePrefix(p string) {
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(prefixes, p) {
		prefixes = append(prefixes, p)
	}
}

// IsPrivate reports whether path is under a bridge-only prefix.
func IsPrivate(path string) bool {
	mu.Lock()
	defer mu.Unlock()
	for _, p := range prefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// Authorized reports whether r carries the bridge secret.
func Authorized(r *http.Request) bool {
	got := r.Header.Get(Header)
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(Secret())) == 1
}

// Sign adds the secret to an outgoing bridge request.
func Sign(r *http.Request) { r.Header.Set(Header, Secret()) }
