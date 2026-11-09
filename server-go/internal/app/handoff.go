package app

import (
	"context"
	"net/http"
)

type handOffKey struct{}

// HandOff marks r as a request a Go route passes to Node on purpose, for a
// case whose owner is not ported yet. The proxy sends a marked request on
// with InternalHeader, so the test harness's strict mode tells it from a
// route that reaches Node by accident; a client's own InternalHeader is
// still dropped. Every hand-off is a temporary gap: each call site names
// the package that removes it.
func HandOff(r *http.Request, why string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), handOffKey{}, why))
}

// IsHandOff reports whether r was marked by HandOff, and why.
func IsHandOff(r *http.Request) (string, bool) {
	why, ok := r.Context().Value(handOffKey{}).(string)
	return why, ok
}
