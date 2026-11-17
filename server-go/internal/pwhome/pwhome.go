// Package pwhome keeps the pw CLI of one person apart from another's.
//
// The CLI keeps its ssh key in ~/.ssh/pwcli and registers it with the
// account it acts for. On a Studio several people use, every pw ssh run with
// a person's credential would otherwise share the service account's one key,
// and that key would be added to each person's platform account. The CLI
// takes its home from $HOME, so a run with a person's credential gets a home
// folder of that person's own.
package pwhome

import "sync"

var (
	mu sync.RWMutex
	of func(key string) string
)

// Set installs the lookup: the home folder for a credential, "" for one
// that is not a person's (the deployment's own, or one not known).
func Set(f func(key string) string) {
	mu.Lock()
	of = f
	mu.Unlock()
}

// Of is the home folder the CLI runs in with key, "" for the account's own.
func Of(key string) string {
	if key == "" {
		return ""
	}
	mu.RLock()
	f := of
	mu.RUnlock()
	if f == nil {
		return ""
	}
	return f(key)
}

// Env is HOME for a run with key, as an environment entry, or nothing.
func Env(key string) []string {
	if h := Of(key); h != "" {
		return []string{"HOME=" + h}
	}
	return nil
}
