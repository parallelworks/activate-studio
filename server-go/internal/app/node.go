package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
)

// InternalHeader marks a request the Go server makes to Node on behalf of
// one of its own routes (a value whose owner is not ported yet). The test
// harness's strict mode (server/test/frontFastify.mjs) counts any other
// request Node receives for a route Go serves as a failure.
const InternalHeader = "X-Studio-Internal"

// Node calls the Node server behind the Go server, as the browser's
// request would: with the same identity (the front's hand-off token, or
// the identity headers passed through where the front is off).
type Node struct {
	Base   *url.URL
	Front  *auth.Front // nil: identity headers are copied from the request
	Header string      // AUTH_HEADER, copied with the cookies where Front is nil
	Client *http.Client
}

// NewNode is the caller for the Node server at base.
func NewNode(base *url.URL, front *auth.Front, authHeader string) *Node {
	return &Node{Base: base, Front: front, Header: authHeader, Client: &http.Client{Timeout: 30 * time.Second}}
}

// Do sends method path (with its query) to Node for the request r and
// decodes a JSON answer into out (nil: discarded). A status of 400 or
// more is an error carrying Node's status and body.
func (n *Node) Do(r *http.Request, method, path string, body any, out any) error {
	if n == nil {
		return fmt.Errorf("no Node server")
	}
	ctx := context.Background()
	if r != nil {
		ctx = r.Context()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	u := n.Base.JoinPath("/")
	ref, err := url.Parse(path)
	if err != nil {
		return err
	}
	u = u.ResolveReference(ref)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(InternalHeader, "1")
	if r != nil {
		req.Host = r.Host
		if n.Front != nil {
			n.Front.ToNode(req, auth.UserOf(r))
		} else {
			for _, h := range []string{n.Header, "Cookie", "Authorization"} {
				if h != "" && r.Header.Get(h) != "" {
					req.Header.Set(h, r.Header.Get(h))
				}
			}
		}
	}
	if r != nil {
		RunToNode(r, req)
	}
	res, err := n.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 400 {
		return &NodeError{Status: res.StatusCode, Body: b}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

// NodeError is an error answer from Node.
type NodeError struct {
	Status int
	Body   []byte
}

func (e *NodeError) Error() string { return fmt.Sprintf("node answered %d: %s", e.Status, e.Body) }
