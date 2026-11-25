package pwbucket

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// The store clients. Each asks for one page of a flat listing (no
// delimiter, so a page is 1,000 keys wherever they are) or a range of one
// object; Store (store.go) pages, retries and renews.

// client is a bucket's store, read with one credential at a time.
type client interface {
	// page asks for the keys under prefix after the page token or, with no
	// token, after the key start.
	page(ctx context.Context, cr Cred, prefix, start, token string) (list listPage, err error)
	// folder asks for one page of what is directly under prefix, up to max.
	folder(ctx context.Context, cr Cred, prefix string, max int) (list listPage, err error)
	// get asks for an object from its first byte to the n-th; n < 0 is all
	// of it.
	get(ctx context.Context, cr Cred, key string, n int64) (*http.Response, error)
	// endpoint names the store, the same on every computer.
	endpoint() string
}

type listPage struct {
	entries []Entry
	// next asks for the page after this one; "" at the end.
	next string
}

// StoreError is the store's answer that a request failed.
type StoreError struct {
	Status        int
	Code, Message string
	region        string
}

func (e *StoreError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if e.Code != "" {
		return fmt.Sprintf("the store answered %s: %s", e.Code, msg)
	}
	return fmt.Sprintf("the store answered %d: %s", e.Status, msg)
}

// expired is whether the store refused the credential for its age.
func expired(err error) bool {
	var e *StoreError
	if !errors.As(err, &e) {
		return false
	}
	switch e.Code {
	case "ExpiredToken", "ExpiredTokenException", "TokenRefreshRequired", "InvalidToken":
		return true
	}
	return e.Status == http.StatusUnauthorized
}

// passing is whether a request is worth asking again: the store was busy,
// or the connection failed.
func passing(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var e *StoreError
	if errors.As(err, &e) {
		return e.Status >= 500 || e.Status == http.StatusTooManyRequests || e.Status == http.StatusRequestTimeout || e.Code == "SlowDown" || e.Code == "RequestTimeout"
	}
	var u *url.Error
	return errors.As(err, &u) || errors.Is(err, io.ErrUnexpectedEOF)
}

// newHTTP is the client the stores are asked with: through the proxy the
// environment names (HTTPS_PROXY, NO_PROXY), trusting what the Studio's
// other requests trust (internal/netx). A redirect is never
// followed, so a credential goes only to the host it was made for.
func newHTTP() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 16
	t.DialContext = (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	t.ResponseHeaderTimeout = 60 * time.Second
	return &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

var sharedHTTP = newHTTP()

// s3Client is a bucket of AWS S3.
type s3Client struct {
	hc     *http.Client
	bucket string
	mu     sync.Mutex
	in     string
	// base is another place to ask than the region's own (Store.Base).
	base   *url.URL
	signer *v4.Signer
	now    func() time.Time
}

func newS3(hc *http.Client, bucket, region string, base *url.URL) *s3Client {
	return &s3Client{hc: hc, bucket: bucket, in: region, base: base, now: time.Now,
		signer: v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })}
}

// fipsRegions have an S3 FIPS endpoint: the regions the pw CLI asks it in.
var fipsRegions = map[string]bool{"us-east-1": true, "us-east-2": true, "us-west-1": true, "us-west-2": true,
	"ca-central-1": true, "ca-west-1": true, "us-gov-east-1": true, "us-gov-west-1": true}

// useFIPS is whether a region's FIPS endpoint is asked: where there is one,
// as the pw CLI does, or as AWS_USE_FIPS_ENDPOINT says.
func useFIPS(region string) bool {
	switch strings.ToLower(os.Getenv("AWS_USE_FIPS_ENDPOINT")) {
	case "true":
		return true
	case "false":
		return false
	}
	return fipsRegions[region]
}

// domains are the AWS partitions whose hosts do not end in amazonaws.com,
// by how their regions' names start.
var domains = [][2]string{
	{"cn-", "amazonaws.com.cn"}, {"us-iso-", "c2s.ic.gov"}, {"us-isob-", "sc2s.sgov.gov"}, {"us-isof-", "csp.hci.ic.gov"},
	{"eu-isoe-", "cloud.adc-e.uk"}, {"eusc-", "amazonaws.eu"},
}

func s3Domain(region string) string {
	for _, d := range domains {
		if strings.HasPrefix(region, d[0]) {
			return d[1]
		}
	}
	return "amazonaws.com"
}

// partition tells AWS's partitions apart: a credential of one is nothing in
// another, so a bucket never moves between them.
func partition(region string) string {
	if strings.HasPrefix(region, "us-gov-") {
		return "us-gov"
	}
	return s3Domain(region)
}

// s3Host is the host of a region's S3.
func s3Host(region string, fips bool) string {
	name := "s3"
	if fips {
		name = "s3-fips"
	}
	return name + "." + region + "." + s3Domain(region)
}

// region is the region the bucket is asked in; move changes it, as the
// store says where the bucket is.
func (c *s3Client) region() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.in
}

func (c *s3Client) move(region string) {
	c.mu.Lock()
	c.in = region
	c.mu.Unlock()
}

// endpoint is the region's host whichever endpoint is asked, as the rclone
// route names the same store.
func (c *s3Client) endpoint() string { return s3Host(c.region(), false) }

// url is the bucket's address, with key below it. A bucket whose name has a
// dot is asked by path: the store's certificate does not cover it as a
// host. S3's FIPS endpoints answer no path, so it is asked at the plain one,
// as the pw CLI asks it.
func (c *s3Client) url(key string) *url.URL {
	region := c.region()
	u := &url.URL{Scheme: "https", Host: s3Host(region, useFIPS(region))}
	p := "/" + key
	switch {
	case c.base != nil:
		u.Scheme, u.Host = c.base.Scheme, c.base.Host
		p = strings.TrimSuffix(c.base.Path, "/") + "/" + c.bucket + p
	case strings.Contains(c.bucket, "."):
		u.Host = s3Host(region, false)
		p = "/" + c.bucket + p
	default:
		u.Host = c.bucket + "." + u.Host
	}
	u.Path, u.RawPath = p, escapeKey(p)
	return u
}

