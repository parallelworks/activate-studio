// Package live sends an open page what it shows as it changes, over one
// WebSocket, in place of the page asking for each thing on its own timer.
//
// The page names the paths it shows and how often each is worth reading
// (Socket). The Studio reads each one at that pace, exactly as the same GET
// from the page would be answered: through the sign-in gate and the route
// itself, as the person, every time. A read that says what the last one
// said goes as a few bytes, so a page that sits open makes no requests.
// Where a socket cannot open, the page reads the same paths together in one
// request (Read).
package live

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"golang.org/x/net/websocket"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/sessions"
)

// paths are what may be followed: state that pages keep on the screen, each
// a plain GET.
var paths = map[string]bool{
	"/healthz":                  true,
	"/api/ai/health":            true,
	"/api/chat/pwcode/here":     true,
	"/api/chat/pwcode/names":    true,
	"/api/delegated":            true,
	"/api/fleet":                true,
	"/api/index/status":         true,
	"/api/kb/stats":             true,
	"/api/libraries":            true,
	"/api/notices":              true,
	"/api/runs":                 true,
	"/api/sessions":             true,
	"/api/settings/mcp-servers": true,
	"/api/transfers":            true,
	"/api/transfers/summary":    true,
	"/api/user-libraries":       true,
	"/api/version":              true,
	"/api/whoami":               true,
	"/api/workflows/runs":       true,
	"/api/workflows/collection": true,
}

const (
	maxPaths   = 64
	minEvery   = time.Second
	maxEvery   = time.Hour
	maxSockets = 200
	readFor    = 60 * time.Second
	// Reads that say nothing new within this of one another are one frame.
	sameFor = time.Second
	maxBody = 16 << 20
)

// Service holds the open sockets.
type Service struct {
	// read answers r as the Studio answers the same request made on its
	// own, the sign-in gate included.
	read func(w http.ResponseWriter, r *http.Request) *http.Request
	// beat is how often an open socket says it is there when it says
	// nothing else, so the page notices one that went silent; life is how
	// long one lasts: the page opens another before, with its sign-in as it
	// is then.
	beat, life time.Duration
	// listEvery is how often a listing feed reads.
	listEvery time.Duration

	mu    sync.Mutex
	conns map[string]map[*conn]bool
}

// New is the service over read, which answers a request and returns it as
// the sign-in gate passed it (nil when the gate refused it).
func New(read func(w http.ResponseWriter, r *http.Request) *http.Request) *Service {
	return &Service{read: read, beat: 4 * time.Second, life: 10 * time.Minute, listEvery: 5 * time.Second, conns: map[string]map[*conn]bool{}}
}

// Routes registers the socket, the combined read, and the feeds of the two
// pw code listings a page follows.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/live", s.Socket)
	mux.HandleFunc("GET /api/live/read", s.Read)
	mux.HandleFunc("GET /api/pwcode-feed/sessions", s.listing("/api/code/sessions"))
	mux.HandleFunc("GET /api/code-page/feed", s.listing("/api/code-page/sessions"))
}

// Drop closes a person's sockets: their cookies changed (a sign-in, a key),
// and the page opens others with the new ones.
func (s *Service) Drop(user string) {
	s.mu.Lock()
	var gone []*conn
	for c := range s.conns[user] {
		gone = append(gone, c)
	}
	s.mu.Unlock()
	for _, c := range gone {
		c.cancel()
	}
}

func (s *Service) add(c *conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.conns[c.user]) >= maxSockets {
		return false
	}
	if s.conns[c.user] == nil {
		s.conns[c.user] = map[*conn]bool{}
	}
	s.conns[c.user][c] = true
	return true
}

func (s *Service) remove(c *conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns[c.user], c)
	if len(s.conns[c.user]) == 0 {
		delete(s.conns, c.user)
	}
}

func userID(r *http.Request) string {
	if u := auth.UserOf(r); u != nil {
		return u.ID
	}
	return ""
}

// allowed is the path of a read the page may ask for, or "".
func allowed(p string) string {
	u, err := url.ParseRequestURI(p)
	if err != nil || u.Host != "" || u.Scheme != "" || len(p) > 2048 || !paths[u.Path] || u.Path != u.EscapedPath() {
		return ""
	}
	return u.RequestURI()
}

// answer is what one read answered.
type answer struct {
	status int
	body   []byte
	sum    [32]byte
	// user is whom the sign-in gate found, and refused that it let nobody
	// through. gaveUp is a handler that ended without an answer.
	user            string
	refused, gaveUp bool
}

