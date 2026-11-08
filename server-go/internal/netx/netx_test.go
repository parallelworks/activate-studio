package netx

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
)

func envOf(m map[string]string) Getenv { return func(k string) string { return m[k] } }

func TestNetworkFailure(t *testing.T) {
	cases := []struct {
		err   error
		code  string
		noDNS bool
	}{
		{&net.DNSError{Err: "no such host", Name: "x.example", IsNotFound: true}, "ENOTFOUND", true},
		{&net.DNSError{Err: "try again", Name: "x", IsTemporary: true}, "EAI_AGAIN", true},
		{&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, "ECONNREFUSED", false},
		{fmt.Errorf("wrapped: %w", syscall.ECONNRESET), "ECONNRESET", false},
		{context.DeadlineExceeded, "timeout", false},
		{errors.New("read ECONNRESET"), "ECONNRESET", false},
	}
	for _, c := range cases {
		f := Failure(c.err)
		if f == nil || f.Code != c.code || f.NoDNS != c.noDNS {
			t.Errorf("%v: %+v, want %s %v", c.err, f, c.code, c.noDNS)
		}
	}
	if Failure(errors.New("400 bad request")) != nil || Failure(nil) != nil {
		t.Error("not a network failure")
	}
	if !CLINetworkFailure("Get https://x: dial tcp: lookup x: no such host") || CLINetworkFailure("401 unauthorized") {
		t.Error("cli wording")
	}
}

func TestMessagesAsNetTs(t *testing.T) {
	if got := UnreachableMessage("127.0.0.1:8000", &NetworkFailure{Code: "ECONNREFUSED"}); got != "Nothing answers at 127.0.0.1:8000 (ECONNREFUSED). Start the model server on this computer, or check its address." {
		t.Error(got)
	}
	if got := UnreachableMessage("", &NetworkFailure{Code: "timeout"}); got != "Could not reach the server (no answer in time). This computer may be offline, or the server is down. Local libraries and a local model keep working." {
		t.Error(got)
	}
	for _, h := range []string{"localhost", "[::1]:80", "127.1.2.3:9", "LOCALHOST:1"} {
		if !IsLoopbackHost(h) {
			t.Error(h)
		}
	}
	if IsLoopbackHost("example.com") {
		t.Error("example.com")
	}
}

func TestTrust(t *testing.T) {
	f := Trust(fmt.Errorf("tls: %w", x509.UnknownAuthorityError{}))
	if f == nil || f.Kind != "untrusted" || f.Code != "UNABLE_TO_GET_ISSUER_CERT_LOCALLY" {
		t.Fatalf("%+v", f)
	}
	if !strings.Contains(TrustMessage("api.example", f), "used a certificate this computer does not trust (unable to get local issuer certificate)") {
		t.Error(TrustMessage("api.example", f))
	}
	if f := Trust(x509.CertificateInvalidError{Reason: x509.Expired}); f == nil || f.Kind != "expired" {
		t.Error("expired")
	}
	if f := Trust(x509.HostnameError{Host: "a", Certificate: &x509.Certificate{}}); f == nil || f.Kind != "name" {
		t.Error("name")
	}
	if Trust(errors.New("x")) != nil {
		t.Error("not a certificate failure")
	}
}

// netTrust.ts's and net.ts's proxy rules: HTTPS_PROXY before HTTP_PROXY,
// a scheme added when missing, NO_PROXY by name, suffix, leading dot or
// star, and * for every host.
func TestProxyFor(t *testing.T) {
	env := envOf(map[string]string{"HTTP_PROXY": "proxy.corp:3128", "NO_PROXY": "internal.corp, .lab, *.dev,localhost"})
	if p := ProxyFor("api.example.com:443", env); p == nil || p.Host != "proxy.corp:3128" || p.Scheme != "http" {
		t.Fatalf("%v", p)
	}
	for _, h := range []string{"internal.corp", "a.internal.corp", "x.lab", "y.dev:8080", "localhost:4080"} {
		if ProxyFor(h, env) != nil {
			t.Error("not excepted:", h)
		}
	}
	if ProxyFor("x", envOf(map[string]string{"HTTPS_PROXY": "http://p:1", "no_proxy": "*"})) != nil {
		t.Error("* excepts all")
	}
	if ProxyFor("x", envOf(nil)) != nil {
		t.Error("no proxy")
	}
	f := Proxy(&net.OpError{Op: "proxyconnect", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, "api.example", envOf(map[string]string{"HTTPS_PROXY": "http://user:pw@p.corp:8080"}))
	if f == nil || f.Code != "ECONNREFUSED" || f.Proxy != "p.corp:8080" {
		t.Fatalf("%+v", f)
	}
	f = Proxy(errors.New("Proxy response (407) !== 200 when HTTP Tunneling"), "api.example", envOf(map[string]string{"HTTPS_PROXY": "p:1"}))
	if f == nil || f.Status != 407 || !strings.Contains(ProxyMessage("api.example", f), "asks for a sign-in (407)") {
		t.Fatalf("%+v", f)
	}
}

func TestRedactAndSetup(t *testing.T) {
	if got := RedactProxy("http://user:secret@p.corp:8080/"); got != "http://user:***@p.corp:8080" {
		t.Error(got)
	}
	if got := RedactProxy("p.corp:3128"); got != "http://p.corp:3128" {
		t.Error(got)
	}
	t.Setenv("HTTPS_PROXY", "http://p.corp:1")
	t.Setenv("NO_PROXY", "a.corp,localhost")
	t.Setenv("no_proxy", "")
	o := Setup()
	if o.NoProxy != "a.corp,localhost,127.0.0.1,::1" || os.Getenv("no_proxy") != o.NoProxy {
		t.Errorf("%+v", o)
	}
	if !strings.Contains(o.Line(), "through the proxy http://p.corp:1 (not for a.corp,localhost,127.0.0.1,::1)") {
		t.Error(o.Line())
	}
}
