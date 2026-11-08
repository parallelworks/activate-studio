package netx

import (
	"crypto/x509"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"

	"golang.org/x/crypto/x509roots/fallback/bundle"
)

// Outbound is netTrust.ts's OutboundSetup: how the server reaches out.
type Outbound struct {
	// SystemCertificates is how many of the system's trusted certificates
	// are trusted besides the bundled list, or -1 where they are not
	// (STUDIO_SYSTEM_CA=0, or the system's could not be read).
	SystemCertificates int
	// Bundled is how many certificates the bundled list has.
	Bundled int
	// Proxy requests go through, without any password, or "".
	Proxy string
	// NoProxy is the hosts that go direct.
	NoProxy string
	Error   string
}

var loopbackHosts = []string{"localhost", "127.0.0.1", "::1"}

// Bundled is the bundled list of certificate authorities, as Node bundles
// its own: Mozilla's, from golang.org/x/crypto/x509roots/fallback/bundle.
func Bundled() (*x509.CertPool, int) {
	p := x509.NewCertPool()
	n := 0
	for c := range bundle.Roots() {
		cert, err := x509.ParseCertificate(c.Certificate)
		if err != nil {
			continue
		}
		if c.Constraint == nil {
			p.AddCert(cert)
		} else {
			p.AddCertWithConstraint(cert, c.Constraint)
		}
		n++
	}
	return p, n
}

var rootsOnce sync.Once

// useRoots makes pool the roots every TLS client without its own RootCAs
// verifies against: crypto/x509's fallback roots, forced over the system's
// with x509usefallbackroots=1 for the one call that reads it. Once per
// process; the system's are read first, so the call replaces them.
func useRoots(pool *x509.CertPool) {
	rootsOnce.Do(func() {
		prev, had := os.LookupEnv("GODEBUG")
		debug := "x509usefallbackroots=1"
		if had && prev != "" {
			debug = prev + "," + debug
		}
		os.Setenv("GODEBUG", debug)
		x509.SetFallbackRoots(pool)
		if had {
			os.Setenv("GODEBUG", prev)
		} else {
			os.Unsetenv("GODEBUG")
		}
	})
}

// Setup is trustSystemCertificates and useProxyFromEnv for the Go server.
// As netTrust.ts does for Node, outbound HTTPS trusts the bundled list and
// the system's trusted certificates (crypto/x509's system pool: the system
// bundle on Linux, the platform verifier with what an administrator
// installed on macOS and Windows); STUDIO_SYSTEM_CA=0 trusts the bundled
// list only. Go's HTTP client reads HTTPS_PROXY, HTTP_PROXY and NO_PROXY on
// its own (http.ProxyFromEnvironment, which never proxies loopback); what
// is left is Node's NO_PROXY rule for the programs the server starts: with
// a proxy set, localhost, 127.0.0.1 and ::1 are added to NO_PROXY and
// no_proxy, so a child reads the same list whichever spelling it uses.
func Setup() Outbound {
	o := Outbound{SystemCertificates: -1}
	bundled, n := Bundled()
	o.Bundled = n
	if os.Getenv("STUDIO_SYSTEM_CA") == "0" {
		useRoots(bundled)
	} else if pool, err := x509.SystemCertPool(); err == nil && pool != nil {
		//lint:ignore SA1019 the count is for the log line only
		o.SystemCertificates = len(pool.Subjects()) //nolint:staticcheck
		for c := range bundle.Roots() {
			if cert, err := x509.ParseCertificate(c.Certificate); err == nil {
				if c.Constraint == nil {
					pool.AddCert(cert)
				} else {
					pool.AddCertWithConstraint(cert, c.Constraint)
				}
			}
		}
		useRoots(pool)
	} else {
		if err != nil {
			o.Error = "could not read the system's trusted certificates: " + err.Error()
		}
		useRoots(bundled)
	}
	proxy := firstEnv(os.Getenv, "HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy")
	if proxy == "" {
		return o
	}
	given := firstEnv(os.Getenv, "NO_PROXY", "no_proxy")
	have := map[string]bool{}
	for _, s := range strings.Split(given, ",") {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			have[s] = true
		}
	}
	parts := []string{}
	if g := strings.TrimSpace(given); g != "" {
		parts = append(parts, g)
	}
	for _, h := range loopbackHosts {
		if !have[h] {
			parts = append(parts, h)
		}
	}
	noProxy := strings.Join(parts, ",")
	os.Setenv("NO_PROXY", noProxy)
	os.Setenv("no_proxy", noProxy)
	o.Proxy = RedactProxy(proxy)
	o.NoProxy = noProxy
	return o
}

// RedactProxy is redactProxy: a proxy address with any password left out.
func RedactProxy(raw string) string {
	s := raw
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "(an address that does not parse)"
	}
	if _, set := u.User.Password(); set {
		u.User = url.UserPassword(u.User.Username(), "***")
	}
	return strings.TrimSuffix(strings.Replace(u.String(), "%2A%2A%2A", "***", 1), "/")
}

// Line is outboundLine: one line for the log about how the server reaches out.
func (o Outbound) Line() string {
	parts := []string{}
	if o.SystemCertificates < 0 {
		parts = append(parts, fmt.Sprintf("outbound HTTPS trusts the bundled certificate list (%d)", o.Bundled))
	} else {
		parts = append(parts, fmt.Sprintf("outbound HTTPS trusts the system's certificates (%d) as well as the bundled list (%d)", o.SystemCertificates, o.Bundled))
	}
	if o.Proxy != "" {
		parts = append(parts, fmt.Sprintf("through the proxy %s (not for %s)", o.Proxy, o.NoProxy))
	}
	if o.Error != "" {
		parts = append(parts, "("+o.Error+")")
	}
	return strings.Join(parts, ", ")
}
