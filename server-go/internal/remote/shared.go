package remote

import (
	"sync"

	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
)

var (
	sharedMu sync.Mutex
	shared   = map[any]*sharedPool{}
)

type sharedPool struct {
	pool    *Pool
	runners *Runners
}

// Shared is owner's one pool and the GUFI runners over it, made with
// options() on first use. Every package that reaches other systems for the
// same server (libraries, search, user libraries) asks for it with the same
// owner, the App, so their sessions, the hosts marked to run only
// gufi_query and the hosts reached are one state, as remoteExec.ts's are
// one module.
func Shared(owner any, options func() Options) (*Pool, *Runners) {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if s := shared[owner]; s != nil {
		return s.pool, s.runners
	}
	p := New(options())
	s := &sharedPool{pool: p, runners: &Runners{Pool: p, Options: gufi.RunnerOptions{}}}
	shared[owner] = s
	return s.pool, s.runners
}
