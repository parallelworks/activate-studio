package sessions

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// The desktop relay: a platform desktop session (type "desktop", software
// "vnc") has no host of its own, and its platform page refuses to be
// framed, so the Studio draws the desktop itself with an RFB client in the
// page and relays the session's websockify socket:
//
//	GET /api/session-desktop/<user>/<name>/websockify   the VNC socket
//	GET /api/session-desktop/<user>/<name>/logs         the session's logs
//
// Only a desktop the viewer's key lists is relayed, at the address built
// from that record and the platform's own host, never from the request,
// and always as the viewer: with sign-in on, the person's own platform
// credential, never the deployment's; without it, refused.

// isDesktop is whether a platform session record is a desktop the relay
// can draw.
func isDesktop(o *jsonv.Object) bool {
	return jsString(jsonv.Path(o, "type")) == "desktop" && jsString(jsonv.Path(o, "software", "type")) == "vnc"
}

// DesktopOf is the desktop session among rows with that user and name, or
// nil.
func DesktopOf(rows []*SessionRow, user, name string) *SessionRow {
	if user == "" || name == "" {
		return nil
	}
	for _, s := range rows {
		if s.Desktop && s.User == user && s.Name == name {
			return s
		}
	}
	return nil
}

// DesktopAddress is the platform address of a desktop's endpoint: the
// websockify socket (leaf "websockify") or its logs ("logs"), under the
// platform's origin, for the record's own user and name.
func DesktopAddress(platformOrigin string, s *SessionRow, leaf string) (*url.URL, error) {
	base, err := url.Parse(platformOrigin)
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") {
		return nil, errors.New("no platform address")
	}
	if s == nil || s.User == "" || s.Name == "" || strings.ContainsAny(s.User+s.Name, "/?#") {
		return nil, errors.New("no desktop")
	}
	u := &url.URL{Scheme: base.Scheme, Host: base.Host}
	u.Path = "/me/session/" + s.User + "/" + s.Name + "/" + leaf
	u.RawPath = "/me/session/" + url.PathEscape(s.User) + "/" + url.PathEscape(s.Name) + "/" + leaf
	return u, nil
}

// DesktopKey is the key the desktop relay connects with. With sign-in on
// (or any identity on the request) it is the person's own platform
// credential and nothing else; a person without one is refused, and the
// deployment's key is never used for them. With sign-in off the Studio is
// one person's, and their key is the deployment's.
func DesktopKey(p *platform.Platform, v *platform.Viewer) (string, error) {
	if v == nil {
		return "", platform.KbError(401, "Sign in to the Studio to open a desktop.")
	}
	if v.AuthEnabled || v.User != nil {
		if v.UserID() == "" {
			return "", platform.KbError(401, "Sign in to the Studio to open a desktop.")
		}
		if k := v.PlatformKey(); k != "" {
			return k, nil
		}
		return "", platform.KbError(409, "A desktop opens with your own ACTIVATE sign-in. Sign in with ACTIVATE under Settings, Accounts, then try again.")
	}
	if k := KeyFor(p, v); k != "" {
		return k, nil
	}
	return "", platform.KbError(409, "No platform credential: add your ACTIVATE API key under Settings, Accounts.")
}

// SameOrigin is whether a browser request comes from a page of this
// Studio: its Origin names the host the request was sent to, or the host
// a proxy in front says it was sent to (X-Forwarded-Host, which the window
// shell and the session ingress set). A WebSocket is not bound by CORS, so
// without this a page on another site could open the person's desktop.
func SameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" || o == "null" {
		return false
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	want := strings.ToLower(u.Host)
	if strings.ToLower(r.Host) == want {
		return true
	}
	if fh := firstHost(r.Header.Get("X-Forwarded-Host")); fh != "" && fh == want {
		return true
	}
	return false
}

func firstHost(v string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(v, ",")[0]))
}

// isWebSocket is whether the request asks to become a WebSocket.
func isWebSocket(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	for _, v := range r.Header.Values("Connection") {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), "upgrade") {
				return true
			}
		}
	}
	return false
}

// desktopOf runs the relay's checks: the viewer's key, and the desktop
// that key lists under the requested user and name.
func (s *Routes) desktopOf(r *http.Request) (string, *SessionRow, error) {
	v, err := s.viewer(r)
	if err != nil {
		return "", nil, err
	}
	key, err := DesktopKey(s.p, v)
	if err != nil {
		return "", nil, err
	}
	rows, err := List(r.Context(), s.p, key)
	if err != nil {
		return "", nil, err
	}
	user, name := r.PathValue("user"), r.PathValue("name")
	row := DesktopOf(rows, user, name)
	if row == nil {
		return "", nil, platform.KbError(404, "No desktop session "+name+" of "+user+" is visible to you.")
	}
	return key, row, nil
}

// wsSendHeaders are the request headers a desktop socket carries upstream;
// the Studio's cookies and sign-in headers stay here.
var wsSendHeaders = []string{"Sec-Websocket-Key", "Sec-Websocket-Version", "Sec-Websocket-Protocol", "Sec-Websocket-Extensions", "Upgrade", "Connection"}

