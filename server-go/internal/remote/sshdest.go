package remote

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

var destPort = regexp.MustCompile(`^(.+):(\d{1,5})$`)

func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return fmt.Sprint(x)
	}
}

// SSHDest is userLibraries.ts sshDest: a destination, port and options
// from a request, checked.
func SSHDest(destination, port, sshOptions any) (string, *int, []string, error) {
	dest := strings.TrimSpace(jsString(destination))
	given := port
	empty := func(v any) bool { return v == nil || v == "" }
	if m := destPort.FindStringSubmatch(dest); m != nil && empty(given) {
		dest, given = m[1], m[2]
	}
	if !SSHDestination.MatchString(dest) {
		return "", nil, nil, apierr.New(400, "destination: user@host, or a Host alias from your ~/.ssh/config, in letters, digits, dots, dashes, and underscores (for an IPv6 address, use an alias)")
	}
	var p *int
	if !empty(given) {
		var n float64
		switch x := given.(type) {
		case float64:
			n = x
		case bool:
			n = map[bool]float64{true: 1, false: 0}[x]
		default:
			s := strings.TrimSpace(jsString(x))
			f, err := strconv.ParseFloat(s, 64)
			if s == "" {
				f, err = 0, nil
			}
			if err != nil {
				f = math.NaN()
			}
			n = f
		}
		if n != math.Trunc(n) || n < 1 || n > 65535 {
			return "", nil, nil, apierr.New(400, "port: a number from 1 to 65535")
		}
		v := int(n)
		p = &v
	}
	var opts []string
	var err error
	if arr, ok := sshOptions.([]any); ok {
		words := make([]string, len(arr))
		for i, w := range arr {
			words[i] = jsString(w)
		}
		opts, err = CheckSSHOptions(words)
	} else {
		opts, err = ParseSSHOptions(jsString(sshOptions))
	}
	if err != nil {
		return "", nil, nil, err
	}
	pp := 0
	if p != nil {
		pp = *p
	}
	if _, err := SSHSite(dest, pp, opts); err != nil {
		return "", nil, nil, err
	}
	return dest, p, opts, nil
}
