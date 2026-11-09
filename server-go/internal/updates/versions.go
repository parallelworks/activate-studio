package updates

import (
	"regexp"
	"strconv"
	"strings"
)

var versionPrefix = regexp.MustCompile(`^v?(\d+)\.(\d+)(?:\.(\d+))?`)

// ParseVersion is vMAJOR.MINOR[.PATCH]; a git describe or other suffix is
// ignored, so v1.137-6-g3140181 is v1.137. ok is false for anything else.
func ParseVersion(v string) (out [3]int, ok bool) {
	m := versionPrefix.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return out, false
	}
	for i := 0; i < 3; i++ {
		if m[i+1] != "" {
			n, err := strconv.Atoi(m[i+1])
			if err != nil {
				return out, false
			}
			out[i] = n
		}
	}
	return out, true
}

// CompareVersions is negative, zero or positive as a is older, the same, or
// newer than b; a string that is no version counts as 0.0.0.
func CompareVersions(a, b string) int {
	x, _ := ParseVersion(a)
	y, _ := ParseVersion(b)
	for i := 0; i < 3; i++ {
		if x[i] != y[i] {
			return x[i] - y[i]
		}
	}
	return 0
}

// IsNewer: candidate is a release newer than running. A running build of
// no known version (dev) counts as older than any release.
func IsNewer(candidate, running string) bool {
	_, ok := ParseVersion(candidate)
	return ok && CompareVersions(candidate, running) > 0
}

// LastNodeRelease is the newest release that runs the Node server. The
// Go version installs only releases after it (the first Preview of the Go
// version is v1.188.1, the first Stable v1.189): it never goes back to Node
// by choice. Releases carry no mark of their server in the signed manifest,
// so the version is the mark. The run loop still puts the copy an update
// replaced back, Node's included, when the new one keeps failing to start.
const LastNodeRelease = "v1.188"

// goRelease: tag is a release of the Go version, one this updater may install.
func (u *Updater) goRelease(tag string) bool {
	return IsNewer(tag, u.o.LastNodeRelease)
}

func sameVersion(a, b string) bool {
	_, ok := ParseVersion(a)
	return ok && CompareVersions(a, b) == 0
}

// dottedAtMost: the system version have is at least need.
func dottedAtMost(need, have string) bool {
	num := func(s string) []float64 {
		var out []float64
		for _, p := range strings.Split(s, ".") {
			f, err := strconv.ParseFloat(p, 64)
			if err != nil {
				f = 0 // Number('x') is NaN, which || 0 makes 0
			}
			out = append(out, f)
		}
		return out
	}
	a, b := num(need), num(have)
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		var x, y float64
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x < y
		}
	}
	return true
}
