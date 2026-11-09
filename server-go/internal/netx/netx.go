// Package netx is net.ts and netTrust.ts: telling a network failure from
// every other failure, the sentences a person reads about one, and how the
// server reaches out (system certificates, the proxy).
//
// A Studio on a laptop is often offline, and everything that works from
// local files has to keep working then. What breaks is anything that calls
// out: the update check, the model gateway, attached MCP servers, the
// platform. Each of those reports a missing network in plain words, as a
// state rather than an error, and none of them waits long for it.
//
// Go's errors carry what Node's carry as codes: a *net.DNSError for a name
// that does not resolve, syscall errors (ECONNREFUSED, ...), net.Error
// timeouts, x509 errors for certificates. NetworkFailure, TrustFailure and
// ProxyFailure map them onto Node's codes, so the messages are the same.
package netx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"syscall"
)

var networkCodes = map[string]bool{
	"ENOTFOUND": true, "EAI_AGAIN": true, "EAI_FAIL": true, "EAI_NODATA": true, "ECONNRESET": true, "ECONNREFUSED": true, "ECONNABORTED": true,
	"ETIMEDOUT": true, "ENETUNREACH": true, "ENETDOWN": true, "EHOSTUNREACH": true, "EHOSTDOWN": true, "EPIPE": true,
	"UND_ERR_CONNECT_TIMEOUT": true, "UND_ERR_HEADERS_TIMEOUT": true, "UND_ERR_BODY_TIMEOUT": true, "UND_ERR_SOCKET": true,
}

// cliNetwork is the pw CLI's wording for the same causes (Go's net
// package and its HTTP client).
var cliNetwork = regexp.MustCompile(`(?i)no such host|connection refused|connection reset|network is unreachable|host is unreachable|i/o timeout|context deadline exceeded|TLS handshake timeout|dial tcp|server misbehaving|temporary failure in name resolution`)

// NetworkFailure is net.ts's NetworkFailure.
type NetworkFailure struct {
	// Code identified it: ENOTFOUND, ECONNRESET, timeout, ...
	Code string
	// NoDNS: the name lookup failed, which is what no network at all looks like.
	NoDNS bool
}

var errnoCodes = map[syscall.Errno]string{
	syscall.ECONNRESET: "ECONNRESET", syscall.ECONNREFUSED: "ECONNREFUSED", syscall.ECONNABORTED: "ECONNABORTED",
	syscall.ETIMEDOUT: "ETIMEDOUT", syscall.ENETUNREACH: "ENETUNREACH", syscall.ENETDOWN: "ENETDOWN",
	syscall.EHOSTUNREACH: "EHOSTUNREACH", syscall.EPIPE: "EPIPE",
}

var codeInText = regexp.MustCompile(`\b(E[A-Z_]{4,}|UND_ERR_[A-Z_]+)\b`)

