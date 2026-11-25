package pwbucket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// gcsClient is a bucket of Google Cloud Storage, asked through its JSON
// interface with the access token.
type gcsClient struct {
	hc     *http.Client
	bucket string
	// base is another place to ask than Google's own (Store.Base).
	base string
}

const gcsHost = "storage.googleapis.com"

func (c *gcsClient) endpoint() string { return gcsHost }

func (c *gcsClient) at(p string, q url.Values) string {
	base := c.base
	if base == "" {
		base = "https://" + gcsHost
	}
	return strings.TrimSuffix(base, "/") + "/storage/v1/b/" + url.PathEscape(c.bucket) + "/o" + p + "?" + q.Encode()
}

func (c *gcsClient) do(ctx context.Context, cr Cred, u, rng string) (*http.Response, error) {
	if cr.Bearer == "" {
		return nil, errors.New("pw buckets get-token gave no Google access token for this bucket")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cr.Bearer)
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	// The object's bytes as they are kept.
	req.Header.Set("Accept-Encoding", "identity")
	res, err := c.hc.Do(req)
	if err != nil {
		return nil, plainURL(err)
	}
	if res.StatusCode/100 == 2 {
		return res, nil
	}
	defer res.Body.Close()
	e := &StoreError{Status: res.StatusCode}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	var x struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &x) == nil {
		e.Message = redact(x.Error.Message, cr.secrets()...)
	}
	return nil, e
}

func (c *gcsClient) page(ctx context.Context, cr Cred, prefix, start, token string) (listPage, error) {
	q := url.Values{"maxResults": {"1000"}, "fields": {"items(name,size,updated),nextPageToken"}}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if token != "" {
		q.Set("pageToken", token)
	} else if start != "" {
		// The offset takes in the key it names; Store drops it.
		q.Set("startOffset", start)
	}
	return c.list(ctx, cr, q)
}

func (c *gcsClient) folder(ctx context.Context, cr Cred, prefix string, max int) (listPage, error) {
	q := url.Values{"maxResults": {strconv.Itoa(max)}, "fields": {"items(name,size,updated),prefixes,nextPageToken"}, "delimiter": {"/"}}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	return c.list(ctx, cr, q)
}

func (c *gcsClient) list(ctx context.Context, cr Cred, q url.Values) (listPage, error) {
	res, err := c.do(ctx, cr, c.at("", q), "")
	if err != nil {
		return listPage{}, err
	}
	defer res.Body.Close()
	var x struct {
		Items []struct {
			Name    string    `json:"name"`
			Size    string    `json:"size"`
			Updated time.Time `json:"updated"`
		} `json:"items"`
		Prefixes []string `json:"prefixes"`
		Next     string   `json:"nextPageToken"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<20)).Decode(&x); err != nil {
		if ctx.Err() != nil {
			return listPage{}, ctx.Err()
		}
		return listPage{}, fmt.Errorf("the store's listing could not be read: %w", io.ErrUnexpectedEOF)
	}
	p := listPage{next: x.Next, entries: make([]Entry, 0, len(x.Items))}
	for _, f := range x.Prefixes {
		p.entries = append(p.entries, Entry{Path: f, IsDir: true})
	}
	for _, o := range x.Items {
		size, _ := strconv.ParseInt(o.Size, 10, 64)
		p.entries = append(p.entries, Entry{Path: o.Name, Size: size, ModTime: o.Updated})
	}
	return p, nil
}

func (c *gcsClient) get(ctx context.Context, cr Cred, key string, n int64) (*http.Response, error) {
	u := c.at("/"+strings.ReplaceAll(escapeKey(key), "/", "%2F"), url.Values{"alt": {"media"}})
	return c.do(ctx, cr, u, byteRange(n))
}
