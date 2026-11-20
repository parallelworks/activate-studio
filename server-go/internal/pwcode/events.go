package pwcode

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"

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
	d, ok := c.(Dialer)
	if !ok {
		return ErrNoStream
	}
	conn, err := d.Dial(ctx)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	defer conn.Close()
	route := sid(id) + "/events"
	if after > 0 {
		route += "?after=" + strconv.FormatInt(after, 10)
	}
	cfg, err := websocket.NewConfig("ws://pw-code"+route, "http://localhost/")
	if err != nil {
		return err
	}
	ws, err := websocket.NewClient(cfg, conn)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
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
		o := jsonv.Obj(v)
		if o == nil {
			continue
		}
		if !fn(o) {
			return nil
		}
	}
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