// Code is the Node-style code of err's network cause, or "".
func Code(err error) string {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		if dns.IsTemporary || dns.IsTimeout {
			return "EAI_AGAIN"
		}
		return "ENOTFOUND"
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if c, ok := errnoCodes[errno]; ok {
			return c
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return "timeout"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	if m := codeInText.FindStringSubmatch(err.Error()); m != nil && networkCodes[m[1]] {
		return m[1]
	}
	return ""
}

// Failure is networkFailure: the network failure behind err, or nil for
// any other kind of error.
func Failure(err error) *NetworkFailure {
	if err == nil {
		return nil
	}
	c := Code(err)
	if c == "" {
		return nil
	}
	return &NetworkFailure{Code: c, NoDNS: strings.HasPrefix(c, "EAI_") || c == "ENOTFOUND"}
}

// CLINetworkFailure is cliNetworkFailure: whether a pw CLI failure, given
// as its output, is the network rather than the request.
func CLINetworkFailure(text string) bool { return cliNetwork.MatchString(text) }

var portSuffix = regexp.MustCompile(`:\d+$`)
var loopback127 = regexp.MustCompile(`^127\.`)

func hostName(host string) string {
	name := portSuffix.ReplaceAllString(host, "")
	name = strings.TrimSuffix(strings.TrimPrefix(name, "["), "]")
	return strings.ToLower(name)
}

// IsLoopbackHost is isLoopbackHost: a host on this computer, where a
// refused connection means the server is not running.
func IsLoopbackHost(host string) bool {
	name := hostName(host)
	return name == "localhost" || name == "::1" || loopback127.MatchString(name)
}

// UnreachableMessage is unreachableMessage.
func UnreachableMessage(host string, f *NetworkFailure) string {
	why := ""
	if f != nil {
		if f.Code == "timeout" {
			why = " (no answer in time)"
		} else {
			why = " (" + f.Code + ")"
		}
	}
	if IsLoopbackHost(host) {
		return fmt.Sprintf("Nothing answers at %s%s. Start the model server on this computer, or check its address.", host, why)
	}
	if host == "" {
		host = "the server"
	}
	return fmt.Sprintf("Could not reach %s%s. This computer may be offline, or the server is down. Local libraries and a local model keep working.", host, why)
}

// TrustFailure is net.ts's TrustFailure.
type TrustFailure struct {
	Code   string
	Kind   string // untrusted, expired or name
	Reason string
}

// Trust is trustFailure: the certificate failure behind err, or nil.
func Trust(err error) *TrustFailure {
	if err == nil {
		return nil
	}
	var unknown x509.UnknownAuthorityError
	if errors.As(err, &unknown) {
		return &TrustFailure{Code: "UNABLE_TO_GET_ISSUER_CERT_LOCALLY", Kind: "untrusted", Reason: "unable to get local issuer certificate"}
	}
	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) {
		if invalid.Reason == x509.Expired {
			return &TrustFailure{Code: "CERT_HAS_EXPIRED", Kind: "expired", Reason: "certificate has expired"}
		}
		return &TrustFailure{Code: "CERT_UNTRUSTED", Kind: "untrusted", Reason: invalid.Error()}
	}
	var host x509.HostnameError
	if errors.As(err, &host) {
		return &TrustFailure{Code: "ERR_TLS_CERT_ALTNAME_INVALID", Kind: "name", Reason: host.Error()}
	}
	var verify *tls.CertificateVerificationError
	if errors.As(err, &verify) {
		return &TrustFailure{Code: "CERT_UNTRUSTED", Kind: "untrusted", Reason: verify.Err.Error()}
	}
	return nil
}

// TrustMessage is trustMessage.
func TrustMessage(host string, f *TrustFailure) string {
	where := host
	if where == "" {
		where = "the server"
	}
	switch f.Kind {
	case "expired":
		return fmt.Sprintf("The certificate %s presented is out of date (%s). Check this computer's date and time; if they are right, the certificate needs renewing.", where, f.Reason)
	case "name":
		return fmt.Sprintf("The certificate %s presented is for a different name (%s), so the connection was refused. A network that inspects HTTPS can cause this.", where, f.Reason)
	}
	trust := "in this computer's trusted certificates (the system store, or a file named by NODE_EXTRA_CA_CERTS)"
	switch runtime.GOOS {
	case "darwin":
		trust = "trusted by macOS (in Keychain Access, or installed by the Mac's management)"
	case "windows":
		trust = "trusted by Windows"
	}
	return fmt.Sprintf("The connection to %s used a certificate this computer does not trust (%s). If your network inspects HTTPS, its certificate authority has to be %s; the Studio uses the system's trusted certificates.", where, f.Reason, trust)
}

// Getenv is os.Getenv; tests pass a map's lookup.
type Getenv func(string) string

func firstEnv(env Getenv, keys ...string) string {
	for _, k := range keys {
		if v := env(k); v != "" {
			return v
		}
	}
	return ""
}

