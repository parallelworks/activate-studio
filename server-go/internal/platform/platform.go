// Package platform is the Studio's reach to ACTIVATE, shared by every Go
// route that acts on the platform: the pw CLI (where it is, running it with
// the environment and --context handling Node uses), the CLI's contexts and
// the choice of one (access.ts), renewing a sign-in through `pw auth token`
// (the pw-token dependency), the deployment's platform credential, the
// platform API, GitHub, and the platform's reachability. It ports
// server/src/access.ts, the pw CLI part of config.ts, platformRecords.ts,
// workspace.ts and net.ts.
//
// One Platform serves a process. Its state is the Node module state of
// those files: the chosen context (INDEX_BASE/access.json), the CLI
// location (INDEX_BASE/pw-cli.json), renewed tokens and renewal errors, and
// the reachability cache. Files are Node's, in Node's formats.
package platform

import (
	"log/slog"
	"os"
	"strings"
	"sync"
)

// Options configure a Platform.
type Options struct {
	IndexBase string
	KBRoot    string
	// LookupEnv is os.LookupEnv; tests replace it. The environment is read
	// as Node reads process.env: some values once at start, others on use.
	LookupEnv func(string) (string, bool)
	Log       *slog.Logger
	// AuthEnabled is the Go front's authEnabled(); nil when the front does
	// not verify identity (then the Node bridge says whether Node does).
	AuthEnabled func() bool
	// Home is os.UserHomeDir(); tests replace it.
	Home string
}

// Platform is one process's platform state.
type Platform struct {
	indexBase, kbRoot string
	lookup            func(string) (string, bool)
	log               *slog.Logger
	home              string
	authEnabled       func() bool

	// startupContext is PW_CONTEXT as the server started with it, which
	// following the CLI restores.
	startupContext string

	mu sync.Mutex
	// pw CLI location: findPwCli's PW_CLI and PW_CLI_SOURCE.
	cliPath   string
	cliSource string
	cliInfo   *cliInfoCache
	// The chosen context (access.json), read at start and written by Choose.
	chosen string
	// Gateway: the base URL as Node's GATEWAY_BASE, a live binding moved
	// by syncGateway.
	gatewayFromEnv bool
	gatewayBase    string
	apiKey         string // PW_API_KEY ?? OPENAI_API_KEY

	store  *storeCache
	tokens *tokenState
	reach  *reachState
	login  *loginSlot

	node     *NodeBridge
	ownsJobs bool
}

// New makes the platform state for a process: it reads the choice and the
// CLI's location as Node's modules do when they load.
func New(o Options) *Platform {
	if o.LookupEnv == nil {
		o.LookupEnv = os.LookupEnv
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir()
	}
	p := &Platform{
		indexBase: o.IndexBase, kbRoot: o.KBRoot, lookup: o.LookupEnv, log: o.Log, home: o.Home,
		authEnabled: o.AuthEnabled,
		store:       &storeCache{}, tokens: newTokenState(), reach: &reachState{}, login: &loginSlot{},
	}
	p.startupContext = p.Getenv("PW_CONTEXT")
	if v, ok := p.lookupFirst("PW_API_KEY", "OPENAI_API_KEY"); ok {
		p.apiKey = v
	}
	p.initGateway()
	p.FindCLI()
	p.chosen = p.loadChoice()
	p.syncGateway()
	return p
}

// Getenv is process.env[k] || ”.
func (p *Platform) Getenv(k string) string {
	v, _ := p.lookup(k)
	return v
}

// lookupFirst is `env.a ?? env.b`: the first that is set, even to "".
func (p *Platform) lookupFirst(keys ...string) (string, bool) {
	for _, k := range keys {
		if v, ok := p.lookup(k); ok {
			return v, true
		}
	}
	return "", false
}

// IndexBase is INDEX_BASE.
func (p *Platform) IndexBase() string { return p.indexBase }

// KBRoot is KB_ROOT.
func (p *Platform) KBRoot() string { return p.kbRoot }

// Log is the platform's logger.
func (p *Platform) Log() *slog.Logger { return p.log }

// SetNode connects the Node bridge (nil without a Node server).
func (p *Platform) SetNode(n *NodeBridge) { p.mu.Lock(); p.node = n; p.mu.Unlock() }

// Node is the Node bridge, or nil.
func (p *Platform) Node() *NodeBridge { p.mu.Lock(); defer p.mu.Unlock(); return p.node }

// PWContext is what Node's process.env.PW_CONTEXT holds: the chosen
// context, else the one the server started with, else unset (applyEnv).
func (p *Platform) PWContext() string {
	if c := p.ChosenContext(); c != "" {
		return c
	}
	return p.startupContext
}

// Environ is the environment a child process gets: this process's, with
// PW_CONTEXT as Node's applyEnv leaves it, and over that set (a value of
// "" removes the variable, as nodeproc.Environ).
func (p *Platform) Environ(set map[string]string) []string {
	over := map[string]string{"PW_CONTEXT": p.PWContext()}
	for k, v := range set {
		over[k] = v
	}
	base := os.Environ()
	out := make([]string, 0, len(base)+len(over))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := over[k]; ok {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range over {
		if v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}