// escapeKey is a key as it goes in a path: every byte but the unreserved
// ones and the slash is escaped, as the signature takes it.
func escapeKey(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '/' || ch == '-' || ch == '_' || ch == '.' || ch == '~' || ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' {
			b.WriteByte(ch)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[ch>>4])
		b.WriteByte(hex[ch&15])
	}
	return b.String()
}

// emptySHA is the hash of a request with no body.
const emptySHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func (c *s3Client) do(ctx context.Context, cr Cred, u *url.URL, rng string) (*http.Response, error) {
	if cr.KeyID == "" || cr.Secret == "" {
		return nil, errors.New("pw buckets get-token gave no AWS credential for this bucket")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.URL = u
	req.Header.Set("X-Amz-Content-Sha256", emptySHA)
	// The object's bytes as they are kept, not as the transport would
	// unpack them.
	req.Header.Set("Accept-Encoding", "identity")
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	if err := c.signer.SignHTTP(ctx, aws.Credentials{AccessKeyID: cr.KeyID, SecretAccessKey: cr.Secret, SessionToken: cr.Session},
		req, emptySHA, "s3", c.region(), c.now()); err != nil {
		return nil, err
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return nil, plainURL(err)
	}
	if res.StatusCode/100 == 2 {
		return res, nil
	}
	defer res.Body.Close()
	e := &StoreError{Status: res.StatusCode, region: res.Header.Get("X-Amz-Bucket-Region")}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	var x struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if xml.Unmarshal(body, &x) == nil {
		e.Code, e.Message = x.Code, redact(x.Message, cr.secrets()...)
	}
	return nil, e
}

// plainURL is a request's failure without its address, whose query a
// signature could be in.
func plainURL(err error) error {
	var u *url.Error
	if errors.As(err, &u) {
		host := ""
		if p, perr := url.Parse(u.URL); perr == nil {
			host = "https://" + p.Host
		}
		return &url.Error{Op: u.Op, URL: host, Err: u.Err}
	}
	return err
}

func (c *s3Client) page(ctx context.Context, cr Cred, prefix, start, token string) (listPage, error) {
	q := url.Values{"list-type": {"2"}, "encoding-type": {"url"}, "max-keys": {"1000"}}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if token != "" {
		q.Set("continuation-token", token)
	} else if start != "" {
		q.Set("start-after", start)
	}
	return c.list(ctx, cr, q)
}

func (c *s3Client) folder(ctx context.Context, cr Cred, prefix string, max int) (listPage, error) {
	q := url.Values{"list-type": {"2"}, "encoding-type": {"url"}, "max-keys": {strconv.Itoa(max)}, "delimiter": {"/"}}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	return c.list(ctx, cr, q)
}

func (c *s3Client) list(ctx context.Context, cr Cred, q url.Values) (listPage, error) {
	u := c.url("")
	u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
	res, err := c.do(ctx, cr, u, "")
	if err != nil {
		return listPage{}, err
	}
	defer res.Body.Close()
	var x struct {
		XMLName               xml.Name `xml:"ListBucketResult"`
		EncodingType          string   `xml:"EncodingType"`
		IsTruncated           bool     `xml:"IsTruncated"`
		NextContinuationToken string   `xml:"NextContinuationToken"`
		Contents              []struct {
			Key          string    `xml:"Key"`
			Size         int64     `xml:"Size"`
			LastModified time.Time `xml:"LastModified"`
		} `xml:"Contents"`
		Folders []struct {
			Prefix string `xml:"Prefix"`
		} `xml:"CommonPrefixes"`
	}
	if err := xml.NewDecoder(io.LimitReader(res.Body, 64<<20)).Decode(&x); err != nil {
		if ctx.Err() != nil {
			return listPage{}, ctx.Err()
		}
		return listPage{}, fmt.Errorf("the store's listing could not be read: %w", io.ErrUnexpectedEOF)
	}
	if x.IsTruncated && x.NextContinuationToken == "" {
		return listPage{}, errors.New("the store's listing stopped before its end")
	}
	p := listPage{entries: make([]Entry, 0, len(x.Contents)+len(x.Folders))}
	if x.IsTruncated {
		p.next = x.NextContinuationToken
	}
	name := func(key string) (string, error) {
		if x.EncodingType != "url" {
			return key, nil
		}
		key, err := url.QueryUnescape(key)
		if err != nil {
			return "", errors.New("the store's listing names a key that could not be read")
		}
		return key, nil
	}
	for _, f := range x.Folders {
		key, err := name(f.Prefix)
		if err != nil {
			return listPage{}, err
		}
		p.entries = append(p.entries, Entry{Path: key, IsDir: true})
	}
	for _, o := range x.Contents {
		key, err := name(o.Key)
		if err != nil {
			return listPage{}, err
		}
		p.entries = append(p.entries, Entry{Path: key, Size: o.Size, ModTime: o.LastModified})
	}
	return p, nil
}

func (c *s3Client) get(ctx context.Context, cr Cred, key string, n int64) (*http.Response, error) {
	return c.do(ctx, cr, c.url(key), byteRange(n))
}

// byteRange asks for an object's first n bytes; "" for all of it.
func byteRange(n int64) string {
	if n < 0 {
		return ""
	}
	return "bytes=0-" + strconv.FormatInt(max(n, 1)-1, 10)
}
