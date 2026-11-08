package libraries

import (
	"log/slog"
	"regexp"
	"strings"
	"sync"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

var ctlChars = regexp.MustCompile(`[\x00-\x1f\x7f]`)
var slashes = regexp.MustCompile(`/+`)

// ReadThroughRoot is readThroughRoot: an absolute folder on a host, with
// no ".." and no control characters; nil for none.
func ReadThroughRoot(v string) (*string, error) {
	s := strings.TrimSpace(v)
	if s == "" {
		return nil, nil
	}
	if len(utf16Units(s)) > 1024 || ctlChars.MatchString(s) || !strings.HasPrefix(s, "/") || containsSeg(s, "..") {
		return nil, apierr.New(400, `folder: an absolute path on that host, with no ".."`)
	}
	c := slashes.ReplaceAllString(s, "/")
	if len(c) > 1 {
		c = strings.TrimSuffix(c, "/")
	}
	return &c, nil
}

func containsSeg(p, seg string) bool {
	for _, x := range strings.Split(p, "/") {
		if x == seg {
			return true
		}
	}
	return false
}

// DestFn checks a destination, port and options as userLibraries.ts's
// sshDest does; set by the server from internal/remote.
type DestFn func(destination any, port any, sshOptions any) (string, *int, []string, error)

// CheckDest is the ssh destination check stored read-through settings
// pass again; nil skips it (tests of this package alone).
var CheckDest DestFn

// ParseReadThrough is parseReadThrough: nil when no destination is given.
func ParseReadThrough(b map[string]any) (*ReadThrough, error) {
	dest, _ := b["destination"].(string)
	if strings.TrimSpace(anyString(b["destination"])) == "" {
		return nil, nil
	}
	rt := &ReadThrough{Destination: dest}
	if CheckDest != nil {
		d, port, opts, err := CheckDest(b["destination"], b["port"], b["sshOptions"])
		if err != nil {
			return nil, err
		}
		rt.Destination, rt.Port = d, port
		if len(opts) > 0 {
			rt.SSHOptions = opts
		}
	}
	root, err := ReadThroughRoot(anyString(b["root"]))
	if err != nil {
		return nil, err
	}
	rt.Root = root
	return rt, nil
}

func anyString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	}
	return jsString(v)
}

var warnedRT sync.Map

// StoredReadThrough is storedReadThrough: a stored setting checked again,
// nil (with a note in the log once) when it would not pass.
func StoredReadThrough(rt *ReadThrough, library string, log *slog.Logger) *ReadThrough {
	if rt == nil {
		return nil
	}
	m := map[string]any{"destination": rt.Destination, "root": nil}
	if rt.Port != nil {
		m["port"] = float64(*rt.Port)
	}
	if rt.SSHOptions != nil {
		opts := make([]any, len(rt.SSHOptions))
		for i, o := range rt.SSHOptions {
			opts[i] = o
		}
		m["sshOptions"] = opts
	}
	if rt.Root != nil {
		m["root"] = *rt.Root
	}
	out, err := ParseReadThrough(m)
	if err != nil {
		note := library + ": readThrough ignored: " + err.Error()
		if _, seen := warnedRT.LoadOrStore(note, true); !seen && log != nil {
			log.Warn(note)
		}
		return nil
	}
	return out
}
