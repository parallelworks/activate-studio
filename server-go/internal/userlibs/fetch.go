package userlibs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// fetch_embed.py's --download and --download-readers, the part of it the
// Studio runs on its own machine: the pinned files of embed_deps.json into
// a cache, under the names the script gives them, each checked against its
// SHA-256. The other system still runs the script, with that cache and no
// network, so the cache here is what the script would have left: the same
// files under the same names and nothing else.

// fetchIdle is how long a download may go without a byte, as the script's
// urlopen(timeout=120) allows each connection and read.
var fetchIdle = 120 * time.Second

// fetchTotal bounds the whole of one sending's downloads.
const fetchTotal = 15 * time.Minute

var fetchClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 120 * time.Second}).DialContext,
		TLSHandshakeTimeout:   120 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
		// The script asks for the bytes as they are stored.
		DisableCompression: true,
		ForceAttemptHTTP2:  true,
	},
}

// fetchMu keeps two sendings from writing the same .part file at once.
var fetchMu sync.Mutex

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// idleReader cancels its request when no byte has come for fetchIdle.
type idleReader struct {
	r     io.Reader
	timer *time.Timer
}

func (i *idleReader) Read(p []byte) (int, error) {
	n, err := i.r.Read(p)
	if n > 0 {
		i.timer.Reset(fetchIdle)
	}
	return n, err
}

// fetchPinned is fetch(): a pinned download, from the cache when it is
// already there and intact. Its errors are the script's exit messages.
func fetchPinned(ctx context.Context, p *jsonv.Object, cache string) error {
	url, want := jsonv.String(get(p, "url")), jsonv.String(get(p, "sha256"))
	if err := os.MkdirAll(cache, 0o777); err != nil {
		return err
	}
	dest := filepath.Join(cache, cacheName(p))
	if st, err := os.Stat(dest); err == nil && !st.IsDir() {
		if got, err := fileSHA256(dest); err == nil && got == want {
			return nil
		}
	}
	part := dest + ".part"
	couldNot := func(why string) error {
		os.Remove(part)
		return fmt.Errorf("could not download %s: %s. Without network, search by meaning keeps using the older model.", url, why)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	idle := time.AfterFunc(fetchIdle, cancel)
	defer idle.Stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return couldNot(err.Error())
	}
	req.Header.Set("Accept-Encoding", "identity")
	res, err := fetchClient.Do(req)
	if err != nil {
		return couldNot(err.Error())
	}
	defer res.Body.Close()
	// urlopen answers only a 2xx, after redirects.
	if res.StatusCode < 200 || res.StatusCode > 299 {
		reason := strings.TrimPrefix(res.Status, strconv.Itoa(res.StatusCode)+" ")
		return couldNot(fmt.Sprintf("HTTP Error %d: %s", res.StatusCode, reason))
	}
	f, err := os.Create(part)
	if err != nil {
		return couldNot(err.Error())
	}
	_, err = io.Copy(f, &idleReader{r: res.Body, timer: idle})
	// A body cut short is taken as it came, as the script's read does: its
	// digest then fails the pin below.
	if errors.Is(err, io.ErrUnexpectedEOF) {
		err = nil
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if ctx.Err() != nil {
			return couldNot("timed out")
		}
		return couldNot(err.Error())
	}
	got, err := fileSHA256(part)
	if err != nil {
		return couldNot(err.Error())
	}
	if got != want {
		os.Remove(part)
		return fmt.Errorf("%s has SHA-256 %s, not the pinned %s; nothing was installed.", url, got, want)
	}
	return os.Rename(part, dest)
}

// fetchAll fetches files in order, stopping at the first that fails.
func fetchAll(ctx context.Context, files []*jsonv.Object, cache string) error {
	for _, p := range files {
		if err := fetchPinned(ctx, p, cache); err != nil {
			return err
		}
	}
	return nil
}

func values(o *jsonv.Object) []*jsonv.Object {
	if o == nil {
		return nil
	}
	out := []*jsonv.Object{}
	for _, k := range o.Keys() {
		out = append(out, jsonv.Obj(get(o, k)))
	}
	return out
}

// downloadPlatform is --download --platform: the wheels for a platform and
// the model files, into the cache only.
func (m *Module) downloadPlatform(ctx context.Context, platform, cache string) error {
	wheels := jsonv.Obj(get(jsonv.Obj(get(m.pins(), "wheels")), platform))
	if wheels == nil || len(wheels.Keys()) == 0 {
		return fmt.Errorf("no pinned wheels for %s", platform)
	}
	files := values(wheels)
	files = append(files, values(jsonv.Obj(get(jsonv.Obj(get(m.pins(), "model")), "files")))...)
	return fetchAll(ctx, files, cache)
}

// downloadReaders is --download-readers: the document readers' wheels, into
// the cache only.
func (m *Module) downloadReaders(ctx context.Context, cache string) error {
	return fetchAll(ctx, values(jsonv.Obj(get(m.pins(), "readers"))), cache)
}

// download fetches what is missing here into the Studio's own folder, with
// platform "" for the document readers; why not, or "".
func (m *Module) download(platform string) string {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTotal)
	defer cancel()
	fetchMu.Lock()
	defer fetchMu.Unlock()
	cache := filepath.Join(m.IndexBase, "embed-wheels")
	var err error
	if platform == "" {
		err = m.downloadReaders(ctx, cache)
	} else {
		err = m.downloadPlatform(ctx, platform, cache)
	}
	if err != nil {
		return err.Error()
	}
	return ""
}
