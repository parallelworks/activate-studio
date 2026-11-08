package userlibs

import (
	"regexp"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// A request's fields, checked as userLibraries.ts checks them.

var (
	controlChars = regexp.MustCompile(`[\x00-\x1f\x7f]`)
	rootedRe     = regexp.MustCompile(`^(?:/|~(?:/|$))`)
	trailingRe   = regexp.MustCompile(`/+$`)
	destPortRe   = regexp.MustCompile(`^(.+):(\d{1,5})$`)
	// clusterRe is a cluster's name as pw ssh takes it: nothing it would
	// read as an option.
	clusterRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,119}$`)
)

// remotePath is a path on the remote host: absolute, or under ~, which the
// test resolves there; nil for none.
func remotePath(v any, what string) (*string, error) {
	s := str(v)
	if s == "" {
		return nil, nil
	}
	if units(s) > 1024 || controlChars.MatchString(s) || !rootedRe.MatchString(s) || containsDotDot(s) {
		return nil, apierr.New(400, "%s: an absolute path on that host, or one under ~", what)
	}
	if units(s) > 1 {
		s = trailingRe.ReplaceAllString(s, "")
	}
	return &s, nil
}

func containsDotDot(s string) bool {
	for _, p := range strings.Split(s, "/") {
		if p == ".." {
			return true
		}
	}
	return false
}

// sshDestSpec is sshDest's answer: the host a request names.
type sshDestSpec struct {
	Destination string
	Port        int // 0: none
	SSHOptions  []string
}

// sshDest is the host a request names: user@host or an alias, with a port
// given apart or written after it (user@host:2222).
func sshDest(destination, port, options any) (sshDestSpec, error) {
	dest := str(destination)
	given := port
	if m := destPortRe.FindStringSubmatch(dest); m != nil && (given == "" || given == nil) {
		dest, given = m[1], m[2]
	}
	if !remote.SSHDestination.MatchString(dest) {
		return sshDestSpec{}, apierr.New(400, "destination: user@host, or a Host alias from your ~/.ssh/config, in letters, digits, dots, dashes, and underscores (for an IPv6 address, use an alias)")
	}
	p := 0
	if given != "" && given != nil {
		n := number(given)
		if !(isInt(n) && n >= 1 && n <= 65535) {
			return sshDestSpec{}, apierr.New(400, "port: a number from 1 to 65535")
		}
		p = int(n)
	}
	var opts []string
	var err error
	if arr, ok := options.([]any); ok {
		words := make([]string, len(arr))
		for i, w := range arr {
			words[i] = jsString(w)
		}
		opts, err = remote.CheckSSHOptions(words)
	} else {
		opts, err = remote.ParseSSHOptions(jsString(options))
	}
	if err != nil {
		return sshDestSpec{}, err
	}
	if opts == nil {
		opts = []string{}
	}
	// The port given twice is refused here.
	if _, err := remote.SSHSite(dest, p, opts); err != nil {
		return sshDestSpec{}, err
	}
	return sshDestSpec{dest, p, opts}, nil
}

// jsString is String(v) where v may be undefined: "undefined" stays out,
// since every caller writes ?? ” or maps array members that are values.
func jsString(v any) string { return jsonv.String(v) }

// sshSpec is a request's fields for a library over ssh.
type sshSpec struct {
	sshDestSpec
	IndexRoot  string
	GufiBin    *string
	SourceRoot *string
}

func newSSHSpec(destination, port, options, indexRoot, gufiBin, sourceRoot any) (sshSpec, error) {
	d, err := sshDest(destination, port, options)
	if err != nil {
		return sshSpec{}, err
	}
	idx, err := remotePath(indexRoot, "index directory")
	if err != nil {
		return sshSpec{}, err
	}
	if idx == nil || *idx == "" {
		return sshSpec{}, apierr.New(400, "index directory: the top of the GUFI index on that host, the directory with its db.db")
	}
	bin, err := remotePath(gufiBin, "GUFI bin directory")
	if err != nil {
		return sshSpec{}, err
	}
	src, err := remotePath(sourceRoot, "source directory")
	if err != nil {
		return sshSpec{}, err
	}
	return sshSpec{d, *idx, bin, src}, nil
}

func sshSpecOf(b *jsonv.Object) (sshSpec, error) {
	return newSSHSpec(field(b, "destination"), field(b, "port"), field(b, "sshOptions"), field(b, "indexRoot"), field(b, "gufiBin"), field(b, "sourceRoot"))
}

// pwSpec is a request's fields for an existing index on a cluster.
type pwSpec struct {
	Cluster    string
	IndexRoot  string
	GufiBin    *string
	SourceRoot *string
}

func newPwSpec(cluster, indexRoot, gufiBin, sourceRoot any) (pwSpec, error) {
	c := str(cluster)
	if !clusterRe.MatchString(c) {
		return pwSpec{}, apierr.New(400, "cluster: the name of one of your running clusters")
	}
	idx, err := remotePath(indexRoot, "index directory")
	if err != nil {
		return pwSpec{}, err
	}
	if idx == nil || *idx == "" {
		return pwSpec{}, apierr.New(400, "index directory: the top of the GUFI index on that cluster, the directory with its db.db")
	}
	bin, err := remotePath(gufiBin, "GUFI bin directory")
	if err != nil {
		return pwSpec{}, err
	}
	src, err := remotePath(sourceRoot, "source directory")
	if err != nil {
		return pwSpec{}, err
	}
	return pwSpec{c, *idx, bin, src}, nil
}

func pwSpecOf(b *jsonv.Object) (pwSpec, error) {
	return newPwSpec(field(b, "cluster"), field(b, "indexRoot"), field(b, "gufiBin"), field(b, "sourceRoot"))
}

// optional turns a stored optional string back into a request field.
func optional(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