// recorder keeps a read's answer.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
	over   bool
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
}
func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.body.Len()+len(b) > maxBody {
		r.over = true
		return len(b), nil
	}
	return r.body.Write(b)
}
func (r *recorder) Flush() {}

// handshake are the headers of the request that opened a socket which are
// not those of a read made over it.
var handshake = []string{"Connection", "Upgrade", "Sec-Websocket-Key", "Sec-Websocket-Version", "Sec-Websocket-Extensions",
	"Sec-Websocket-Protocol", "Accept-Encoding", "If-None-Match", "If-Modified-Since", "Range", "Content-Length", "Content-Type"}

// get reads path as r's sender would.
func (s *Service) get(ctx context.Context, r *http.Request, path string) (a answer) {
	// A context of its own: what the gate found of the person when the
	// socket opened is not this read's.
	fresh, cancel := context.WithTimeout(context.Background(), readFor)
	defer cancel()
	defer context.AfterFunc(ctx, cancel)()
	u, err := url.ParseRequestURI(path)
	if err != nil {
		return answer{status: http.StatusBadRequest}
	}
	in := (&http.Request{Method: http.MethodGet, URL: u, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: r.Header.Clone(),
		Body: http.NoBody, Host: r.Host, RemoteAddr: r.RemoteAddr, RequestURI: u.RequestURI(), TLS: r.TLS}).WithContext(fresh)
	for _, h := range handshake {
		in.Header.Del(h)
	}
	in.Header.Set("Accept", "application/json")
	rec := &recorder{header: http.Header{}}
	func() {
		// A handler that gives its answer up panics (http.ErrAbortHandler).
		defer func() {
			if p := recover(); p != nil {
				rec.status, rec.over, a.gaveUp = http.StatusBadGateway, true, true
			}
		}()
		if got := s.read(rec, in); got != nil {
			a.user = userID(got)
		} else {
			a.refused = true
		}
	}()
	a.status, a.body = rec.status, rec.body.Bytes()
	switch {
	case a.status == 0:
		a.status = http.StatusBadGateway
	case rec.over:
		a.status, a.body = http.StatusBadGateway, nil
	}
	if !json.Valid(a.body) {
		a.body, _ = json.Marshal(string(a.body))
	}
	a.sum = sha256.Sum256(append(strconv.AppendInt(nil, int64(a.status), 10), a.body...))
	return a
}

// Read is GET /api/live/read?p=...&p=...: each path's answer, for a page no
// socket opens for.
func (s *Service) Read(w http.ResponseWriter, r *http.Request) {
	ask := r.URL.Query()["p"]
	if len(ask) == 0 || len(ask) > maxPaths {
		apierr.JSON(w, http.StatusBadRequest, map[string]string{"error": "p is the paths to read"})
		return
	}
	for _, p := range ask {
		if allowed(p) == "" {
			apierr.JSON(w, http.StatusBadRequest, map[string]string{"error": "that path is not read this way"})
			return
		}
	}
	type one struct {
		Path   string          `json:"path"`
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	}
	out := make([]one, len(ask))
	var wg sync.WaitGroup
	for i, p := range ask {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := s.get(r.Context(), r, allowed(p))
			out[i] = one{p, a.status, a.body}
		}()
	}
	wg.Wait()
	w.Header().Set("Cache-Control", "no-store")
	apierr.JSON(w, http.StatusOK, map[string]any{"reads": out})
}

// conn is one open socket.
type conn struct {
	s      *Service
	r      *http.Request
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	user   string
	follow map[string]*followed

	wmu sync.Mutex
	// same are the paths whose last reads said nothing new, told together.
	same   []string
	sameAt *time.Timer
	// said is when something was last sent.
	said time.Time
}

// open makes r a socket and runs fn on it until either end goes, the
// person's sign-in changes, or its life is over.
func (s *Service) open(w http.ResponseWriter, r *http.Request, fn func(c *conn)) {
	// A WebSocket is not bound by CORS: only the Studio's own pages open it.
	if !sessions.SameOrigin(r) {
		apierr.JSON(w, http.StatusForbidden, map[string]string{"error": "Only this Studio's pages follow what it shows."})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.life)
	defer cancel()
	c := &conn{s: s, r: r, ctx: ctx, cancel: cancel, user: userID(r), follow: map[string]*followed{}}
	if !s.add(c) {
		apierr.JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "too many pages follow the Studio as you"})
		return
	}
	defer s.remove(c)
	srv := websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(ws *websocket.Conn) {
			ws.PayloadType = websocket.TextFrame
			ws.MaxPayloadBytes = 64 << 10
			c.ws = ws
			context.AfterFunc(ctx, func() { _ = ws.Close() })
			defer cancel()
			fn(c)
		},
	}
	srv.ServeHTTP(hijacker{w}, r)
}

