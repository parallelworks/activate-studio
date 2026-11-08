package netx

import (
	"crypto/x509"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Outbound is netTrust.ts's OutboundSetup: how the server reaches out.
type Outbound struct {
	// SystemCertificates: Go's TLS verifies against the system's trusted
	// certificates (crypto/x509's system pool), which is what netTrust.ts
	// adds to Node's own list. -1 where the pool could not be read.
	SystemCertificates int
	// Proxy requests go through, without any password, or "".
	Proxy string
	// NoProxy is the hosts that go direct.
	NoProxy string
	Error   string
}

var loopbackHosts = []string{"localhost", "127.0.0.1", "::1"}

// Setup is trustSystemCertificates and useProxyFromEnv for the Go server.
// Go's HTTP client uses the system's certificates and HTTPS_PROXY,
// HTTP_PROXY and NO_PROXY on its own (http.ProxyFromEnvironment, which
// never proxies loopback); what is left is Node's NO_PROXY rule for the
// programs the server starts: with a proxy set, localhost, 127.0.0.1 and
// ::1 are added to NO_PROXY and no_proxy, so a child reads the same list
// whichever spelling it uses. STUDIO_SYSTEM_CA=0 has no Go equivalent
// (there is no bundled list to keep to) and is reported as such.
func Setup() Outbound {
	o := Outbound{SystemCertificates: -1}
	if os.Getenv("STUDIO_SYSTEM_CA") == "0" {
		o.Error = "STUDIO_SYSTEM_CA=0 is not followed by the Go server, which always uses the system's certificates"
	}
	if pool, err := x509.SystemCertPool(); err == nil && pool != nil {
		//lint:ignore SA1019 the count is for the log line only
		o.SystemCertificates = len(pool.Subjects()) //nolint:staticcheck
	} else if o.Error == "" && err != nil {
		o.Error = "could not read the system's trusted certificates: " + err.Error()
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
		parts = append(parts, "outbound HTTPS trusts the system's certificates")
	} else {
		parts = append(parts, fmt.Sprintf("outbound HTTPS trusts the system's certificates (%d)", o.SystemCertificates))
	}
	if o.Proxy != "" {
		parts = append(parts, fmt.Sprintf("through the proxy %s (not for %s)", o.Proxy, o.NoProxy))
	}
	if o.Error != "" {
		parts = append(parts, "("+o.Error+")")
	}
	return strings.Join(parts, ", ")
}
