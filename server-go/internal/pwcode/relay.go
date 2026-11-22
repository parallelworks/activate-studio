package pwcode

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/websocket"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// The platform relay: a pw code session on any of the person's machines,
// driven through ACTIVATE as the person, the way the platform's own Code
// page drives it, so the session keeps running where it is and nothing on
// its machine changes. Everything the Studio knows of the relay is in this
// file, read from the platform's source (parallelworks/core):
//
//   - cmd/ingress/internal/handlers/agents/routes.go and sessions.go: each
//     /api/code/machines/{machine}/sessions/{id}<suffix> route is passed to
//     the machine's daemon as /v1/sessions/{id}<suffix> with the same body
//     (controlSession), as the caller, whose credential picks the machines
//     they may reach. The machine's daemon decides what it allows: a
//     session above the machine's remote permission ceiling, or one another
//     host holds, is driven only from the machine (interrupt and stop
//     excepted).
//   - cmd/ingress/internal/handlers/agents/events_ws.go: the events are the
//     daemon's own /v1/sessions/{id}/events frames, on a WebSocket at
//     /ws/code/machines/{machine}/sessions/{id}/events?after=N&heartbeat=S.
//     The handshake takes the caller's Authorization header; an Origin, when
//     sent, must be one the platform allows (its own).
//   - cmd/cli/cmd/code/serverapi/types.go: the bodies (MessageRequest,
//     ApprovalAnswer for confirm, ask and plan, SessionDetail).
//   - packages/code/src/sessionEvents.ts: the platform Code page's own
//     events client (heartbeat 10).
//
// The routes it does not relay (attach, mcp, goal, state) answer as not
// offered here, so the Studio cannot give such a session its library tools.

// relayed are the session routes the relay passes on, by the suffix after
// /v1/sessions/{id}: "" is the session's detail.
var relayed = map[string]bool{
	"": true, "/transcript": true, "/messages": true, "/direction": true,
	"/interrupt": true, "/stop": true, "/approvals": true,
}

// relayHeartbeat is the heartbeat the platform Code page asks of the feed.
const relayHeartbeat = 10

// RelayClient drives the sessions on one machine through the platform at
// origin, with key: the person's own ACTIVATE credential, never the
// Studio's account.
type RelayClient struct {
	origin, key, machine string
}

// Relay is a client for the person's sessions on machine (its platform id).
func Relay(origin, key, machine string) *RelayClient {
	return &RelayClient{origin: strings.TrimRight(origin, "/"), key: key, machine: machine}
}

// Site is where the client's sessions run, as the Studio names a relayed
// machine: "relay:" and its platform id.
func (c *RelayClient) Site() string { return RelaySite(c.machine) }

// RelaySite is the site name of a machine reached through the relay.
func RelaySite(machine string) string { return "relay:" + machine }

// Machine is the platform id of the client's machine.
func (c *RelayClient) Machine() string { return c.machine }

// relayPath is the platform path for a daemon route, or "" for a route the
// relay does not offer.
func (c *RelayClient) relayPath(route string) string {
	rest, ok := strings.CutPrefix(route, "/v1/sessions/")
	if !ok {
		return ""
	}
	query := ""
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		rest, query = rest[:i], rest[i:]
	}
	id, suffix := rest, ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		id, suffix = rest[:i], rest[i:]
	}
	base := suffix
	if strings.HasPrefix(suffix, "/approvals/") {
		base = "/approvals"
	}
	if id == "" || !relayed[base] {
		return ""
	}
	return "/api/code/machines/" + platform.EncodeURIComponent(c.machine) + "/sessions/" + id + suffix + query
}

// relayDoer sends a daemon request to the platform instead.
type relayDoer struct{ c *RelayClient }

func (d relayDoer) Do(req *http.Request) (*http.Response, error) {
	path := d.c.relayPath(req.URL.RequestURI())
	u, err := url.Parse(d.c.origin + path)
	if err != nil {
		return nil, err
	}
	req.URL, req.Host = u, u.Host
	req.Header.Set("Authorization", "Bearer "+d.c.key)
	return platform.HTTPClient.Do(req)
}

