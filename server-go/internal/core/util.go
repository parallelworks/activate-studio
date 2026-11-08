package core

import (
	"context"
	"net/http"
	"sort"
)

func sortStable[T any](s []T, less func(a, b T) bool) {
	sort.SliceStable(s, func(i, j int) bool { return less(s[i], s[j]) })
}

// contextWithoutCancel is the request's context without its end, for work
// that goes on after the answer (main.ts's fire-and-forget calls).
func contextWithoutCancel(r *http.Request) context.Context {
	return context.WithoutCancel(r.Context())
}