// hijacker reaches the connection under the server's response wrappers,
// which x/net/websocket asks for as an http.Hijacker.
type hijacker struct{ http.ResponseWriter }

func (h hijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(h.ResponseWriter).Hijack()
}

func (c *conn) send(b []byte) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.write(b)
}

func (c *conn) write(b []byte) {
	c.said = time.Now()
	_ = c.ws.SetWriteDeadline(time.Now().Add(20 * time.Second))
	if websocket.Message.Send(c.ws, string(b)) != nil {
		c.cancel()
	}
}

// unchanged tells the page that path was read and said nothing new, with
// the others read within a moment.
func (c *conn) unchanged(path string) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.same = append(c.same, path)
	if c.sameAt != nil {
		return
	}
	c.sameAt = time.AfterFunc(sameFor, func() {
		c.wmu.Lock()
		defer c.wmu.Unlock()
		c.sameAt = nil
		if c.ctx.Err() == nil {
			c.flush()
		}
	})
}

// flush sends what unchanged gathered; wmu is held.
func (c *conn) flush() {
	if len(c.same) == 0 {
		return
	}
	b, _ := json.Marshal(map[string]any{"same": c.same})
	c.same = nil
	c.write(b)
}

// heartbeat sends frame before nothing has been sent for a beat, or every
// beat with always, until the socket ends.
func (c *conn) heartbeat(every time.Duration, frame string, always bool) {
	look := every
	if !always {
		look = every / 4
	}
	t := time.NewTicker(look)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
			c.wmu.Lock()
			if always || time.Since(c.said) >= every-look {
				c.write([]byte(frame))
			}
			c.wmu.Unlock()
		}
	}
}

// until tells the page how long the socket has: until its life is over, or
// before that the sign-in it was opened with.
func (c *conn) until() {
	end, _ := c.ctx.Deadline()
	if u := auth.UserOf(c.r); u != nil {
		if t := u.TokenEnds(); !t.IsZero() && t.Before(end) {
			end = t
		}
	}
	c.send(fmt.Appendf(nil, `{"until":%d}`, max(0, time.Until(end).Milliseconds())))
}

// get reads path as the socket's person; false when it is no longer them
// the read is for (a sign-in that ended or changed), which ends the socket.
// A route's own refusal is its answer.
func (c *conn) get(path string) (answer, bool) {
	a := c.s.get(c.ctx, c.r, path)
	if c.ctx.Err() != nil {
		return a, false
	}
	if path != "/healthz" && !a.gaveUp && (a.refused || a.user != c.user) {
		c.cancel()
		return a, false
	}
	return a, true
}

// followed is a path the page shows.
type followed struct {
	path  string
	every time.Duration
	stop  context.CancelFunc
	pace  chan time.Duration
	now   chan int64
}

// message is what the page sends: every path it shows, with how often in
// milliseconds and how long until the first read (want), or one to read now
// (read), answered with n.
type message struct {
	Want map[string][2]int64 `json:"want"`
	Read string              `json:"read"`
	N    int64               `json:"n"`
}

type frame struct {
	Path   string          `json:"path"`
	Status int             `json:"status,omitempty"`
	Body   json.RawMessage `json:"body,omitempty"`
	N      []int64         `json:"n,omitempty"`
}

// Socket is GET /api/live.
func (s *Service) Socket(w http.ResponseWriter, r *http.Request) {
	s.open(w, r, func(c *conn) {
		c.until()
		go c.heartbeat(s.beat, `{"beat":true}`, false)
		for {
			var raw string
			if websocket.Message.Receive(c.ws, &raw) != nil {
				return
			}
			var m message
			if json.Unmarshal([]byte(raw), &m) != nil {
				return
			}
			if m.Want != nil {
				c.want(m.Want)
			}
			if f := c.follow[m.Read]; f != nil {
				select {
				case f.now <- m.N:
				default:
				}
			}
		}
	})
}