// Call is one daemon request, sent through the relay.
func (c *RelayClient) Call(ctx context.Context, method, route string, body any, timeout time.Duration) (any, error) {
	if c.origin == "" {
		return nil, &DaemonError{Message: "This Studio is not connected to an ACTIVATE platform, which carries messages to sessions on other machines.", Status: http.StatusConflict}
	}
	if c.relayPath(route) == "" {
		return nil, &DaemonError{Message: "ACTIVATE does not offer this for a session on another machine yet.", Status: http.StatusNotImplemented}
	}
	v, err := Request(ctx, relayDoer{c}, method, route, body, timeout)
	var de *DaemonError
	if errors.As(err, &de) {
		return nil, relayError(de)
	}
	return v, err
}

// relayError says a relay refusal in the person's terms.
func relayError(de *DaemonError) error {
	said := strings.TrimPrefix(de.Message, "pw code daemon: ")
	switch de.Status {
	case http.StatusUnauthorized:
		return &DaemonError{Status: de.Status, Message: "ACTIVATE refused your credential; sign in again, or renew your API key under Settings, Model access."}
	case http.StatusForbidden:
		return &DaemonError{Status: de.Status, Message: "The session's machine does not take this from ACTIVATE (" + said + "). Answer it where the session runs."}
	case 0:
		return &DaemonError{Message: "ACTIVATE did not answer: " + said}
	}
	return &DaemonError{Status: de.Status, Message: said}
}

// Stream opens the session's event feed through the platform, after seq
// after (0 for all of it).
func (c *RelayClient) Stream(ctx context.Context, id string, after int64) (*websocket.Conn, error) {
	if c.origin == "" {
		return nil, ErrNoStream
	}
	u, err := url.Parse(c.origin)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	u.Path = "/ws/code/machines/" + url.PathEscape(c.machine) + "/sessions/" + url.PathEscape(id) + "/events"
	u.RawQuery = "after=" + strconv.FormatInt(after, 10) + "&heartbeat=" + strconv.Itoa(relayHeartbeat)
	cfg, err := websocket.NewConfig(u.String(), c.origin)
	if err != nil {
		return nil, err
	}
	cfg.Header = http.Header{"Authorization": {"Bearer " + c.key}}
	if u.Scheme == "wss" {
		cfg.TlsConfig = &tls.Config{}
		if t, ok := platform.HTTPClient.Transport.(*http.Transport); ok && t.TLSClientConfig != nil {
			cfg.TlsConfig = t.TLSClientConfig.Clone()
		}
		cfg.TlsConfig.ServerName = u.Hostname()
		// The handshake is HTTP/1.1: a connection that agrees on HTTP/2,
		// which the platform offers, answers it in frames.
		cfg.TlsConfig.NextProtos = []string{"http/1.1"}
	}
	host := u.Host
	if u.Port() == "" {
		if u.Scheme == "wss" {
			host = net.JoinHostPort(u.Hostname(), "443")
		} else {
			host = net.JoinHostPort(u.Hostname(), "80")
		}
	}
	var d net.Dialer
	raw, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, &DaemonError{Message: "ACTIVATE did not answer: " + err.Error()}
	}
	var conn io.ReadWriteCloser = raw
	if u.Scheme == "wss" {
		tc := tls.Client(raw, cfg.TlsConfig)
		if err := tc.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, &DaemonError{Message: "ACTIVATE did not answer: " + err.Error()}
		}
		conn = tc
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	ws, err := websocket.NewClient(cfg, conn)
	stop()
	if err != nil {
		conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &DaemonError{Message: "ACTIVATE did not open the session's events: " + err.Error()}
	}
	return ws, nil
}

// Streamer is a client whose event feed is its own WebSocket.
type Streamer interface {
	Stream(ctx context.Context, id string, after int64) (*websocket.Conn, error)
}

var _ Streamer = (*RelayClient)(nil)

// receive reads frames from ws until fn returns false or the feed ends.
func receive(ctx context.Context, ws *websocket.Conn, fn func(ev *jsonv.Object) bool) error {
	ws.MaxPayloadBytes = 32 << 20
	for {
		var msg string
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		v, err := jsonv.Decode([]byte(msg))
		if err != nil {
			continue
		}
		if o := jsonv.Obj(v); o != nil && !fn(o) {
			return nil
		}
	}
}
