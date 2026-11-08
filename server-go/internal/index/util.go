package index

import "time"

func remove(xs []int, x int) []int {
	for i, v := range xs {
		if v == x {
			return append(xs[:i], xs[i+1:]...)
		}
	}
	return xs
}

func isoNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// truncRunes is String(x).slice(0, n), by code points.
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
