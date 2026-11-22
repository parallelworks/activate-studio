package fleet

import (
	"net/http"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/mainlib"
)

// mayUse says whether the person r is from may see agent a and act on it:
// anyone on a Studio without sign-in, which one person uses; with sign-in,
// the person who made it and the Studio's administrators. An agent kept
// without an owner is its administrators' alone. An agent wakes with its
// owner's sign-in, so nobody else may read what it found, wake it or
// write to it.
func (f *Fleet) mayUse(r *http.Request, a *Agent) bool {
	if a == nil {
		return false
	}
	if !f.A.Cfg.AuthEnabled() {
		return true
	}
	u := auth.UserOf(r)
	if u == nil || u.ID == "" {
		return false
	}
	if a.Owner != nil && *a.Owner == u.ID {
		return true
	}
	return mainlib.Admin(mainlib.Of(f.A, r))
}

// Shown is a as the routes answer it: without the record of the person it
// acts for (their groups and sign-in claims), which stays on the server.
func Shown(a Agent) Agent {
	a.OwnerUser = nil
	return a
}

// For is the agents the person r is from may see, newest first.
func (f *Fleet) For(r *http.Request) []Agent {
	out := []Agent{}
	for _, a := range f.List() {
		if f.mayUse(r, &a) {
			out = append(out, Shown(a))
		}
	}
	return out
}

// GetFor is agent id when the person r is from may see it, else nil.
func (f *Fleet) GetFor(r *http.Request, id string) *Agent {
	a := f.Get(id)
	if !f.mayUse(r, a) {
		return nil
	}
	s := Shown(*a)
	return &s
}
