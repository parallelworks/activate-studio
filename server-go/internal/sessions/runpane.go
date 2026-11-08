package sessions

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/workspace"
)

var slugRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,120}$`)

var terminal = map[string]bool{"completed": true, "error": true, "failed": true, "cancelled": true, "canceled": true}

var paneKeep = []string{"slug", "number", "status", "statusReason", "createdAt", "completedAt", "workflowName", "workflowSlug", "inputs", "executedJobs", "links", "user"}

// PaneRun is paneRun: the run fields the pane draws, or nil.
func PaneRun(raw any) *jsonv.Object {
	o := jsonv.Obj(raw)
	if o == nil || !jsonv.Truthy(jsonv.Path(o, "slug")) {
		return nil
	}
	out := jsonv.NewObject()
	for _, k := range paneKeep {
		if v, ok := o.Get(k); ok {
			out.Set(k, v)
		}
	}
	st, _ := o.Get("status")
	if st == nil {
		st = ""
	}
	out.Set("terminal", terminal[strings.ToLower(jsonv.String(st))])
	return out
}

var hostRe = regexp.MustCompile(`^[a-z0-9.-]+(:\d{1,5})?$`)

// ProxyOrigin is proxyOrigin: the origin to relay for a requested host,
// that of a session the viewer can see served on its own host, or "".
func ProxyOrigin(host string, rows []*SessionRow) string {
	want := strings.ToLower(host)
	if want == "" || !hostRe.MatchString(want) {
		return ""
	}
	for _, s := range rows {
		if !s.OwnHost || s.URL == "" {
			continue
		}
		u, err := url.Parse(s.URL)
		if err != nil || u.Host == "" {
			continue
		}
		if strings.ToLower(u.Host) == want {
			return u.Scheme + "://" + strings.ToLower(u.Host)
		}
	}
	return ""
}

// pass are the response headers relayed back; framing rules, cookies and
// transfer framing stay with the session's host.
var pass = []string{"content-type", "cache-control", "etag", "last-modified", "content-disposition", "content-language", "expires"}

// FrameScrollGuard is runPane.ts's FRAME_SCROLL_GUARD, byte for byte.
const FrameScrollGuard = `<script>/* ACTIVATE Studio: keep this page's scrolling inside its frame */(function () {
  if (window.top === window) return;
  var E = Element.prototype, H = HTMLElement.prototype, focus = H.focus;
  function scrollers(el) {
    var out = [], p = el.parentElement;
    while (p) { var s = getComputedStyle(p); if (/(auto|scroll)/.test(s.overflowY + ' ' + s.overflowX)) out.push(p); p = p.parentElement; }
    out.push(document.scrollingElement || document.documentElement);
    return out;
  }
  function delta(a0, a1, b0, b1, mode) {
    if (mode === 'start') return a0 - b0;
    if (mode === 'end') return a1 - b1;
    if (mode === 'center') return (a0 + a1) / 2 - (b0 + b1) / 2;
    if (a0 < b0) return a0 - b0;
    if (a1 > b1) return Math.min(a1 - b1, a0 - b0);
    return 0;
  }
  E.scrollIntoView = function (arg) {
    var o = arg && typeof arg === 'object' ? arg : { block: arg === false ? 'end' : 'start' };
    var list = scrollers(this);
    for (var i = 0; i < list.length; i++) {
      var p = list[i], r = this.getBoundingClientRect();
      var top = p === list[list.length - 1] ? { top: 0, bottom: innerHeight, left: 0, right: innerWidth } : p.getBoundingClientRect();
      p.scrollTop += delta(r.top, r.bottom, top.top, top.bottom, o.block || 'start');
      p.scrollLeft += delta(r.left, r.right, top.left, top.right, o.inline || 'nearest');
    }
  };
  if (E.scrollIntoViewIfNeeded) E.scrollIntoViewIfNeeded = function () { E.scrollIntoView.call(this, { block: 'nearest' }); };
  H.focus = function (o) { return focus.call(this, Object.assign({}, o, { preventScroll: true })); };
})();</script>`

var (
	headRe    = regexp.MustCompile(`(?i)<head[^>]*>`)
	doctypeRe = regexp.MustCompile(`(?i)^\s*<!doctype[^>]*>`)
	htmlType  = regexp.MustCompile(`(?i)^text/html\b`)
)

// WithScrollGuard is withScrollGuard.
func WithScrollGuard(html string) string {
	if m := headRe.FindStringIndex(html); m != nil {
		return html[:m[1]] + FrameScrollGuard + html[m[1]:]
	}
	if m := doctypeRe.FindStringIndex(html); m != nil {
		return html[:m[1]] + FrameScrollGuard + html[m[1]:]
	}
	return FrameScrollGuard + html
}

func (s *Routes) runItem(w http.ResponseWriter, r *http.Request) (any, error) {
	slug := platform.Q(r, "slug")
	if !slugRe.MatchString(slug) {
		return nil, platform.KbError(400, "slug required")
	}
	key, err := s.keyOf(r)
	if err != nil {
		return nil, err
	}
	run := PaneRun(s.p.APIJSON(r.Context(), "/api/workflow-runs/"+platform.EncodeURIComponent(slug), key, 0))
	if run == nil {
		return nil, platform.KbError(404, "No run "+slug+" is visible to you.")
	}
	return run, nil
}

const startEvery = 3 * time.Minute

// startWorkspace is `pw workspace start` with the key, as runPane.ts; its
// outcome is not waited for.
func (s *Routes) startWorkspace(key string) {
	go s.p.Exec(context.Background(), []string{"workspace", "start"}, platform.ExecOptions{Timeout: 120 * time.Second, Env: map[string]string{"PW_API_KEY": key}})
}

func (s *Routes) runFile(w http.ResponseWriter, r *http.Request) (any, error) {
	slug, path := platform.Q(r, "slug"), platform.Q(r, "path")
	if !slugRe.MatchString(slug) {
		return nil, platform.KbError(400, "slug required")
	}
	if path == "" || utf16Len(path) > 512 || strings.HasPrefix(path, "/") || contains(strings.Split(path, "/"), "..") {
		return nil, platform.KbError(400, "a path inside the run directory is required")
	}
	key, err := s.keyOf(r)
	if err != nil {
		return nil, err
	}
	res := s.p.APIFetch(r.Context(), "/api/workflow-runs/"+platform.EncodeURIComponent(slug)+"/files?file="+platform.EncodeURIComponent(path), key, 0)
	if res == nil {
		return nil, platform.KbError(502, "The platform did not answer.")
	}
	if text, ok := res.Body.(string); ok && res.OK {
		return jsonv.NewObject("text", text), nil
	}
	msgV := jsonv.Path(res.Body, "message")
	if msgV == nil {
		msgV = jsonv.Path(res.Body, "error")
	}
	message := jsonv.String(msgV)
	if workspace.IsDown(message) {
		s.mu.Lock()
		due := time.Since(s.run[key]) > startEvery
		if due {
			s.run[key] = time.Now()
		}
		s.mu.Unlock()
		if due {
			s.pw(key)
		}
		return nil, platform.KbError(503, "Your platform workspace was not running, so a start was requested; run files appear once it is up, usually within a minute or two.")
	}
	if res.Status == 404 {
		return nil, platform.KbError(404, "No file "+path+" in run "+slug+".")
	}
	if message != "" {
		return nil, platform.KbError(502, "The platform could not read "+path+": "+message)
	}
	return nil, platform.KbError(502, "The platform could not read "+path+" (HTTP "+itoa(res.Status)+").")
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// redirect is GET /api/session-proxy/:host: relative addresses resolve
// against the page's own folder, so the page is served with its slash.
func (s *Routes) redirect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Location", "/api/session-proxy/"+r.PathValue("host")+"/")
	w.WriteHeader(http.StatusFound)
}

const relayTimeout = 60 * time.Second

// relay serves a session's own host under /api/session-proxy/<host>/ with
// the viewer's key: only hosts of sessions the key can see, the address
// from the platform's record, never from the request.
func (s *Routes) relay(w http.ResponseWriter, r *http.Request) (any, error) {
	host := r.PathValue("host")
	key, err := s.keyOf(r)
	if err != nil {
		return nil, err
	}
	rows, err := List(r.Context(), s.p, key)
	if err != nil {
		return nil, err
	}
	origin := ProxyOrigin(host, rows)
	if origin == "" {
		return nil, platform.KbError(404, "That session is not one of yours, or it is not served on its own host.")
	}
	prefix := "/api/session-proxy/" + host
	raw := r.RequestURI
	// req.url.slice(req.url.indexOf(prefix) + prefix.length) || '/'
	from := strings.Index(raw, prefix) + len(prefix)
	if from < 0 {
		from = 0
	}
	rest := "/"
	if from < len(raw) {
		rest = raw[from:]
	}
	ctx, cancel := context.WithTimeout(r.Context(), relayTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, origin+rest, nil)
	if err != nil {
		return nil, platform.KbError(502, "The session did not answer: "+err.Error())
	}
	req.Header.Set("Authorization", "Bearer "+key)
	// fetch sends Accept: */* and its own User-Agent when none is given.
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "node")
	if a := r.Header.Get("Accept"); a != "" {
		req.Header.Set("Accept", a)
	}
	if rg := r.Header.Get("Range"); rg != "" {
		req.Header.Set("Range", rg)
	}
	client := &http.Client{Transport: platform.HTTPClient.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		msg := "fetch failed"
		if errors.Is(err, context.DeadlineExceeded) {
			msg = "The operation was aborted due to timeout"
		}
		return nil, platform.KbError(502, "The session did not answer: "+msg)
	}
	defer res.Body.Close()
	// A redirect is the session host's sign-in page: the key was not accepted.
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		return nil, platform.KbError(502, "The session asked for a sign-in the Studio cannot complete; open it in a new tab.")
	}
	h := w.Header()
	for _, name := range pass {
		if v := res.Header.Get(name); v != "" {
			h.Set(name, v)
		}
	}
	if r.Method == http.MethodHead {
		// reply.send('') on HEAD: a string payload is text/plain when the
		// session gave no type.
		if h.Get("Content-Type") == "" {
			h.Set("Content-Type", "text/plain; charset=utf-8")
		}
		h.Set("Content-Length", "0")
		w.WriteHeader(res.StatusCode)
		return nil, nil
	}
	if htmlType.MatchString(res.Header.Get("Content-Type")) {
		b, err := io.ReadAll(res.Body)
		if err != nil {
			return nil, err
		}
		page := WithScrollGuard(decodeUTF8(b))
		h.Set("Content-Length", itoa(len(page)))
		w.WriteHeader(res.StatusCode)
		io.WriteString(w, page)
		return nil, nil
	}
	// Everything else streams through as it arrives. A stream is sent with
	// no type of its own when the session gave none (Go would sniff one).
	if h.Get("Content-Type") == "" {
		h["Content-Type"] = nil
	}
	w.WriteHeader(res.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 64<<10)
	for {
		n, rerr := res.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return nil, nil
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				// A body cut off mid-stream ends the answer as Node's does:
				// the connection closes without the last chunk.
				panic(http.ErrAbortHandler)
			}
			return nil, nil
		}
	}
}

// decodeUTF8 is Buffer.toString('utf8'): each invalid byte becomes U+FFFD.
func decodeUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		if r == utf8.RuneError && n <= 1 {
			sb.WriteRune(utf8.RuneError)
		} else {
			sb.Write(b[:n])
		}
		b = b[n:]
	}
	return sb.String()
}
