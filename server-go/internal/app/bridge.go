package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/bridge"
)

// The Node bridge is TRANSITIONAL. It exists only so the Go file routes can
// reach two things of Node's that no Node route exposes: the index queue
// (indexing.ts's incrementalIndexDir and indexRootDb, with their run
// time) and the caches built from the index (invalidateDbList,
// invalidateContext, invalidateTagMaps). It goes away once the index track
// fills IndexDirMS, IndexRootDB and Changed, and the release is Node-free.
// Nothing else is to be routed through it.
//
// server/src/goBridge.ts answers POST /internal/go/index and
// /internal/go/changed when the request carries internal/bridge's secret;
// the Go front refuses BridgePrefix to clients (bridge.RegisterPrivatePrefix).

// BridgePrefix is these calls' path on the Node server.
const BridgePrefix = "/internal/go/"

func init() { bridge.RegisterPrivatePrefix(BridgePrefix) }

type nodeBridge struct {
	base   *url.URL
	client *http.Client
}

// Index jobs wait behind each other, and a large subtree takes minutes.
func newBridge(base *url.URL) *nodeBridge {
	return &nodeBridge{base: base, client: &http.Client{Timeout: 6 * time.Hour}}
}

func (b *nodeBridge) post(ctx context.Context, path string, body any, out any) error {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", b.base.JoinPath(path).String(), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	bridge.Sign(req)
	req.Header.Set(InternalHeader, "1")
	res, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	ans, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var r struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	json.Unmarshal(ans, &r)
	if res.StatusCode != 200 || !r.OK {
		if r.Error != "" {
			return errors.New(r.Error)
		}
		return fmt.Errorf("node bridge answered %d", res.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(ans, out)
	}
	return nil
}

// index runs an index job in Node. It is not cancelled with the request
// that asked for it, as Node's own routes do not cancel theirs.
func (b *nodeBridge) index(ctx context.Context, op, rel string) (int64, error) {
	var out struct {
		MS int64 `json:"ms"`
	}
	err := b.post(context.WithoutCancel(ctx), BridgePrefix+"index", map[string]string{"op": op, "path": rel}, &out)
	return out.MS, err
}

func (b *nodeBridge) changed(ctx context.Context, what []string) {
	b.post(context.WithoutCancel(ctx), BridgePrefix+"changed", map[string]any{"what": what}, nil)
}