// ProxyFor is proxyFor: the proxy the environment names for a request to
// host, or nil (none, or the host is excepted by NO_PROXY).
func ProxyFor(host string, env Getenv) *url.URL {
	if env == nil {
		env = os.Getenv
	}
	raw := firstEnv(env, "HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy")
	if raw == "" {
		return nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	proxy, err := url.Parse(raw)
	if err != nil || proxy.Host == "" {
		return nil
	}
	name := hostName(host)
	noProxy := firstEnv(env, "NO_PROXY", "no_proxy")
	for _, entry := range strings.Split(noProxy, ",") {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		if entry == "*" {
			return nil
		}
		bare := portSuffix.ReplaceAllString(strings.TrimPrefix(strings.TrimPrefix(entry, "*"), "."), "")
		if name == bare || strings.HasSuffix(name, "."+bare) {
			return nil
		}
	}
	return proxy
}

// ProxyFailure is net.ts's ProxyFailure.
type ProxyFailure struct {
	Proxy  string // host:port, without any password
	Code   string // ENOTFOUND, ECONNREFUSED, ... or "refused"
	Status int    // the proxy's answer to the tunnel request, when it refused one
}

var proxyConnectCodes = map[string]bool{"ENOTFOUND": true, "EAI_AGAIN": true, "EAI_FAIL": true, "ECONNREFUSED": true, "ETIMEDOUT": true, "EHOSTUNREACH": true, "ENETUNREACH": true, "UND_ERR_CONNECT_TIMEOUT": true, "timeout": true}

// Go's transport reports a refused CONNECT as "proxyconnect tcp: ..." for
// a proxy it could not reach, and with the proxy's status line otherwise.
var proxyAnswered = regexp.MustCompile(`Proxy response \((\d{3})\)|proxy(?:connect)?[^:]*:.*?(\d{3}) [A-Z]`)

// Proxy is proxyFailure: the proxy failure behind err for a request to
// host, or nil.
func Proxy(err error, host string, env Getenv) *ProxyFailure {
	proxy := ProxyFor(host, env)
	if proxy == nil || err == nil {
		return nil
	}
	shown := proxy.Hostname()
	if p := proxy.Port(); p != "" {
		shown += ":" + p
	}
	if m := proxyAnswered.FindStringSubmatch(err.Error()); m != nil {
		st := m[1]
		if st == "" {
			st = m[2]
		}
		var n int
		fmt.Sscan(st, &n)
		return &ProxyFailure{Proxy: shown, Code: "refused", Status: n}
	}
	// Through a proxy this computer only ever connects to the proxy, so a
	// failure to connect is the proxy's.
	if c := Code(err); proxyConnectCodes[c] && c != "timeout" {
		return &ProxyFailure{Proxy: shown, Code: c}
	}
	return nil
}

// ProxyMessage is proxyMessage.
func ProxyMessage(host string, f *ProxyFailure) string {
	to := host
	if to == "" {
		to = "the server"
	}
	if f.Code == "refused" && f.Status == 407 {
		return fmt.Sprintf("The proxy at %s asks for a sign-in (407), so it did not open a connection to %s. The Studio signs in with a user name and password given in the proxy address (HTTPS_PROXY=http://user:password@host:port); other kinds of proxy sign-in are not supported.", f.Proxy, to)
	}
	if f.Code == "refused" {
		ans := "with a refusal"
		if f.Status != 0 {
			ans = fmt.Sprint(f.Status)
		}
		return fmt.Sprintf("The proxy at %s would not open a connection to %s (it answered %s).", f.Proxy, to, ans)
	}
	return fmt.Sprintf("The proxy at %s could not be reached (%s), so %s could not be either. Check the proxy setting (HTTPS_PROXY, or the system's proxy settings in the macOS app).", f.Proxy, f.Code, to)
}

// ConnectionProblem is connectionProblem: a sentence about a certificate
// or proxy problem behind err for a request to host, or "".
func ConnectionProblem(err error, host string, env Getenv) string {
	if t := Trust(err); t != nil {
		return TrustMessage(host, t)
	}
	if p := Proxy(err, host, env); p != nil {
		return ProxyMessage(host, p)
	}
	return ""
}
