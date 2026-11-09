package app

import (
	"net/http"
	"sync"
)

// ToNodeHook adjusts a request the Go server passes to Node (proxied, or
// one of its own value calls): in is the request Go received, out the one
// Node gets. A track that owns state Node still reads per request sends it
// this way (the chat track's model keys).
type ToNodeHook func(in, out *http.Request)

var (
	toNodeMu    sync.RWMutex
	toNodeHooks []ToNodeHook
)

// OnToNode adds a hook run on every request passed to Node.
func OnToNode(h ToNodeHook) {
	toNodeMu.Lock()
	toNodeHooks = append(toNodeHooks, h)
	toNodeMu.Unlock()
}

// RunToNode runs the hooks for a request passed to Node.
func RunToNode(in, out *http.Request) {
	toNodeMu.RLock()
	hs := toNodeHooks
	toNodeMu.RUnlock()
	for _, h := range hs {
		h(in, out)
	}
}
