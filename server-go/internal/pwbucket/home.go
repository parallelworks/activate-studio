package pwbucket

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// The HOME a pw run is given. pw keeps the bucket's credential under it, so
// it is open to this account alone, in memory where the system has such a
// folder, locked while in use and removed when the last run ends. A Studio
// that was cut off leaves its own behind, not locked: Sweep removes those.

// homePrefix starts a HOME's name. Nothing else of the Studio's is named so:
// platform's studio-pw-no-sign-in, which Studios share, is not to be swept.
const homePrefix = "studio-pw-run-"

// tmpBase is where a run's HOME is made: a folder kept in memory where
// there is one (Linux's /dev/shm), else the system's own for temporary
// files ("").
var tmpBase = func() string {
	if runtime.GOOS == "linux" {
		if st, err := os.Stat("/dev/shm"); err == nil && st.IsDir() {
			return "/dev/shm"
		}
	}
	return ""
}

// privateHome makes a HOME for pw; remove ends it.
func privateHome() (dir string, remove func(), err error) {
	for _, base := range []string{tmpBase(), ""} {
		// Another Studio's Sweep may take one before it is locked.
		for range 5 {
			if dir, err = os.MkdirTemp(base, homePrefix); err != nil {
				break
			}
			if l, _ := lockDir(dir); l != nil {
				return dir, func() { removeLocked(dir, l) }, nil
			}
		}
		if base == "" {
			break
		}
	}
	if err == nil {
		err = errors.New("no private folder for the pw CLI could be made")
	}
	return "", nil, err
}

// Sweep removes the HOMEs of this account that a Studio cut off left behind,
// and says how many. One in use, by this Studio or another, is locked and
// stays.
func Sweep() int {
	n := 0
	seen := map[string]bool{}
	for _, base := range []string{tmpBase(), os.TempDir()} {
		if base == "" || seen[base] {
			continue
		}
		seen[base] = true
		list, _ := os.ReadDir(base)
		for _, e := range list {
			dir := filepath.Join(base, e.Name())
			if !strings.HasPrefix(e.Name(), homePrefix) || !ownDir(dir) {
				continue
			}
			l, err := lockDir(dir)
			if err == nil && removeLocked(dir, l) == nil {
				n++
			} else if l != nil {
				l.Close()
			}
		}
	}
	return n
}
