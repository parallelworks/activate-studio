package pwcode

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"time"

	"golang.org/x/net/websocket"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// A session's events come over a WebSocket at /v1/sessions/:id/events:
// one JSON object per message, each with a seq and a type (state,
// user_message, reasoning, chunk, tool_start, tool_end, approval, done,
// error and others). ?after=SEQ starts after that event; without it the
// daemon replays the session from its start. A remote daemon's stream
// goes over a pipe of its own, so the bridge's one connection stays free
// for requests.

// Dialer is a client that can open a connection of its own to its
// daemon, which carries the event stream.
type Dialer interface {
	Dial(ctx context.Context) (io.ReadWriteCloser, error)
}

// Dial is a new connection to this host's daemon socket.
func (l *localClient) Dial(ctx context.Context) (io.ReadWriteCloser, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", l.s.SocketPath())
}

// Dial starts a `pw ssh … pw code daemon proxy --ensure` of its own and
// gives its stdio as the connection.
func (r *remoteClient) Dial(ctx context.Context) (io.ReadWriteCloser, error) {
	r.s.bridges.mu.Lock()
	spawn := r.s.bridges.spawn
	r.s.bridges.mu.Unlock()
	// A bridge of its own, so its stderr is not the request bridge's.
	b := &bridge{id: r.id + "|events", resource: r.resource}
	return b.open(spawn(r.resource, r.key))
}

// ErrNoStream is a client that cannot open an event stream.
var ErrNoStream = errors.New("this pw code daemon offers no event stream here")

// Follow reads session id's events after seq after (0 for all), calling
// fn with each until fn returns false, the stream ends or ctx ends.
func Follow(ctx context.Context, c Client, id string, after int64, fn func(ev *jsonv.Object) bool) error {
	ws, err := OpenEvents(ctx, c, id, after)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { ws.Close() })
	defer stop()
	defer ws.Close()
	idle := time.Duration(0)
	if _, ok := c.(Streamer); ok {
		idle = relayIdle
	}
	return receive(ctx, ws, fn, idle)
}

// Heartbeats is whether c's event feed sends a heartbeat frame of its own
// (the platform's), so its silence can be timed.
func Heartbeats(c Client) bool { _, ok := c.(Streamer); return ok }

// OpenEvents opens session id's event feed after seq after: the platform's
// WebSocket for a relayed session, else the daemon's own over a connection
// of its own. The caller closes it.
func OpenEvents(ctx context.Context, c Client, id string, after int64) (*websocket.Conn, error) {
	if st, ok := c.(Streamer); ok {
		return st.Stream(ctx, id, after)
	}
	d, ok := c.(Dialer)
	if !ok {
		return nil, ErrNoStream
	}
	conn, err := d.Dial(ctx)
	if err != nil {
		return nil, err
	}
	route := sid(id) + "/events"
	if after > 0 {
		route += "?after=" + strconv.FormatInt(after, 10)
	}
	cfg, err := websocket.NewConfig("ws://pw-code"+route, "http://localhost/")
	if err != nil {
		conn.Close()
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	ws, err := websocket.NewClient(cfg, conn)
	stop()
	if err != nil {
		conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return ws, nil
}

// Seq is an event's seq, or 0.
func Seq(ev *jsonv.Object) int64 {
	v, _ := ev.Get("seq")
	n, ok := jsonv.Num(v)
	if !ok {
		return 0
	}
	return int64(n)
}
