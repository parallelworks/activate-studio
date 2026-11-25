package sessions

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// ownRequest is the header the Studio's own page sends with its requests
// (web/src/ownRequest.ts).
const ownRequest = "X-Studio-Request"

// OtherSiteRemedy ends a refusal: what to set where the page was the
// Studio's own after all (docs/DEPLOY.md).
const OtherSiteRemedy = " If this is the Studio's own page, behind a proxy, set STUDIO_SELF_URL to the address people open, or have the proxy send that host in X-Forwarded-Host."

// OtherSite is whether a page of another site sent a request, a GET's too:
// what is read with a person's own sign-in is asked for by the Studio's
// pages only, with sign-in off as with it on. A browser says so itself
// (Sec-Fetch-Site) where it trusts the address. Where it does not, over plain
// http, the Studio's page has the header ownRequest, which a page of another
// site cannot send: it would need a CORS preflight the Studio never grants. A
// request without it has the origin of its page on anything but a GET, and
// that is the host the request was sent to or, behind a proxy that rewrites
// the host, the Studio's own address as its environment has it (own).
func OtherSite(r *http.Request, own ...string) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return true
	case "same-origin":
		return false
	}
	if r.Header.Get(ownRequest) == "1" {
		return false
	}
	at := r.Header.Get("Origin")
	if at == "" {
		u, err := url.Parse(r.Header.Get("Referer"))
		if err != nil || u.Host == "" {
			return false
		}
		at = u.Scheme + "://" + u.Host
	}
	for _, o := range own {
		if o := originOf(o); o != "" && o == originOf(at) {
			return false
		}
	}
	named := r.Clone(r.Context())
	named.Header.Set("Origin", at)
	return !SameOrigin(named)
}

// originOf is an address's origin, "" for one of no web page.
func originOf(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	return u.Scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}
