package links

import (
	"net"
	"net/url"
	"strings"
)

// Base is the address links start with: the Studio's own address as people
// reach it. selfURL (STUDIO_SELF_URL) names it, else the address people
// sign in at (OIDC_REDIRECT_URI), which is fixed. "" where neither is an
// address others can open, and the page's own is used.
func Base(selfURL, oidcRedirect, callbackPath string) string {
	if b := lasting(selfURL); b != "" {
		return b
	}
	b := lasting(oidcRedirect)
	if b == "" || !strings.HasSuffix(b, callbackPath) {
		return ""
	}
	return strings.TrimRight(strings.TrimSuffix(b, callbackPath), "/")
}

func lasting(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || Loopback(u.Hostname()) {
		return ""
	}
	return "https://" + u.Host + strings.TrimRight(u.EscapedPath(), "/")
}

// Loopback reports a host only the computer itself reaches.
func Loopback(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}