// want follows what the page shows now, and no longer what it does not.
func (c *conn) want(m map[string][2]int64) {
	for p, f := range c.follow {
		if _, ok := m[p]; !ok {
			f.stop()
			delete(c.follow, p)
		}
	}
	for p, v := range m {
		every, first := span(v[0], minEvery), span(v[1], 0)
		if f := c.follow[p]; f != nil {
			if f.every != every {
				f.every = every
				select {
				case <-f.pace:
				default:
				}
				f.pace <- every
			}
			continue
		}
		path := allowed(p)
		if path == "" || len(c.follow) >= maxPaths {
			continue
		}
		ctx, stop := context.WithCancel(c.ctx)
		f := &followed{path: p, every: every, stop: stop, pace: make(chan time.Duration, 1), now: make(chan int64, 16)}
		c.follow[p] = f
		go c.run(ctx, f, path, every, first)
	}
}

// span is ms milliseconds within bounds.
func span(ms int64, least time.Duration) time.Duration {
	return min(max(time.Duration(min(ms, int64(maxEvery/time.Millisecond)))*time.Millisecond, least), maxEvery)
}

// run reads one path when first is over, then at its pace and when asked.
func (c *conn) run(ctx context.Context, f *followed, path string, every, first time.Duration) {
	var last [32]byte
	sent := false
	// When its last read began, the page's own included.
	at := time.Now().Add(first - every)
	t := time.NewTimer(first)
	defer t.Stop()
	for {
		var n []int64
		select {
		case <-ctx.Done():
			return
		case every = <-f.pace:
			t.Reset(max(0, time.Until(at.Add(every))))
			continue
		case v := <-f.now:
			n = append(n, v)
		case <-t.C:
		}
		// Those asked for until now get the read that starts now.
		for more := true; more; {
			select {
			case v := <-f.now:
				n = append(n, v)
			default:
				more = false
			}
		}
		at = time.Now()
		a, ok := c.get(path)
		if !ok || ctx.Err() != nil {
			return
		}
		if sent && a.sum == last && len(n) == 0 {
			c.unchanged(f.path)
		} else {
			b, err := json.Marshal(frame{Path: f.path, Status: a.status, Body: a.body, N: n})
			if err != nil {
				return
			}
			c.wmu.Lock()
			// In the order read.
			c.flush()
			c.write(b)
			c.wmu.Unlock()
		}
		last, sent = a.sum, true
		t.Reset(max(0, time.Until(at.Add(every))))
	}
}

// feedHeartbeat is the frame the platform sends a feed that asked for a
// heartbeat.
const feedHeartbeat = `{"type":"heartbeat","seq":0}`

// listing is a feed of a pw code listing as the platform's /ws/code/sessions
// sends it: at once, and again each time it changes, with a heartbeat every
// ?heartbeat= seconds. While the first read fails the feed says nothing, so
// the page polls and shows why; one that fails later ends the feed.
func (s *Service) listing(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		beat := 10
		if v := r.URL.Query().Get("heartbeat"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 60 {
				apierr.JSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid heartbeat interval"})
				return
			}
			beat = n
		}
		s.open(w, r, func(c *conn) {
			go c.heartbeat(time.Duration(beat)*time.Second, feedHeartbeat, true)
			// The page says which session it has open, when that changes; a
			// read ends when it goes.
			opened := make(chan string, 1)
			go func() {
				defer c.cancel()
				var m string
				for websocket.Message.Receive(c.ws, &m) == nil {
					var v struct {
						Open *string `json:"open"`
					}
					if json.Unmarshal([]byte(m), &v) != nil || v.Open == nil {
						continue
					}
					select {
					case opened <- *v.Open:
					case <-c.ctx.Done():
						return
					}
				}
			}()
			at := withOpen(path, r.URL.Query().Get("open"))
			var last [32]byte
			sent, again := false, false
			t := time.NewTicker(s.listEvery)
			defer t.Stop()
			for {
				a, ok := c.get(at)
				switch {
				case !ok || (sent && a.status != http.StatusOK):
					return
				case a.status == http.StatusOK && (!sent || again || a.sum != last):
					b, err := json.Marshal(map[string]any{"type": "sessions", "listing": json.RawMessage(a.body)})
					if err != nil {
						return
					}
					c.send(b)
					last, sent = a.sum, true
				}
				again = false
				select {
				case <-c.ctx.Done():
					return
				case v := <-opened:
					at, again = withOpen(path, v), true
				case <-t.C:
				}
			}
		})
	}
}

// withOpen is the path of a listing that has the session open as well.
func withOpen(path, open string) string {
	if open == "" || len(open) > 512 {
		return path
	}
	return path + "?open=" + url.QueryEscape(open)
}
