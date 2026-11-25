package extractfiles

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
)

// SocketParam names the object in a request to a Socket.
const SocketParam = "object"

// Socket reads the listed files from the program that started this one: it
// answers each over a unix socket it listens on, so whatever it reads them
// with stays its own. As with Rclone, the mtime is taken as listed.
type Socket struct {
	// Path is the socket.
	Path string
	// Strip is the leading path removed from each listed path.
	Strip string
	// Tmp is the folder the objects are written to while read.
	Tmp string

	hc *http.Client
}

// NewSocket is a Socket over path.
func NewSocket(socket, strip, tmp string) *Socket {
	return &Socket{Path: socket, Strip: strip, Tmp: tmp, hc: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
		MaxIdleConnsPerHost: 16,
		DisableCompression:  true,
	}}}
}

// Fetch is Source.Fetch.
func (s *Socket) Fetch(ctx context.Context, e Entry) (string, int64, int64, func(), error) {
	rel := strings.TrimPrefix(path.Clean("/"+stripPath(e.Path, s.Strip)), "/")
	if rel == "" || rel == "." {
		return "", 0, 0, nil, errors.New("the listed path names no object")
	}
	f, err := os.CreateTemp(s.Tmp, "object-*"+path.Ext(e.Path))
	if err != nil {
		return "", 0, 0, nil, err
	}
	release := func() { os.Remove(f.Name()) }
	n, err := s.get(ctx, rel, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		release()
		if ctx.Err() != nil {
			return "", 0, 0, nil, ctx.Err()
		}
		return "", 0, 0, nil, err
	}
	return f.Name(), n, e.Mtime, release, nil
}

func (s *Socket) get(ctx context.Context, rel string, w io.Writer) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://studio/?"+url.Values{SocketParam: {rel}}.Encode(), nil)
	if err != nil {
		return 0, err
	}
	res, err := s.hc.Do(req)
	if err != nil {
		return 0, errors.New("reading the object from the Studio: " + err.Error())
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		if res.StatusCode == http.StatusNotFound {
			return 0, fmt.Errorf("%s: %w", rel, os.ErrNotExist)
		}
		return 0, errors.New(strings.TrimSpace(string(msg)))
	}
	n, err := io.Copy(w, res.Body)
	if err != nil {
		return n, errors.New("the object stopped part way: " + err.Error())
	}
	return n, nil
}