// wsAnswerHeaders are the platform's handshake headers the browser gets.
var wsAnswerHeaders = []string{"Upgrade", "Connection", "Sec-Websocket-Accept", "Sec-Websocket-Protocol", "Sec-Websocket-Extensions"}

type upstreamError struct {
	status int
	msg    string
}

func (e *upstreamError) Error() string { return e.msg }

// desktopSocket is GET /api/session-desktop/{user}/{name}/websockify.
func (s *Routes) desktopSocket(w http.ResponseWriter, r *http.Request) {
	if !isWebSocket(r) {
		apierr.Write(w, s.a.Log, platform.KbError(400, "This address takes a WebSocket from the Studio's desktop viewer."))
		return
	}
	if !SameOrigin(r) {
		apierr.Write(w, s.a.Log, platform.KbError(403, "A desktop opens only from the Studio's own pages."))
		return
	}
	key, row, err := s.desktopOf(r)
	if err != nil {
		apierr.Write(w, s.a.Log, err)
		return
	}
	target, err := DesktopAddress(s.p.Origin(), row, "websockify")
	if err != nil {
		apierr.Write(w, s.a.Log, platform.KbError(502, "No platform address is configured."))
		return
	}
	origin := target.Scheme + "://" + target.Host
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = &url.URL{Scheme: target.Scheme, Host: target.Host, Path: target.Path, RawPath: target.RawPath}
			pr.Out.Host = target.Host
			keep := http.Header{}
			for _, h := range wsSendHeaders {
				if vs := pr.Out.Header.Values(h); len(vs) > 0 {
					keep[h] = vs
				}
			}
			keep.Set("Authorization", "Bearer "+key)
			keep.Set("Origin", origin)
			keep.Set("User-Agent", "activate-studio")
			pr.Out.Header = keep
		},
		Transport: s.desktopTransport(),
		ModifyResponse: func(res *http.Response) error {
			if res.StatusCode == http.StatusSwitchingProtocols {
				// Only the handshake's own headers reach the browser: a
				// cookie or other header the platform sets is for its
				// origin, and would otherwise be kept under the Studio's.
				keep := http.Header{}
				for _, h := range wsAnswerHeaders {
					if vs := res.Header.Values(h); len(vs) > 0 {
						keep[h] = vs
					}
				}
				res.Header = keep
				return nil
			}
			return upstreamFailure(res.StatusCode)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			var ue *upstreamError
			if errors.As(err, &ue) {
				apierr.Write(w, s.a.Log, platform.KbError(ue.status, ue.msg))
				return
			}
			if errors.Is(err, context.Canceled) {
				return
			}
			apierr.Write(w, s.a.Log, platform.KbError(502, "The desktop did not answer."))
		},
	}
	rp.ServeHTTP(w, r)
}

// upstreamFailure explains a desktop socket the platform did not open.
func upstreamFailure(status int) error {
	switch {
	case status >= 300 && status < 400, status == 401, status == 403:
		return &upstreamError{502, "The platform did not accept your credential for this desktop; it may have expired or been revoked."}
	case status == 404:
		return &upstreamError{404, "The platform has no desktop at that address; it may have stopped."}
	case status == 502 || status == 503 || status == 504:
		return &upstreamError{503, "The desktop's cluster is not answering; it may be starting, or offline."}
	}
	return &upstreamError{502, "The platform did not open the desktop (HTTP " + itoa(status) + ")."}
}

// desktopTransport is how the relay reaches the platform; tests set it.
func (s *Routes) desktopTransport() http.RoundTripper {
	if s.transport != nil {
		return s.transport
	}
	return platform.HTTPClient.Transport
}

const logsTimeout = 20 * time.Second

// desktopLogs is GET /api/session-desktop/{user}/{name}/logs: the desktop's
// start-up logs from the platform ({xstartup, xvnc}), which the viewer
// shows while it connects. Its errors are the relay's, so the viewer can
// say why a socket did not open.
func (s *Routes) desktopLogs(w http.ResponseWriter, r *http.Request) (any, error) {
	key, row, err := s.desktopOf(r)
	if err != nil {
		return nil, err
	}
	target, err := DesktopAddress(s.p.Origin(), row, "logs")
	if err != nil {
		return nil, platform.KbError(502, "No platform address is configured.")
	}
	ctx, cancel := context.WithTimeout(r.Context(), logsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, platform.KbError(502, "The desktop did not answer.")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Transport: s.desktopTransport(), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return nil, platform.KbError(502, "The desktop did not answer.")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		var ue *upstreamError
		errors.As(upstreamFailure(res.StatusCode), &ue)
		return nil, platform.KbError(ue.status, ue.msg)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, platform.KbError(502, "The desktop did not answer.")
	}
	v, err := jsonv.Decode(b)
	o := jsonv.Obj(v)
	if err != nil || o == nil {
		return jsonv.NewObject("xstartup", "", "xvnc", ""), nil
	}
	return jsonv.NewObject("xstartup", jsString(jsonv.Path(o, "xstartup")), "xvnc", jsString(jsonv.Path(o, "xvnc"))), nil
}
