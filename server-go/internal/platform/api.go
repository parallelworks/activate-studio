package platform

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/netx"
)

// HTTPClient is the client every outbound call of the platform track uses:
// the environment's proxy, the system's certificates, and no redirect
// following unless a caller asks (fetch's redirect: 'manual' is the relay's).
var HTTPClient = &http.Client{Transport: transport()}

func transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 16
	return t
}

// Response is a platform answer: its status and parsed body (nil when it
// was not JSON), and the raw bytes.
type Response struct {
	OK     bool
	Status int
	Body   any
	Raw    []byte
	Header http.Header
}

// Request is fetch(url, {method, headers, body, signal: AbortSignal.timeout}).
func Request(ctx context.Context, method, url string, header map[string]string, body io.Reader, timeout time.Duration) (*http.Response, context.CancelFunc, error) {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	req, err := http.NewRequestWithContext(cctx, method, url, body)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := HTTPClient.Do(req)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return res, cancel, nil
}

// Get fetches url and reads the whole body.
func Get(ctx context.Context, url string, header map[string]string, timeout time.Duration) (*Response, error) {
	res, cancel, err := Request(ctx, http.MethodGet, url, header, nil, timeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 256<<20))
	if err != nil {
		return nil, err
	}
	r := &Response{OK: res.StatusCode >= 200 && res.StatusCode < 300, Status: res.StatusCode, Raw: b, Header: res.Header}
	if v, err := jsonv.Decode(b); err == nil {
		r.Body = v
	}
	return r, nil
}

// APIFetch is platformFetch: a platform API GET with a key, with status and
// parsed body; nil when the platform is unreachable.
func (p *Platform) APIFetch(ctx context.Context, path, key string, timeout time.Duration) *Response {
	origin := p.Origin()
	if origin == "" {
		return nil
	}
	r, err := Get(ctx, origin+path, map[string]string{"Authorization": "Bearer " + key}, timeout)
	if err != nil {
		return nil
	}
	return r
}

// APIJSON is platformJson: a platform API read with a key, or nil when it
// cannot be had (no key, unreachable, an error status, not JSON).
func (p *Platform) APIJSON(ctx context.Context, path, key string, timeout time.Duration) any {
	if key == "" {
		return nil
	}
	r := p.APIFetch(ctx, path, key, timeout)
	if r == nil || !r.OK {
		return nil
	}
	return r.Body
}

// WorkflowRecord is workflowRecord: a workflow's record from the API, or
// nil when the API cannot give one with its yaml.
func (p *Platform) WorkflowRecord(ctx context.Context, name, key string) *jsonv.Object {
	doc := jsonv.Obj(p.APIJSON(ctx, "/api/workflows/"+EncodeURIComponent(name), key, 0))
	if doc == nil {
		return nil
	}
	if y, _ := doc.Get("yaml"); !jsonv.Truthy(y) {
		return nil
	}
	return doc
}

// EncodeURIComponent is JavaScript's encodeURIComponent.
func EncodeURIComponent(s string) string {
	const keep = "-_.!~*'()"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(keep, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte("0123456789ABCDEF"[c>>4])
		b.WriteByte("0123456789ABCDEF"[c&15])
	}
	return b.String()
}

/* ---- reaching the platform ---- */

// Reach is platformReachability's answer.
type Reach struct {
	OK      bool
	Message string
}

type reachState struct {
	mu      sync.Mutex
	origin  string
	at      time.Time
	ok      bool
	message string
	have    bool
	flight  chan struct{}
}

func (r *reachState) reset() { r.mu.Lock(); r.have = false; r.mu.Unlock() }

// Reachability is platformReachability: whether the platform answers at
// all (any HTTP answer counts), kept 30 seconds, 10 when down.
func (p *Platform) Reachability(ctx context.Context) Reach {
	origin := p.Origin()
	host := p.Host()
	if origin == "" {
		return Reach{OK: true}
	}
	r := p.reach
	for {
		r.mu.Lock()
		keep := 30 * time.Second
		if !r.ok {
			keep = 10 * time.Second
		}
		if r.have && r.origin == origin && time.Since(r.at) < keep {
			out := Reach{OK: r.ok, Message: r.message}
			r.mu.Unlock()
			return out
		}
		if r.flight != nil {
			ch := r.flight
			r.mu.Unlock()
			<-ch
			r.mu.Lock()
			out := Reach{OK: r.ok, Message: r.message}
			r.mu.Unlock()
			return out
		}
		ch := make(chan struct{})
		r.flight = ch
		r.mu.Unlock()
		req, _ := http.NewRequest(http.MethodHead, origin, nil)
		cctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		client := &http.Client{Transport: HTTPClient.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		res, err := client.Do(req.WithContext(cctx))
		cancel()
		ok, msg := true, ""
		if err != nil {
			if conn := netx.ConnectionProblem(err, host, p.Getenv); conn != "" {
				ok, msg = false, conn
			} else if f := netx.Failure(err); f != nil {
				ok, msg = false, netx.UnreachableMessage(host, f)
			}
		} else {
			res.Body.Close()
		}
		r.mu.Lock()
		r.origin, r.at, r.ok, r.message, r.have, r.flight = origin, time.Now(), ok, msg, true, nil
		r.mu.Unlock()
		close(ch)
		return Reach{OK: ok, Message: msg}
	}
}
