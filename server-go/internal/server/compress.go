package server

import (
	"bytes"
	"compress/gzip"
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/andybalholm/brotli"
)

// Static files of the web app are sent compressed when the browser accepts
// it: brotli, else gzip. Each file is compressed once, on its first request
// (the page's entry script and style sheet right after start), and kept in
// memory keyed by its path, size and modification time, so a rebuild of
// the web app is picked up as before. Types that are compressed already
// (images, woff2) and small files go out as they are.

// compressibleExt are the web app's text and uncompressed binary types.
var compressibleExt = map[string]bool{
	".js": true, ".mjs": true, ".css": true, ".html": true, ".svg": true,
	".json": true, ".webmanifest": true, ".map": true, ".txt": true,
	".xml": true, ".wasm": true, ".ttf": true, ".otf": true,
}

// minCompress is the smallest file worth compressing: below it the saving
// is less than a packet.
const minCompress = 1024

// maxCompressCache bounds the compressed bytes kept; past it the cache
// starts over (a long-running server through many rebuilds).
const maxCompressCache = 128 << 20

// Brotli quality 6 and gzip level 6 compress the 2.7 MB entry script in
// about 0.1 s each; the higher levels gain a few percent for several times
// the time.
const (
	brotliQuality = 6
	gzipLevel     = 6
)

func compressible(file string, size int64) bool {
	return size >= minCompress && compressibleExt[strings.ToLower(filepath.Ext(file))]
}

// negotiate picks the encoding for an Accept-Encoding header: br, gzip, or
// "" for none. A q of 0 refuses an encoding; at equal q brotli wins.
func negotiate(accept string) string {
	qbr, qgz, qany := -1.0, -1.0, -1.0
	for _, part := range strings.Split(accept, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
			if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = f
				}
			}
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "br":
			qbr = q
		case "gzip", "x-gzip":
			qgz = q
		case "*":
			qany = q
		}
	}
	if qbr < 0 {
		qbr = qany
	}
	if qgz < 0 {
		qgz = qany
	}
	switch {
	case qbr > 0 && qbr >= qgz:
		return "br"
	case qgz > 0:
		return "gzip"
	}
	return ""
}

// encodedETag is the file's ETag with the encoding in it, so a cache never
// takes one encoding's bytes for another's.
func encodedETag(size, mtimeMs int64, enc string) string {
	if enc == "gzip" {
		enc = "gz"
	}
	return strings.TrimSuffix(etag(size, mtimeMs), `"`) + "-" + enc + `"`
}

type compressKey struct {
	file    string
	size    int64
	mtimeNs int64
	enc     string
}

type compressed struct {
	once  sync.Once
	data  []byte
	ctype string
	err   error
}

type compressCache struct {
	mu    sync.Mutex
	m     map[compressKey]*compressed
	bytes int64
}

func newCompressCache() *compressCache {
	return &compressCache{m: map[compressKey]*compressed{}}
}

var errChanged = errors.New("file changed while it was compressed")

// get returns the file compressed with enc and its content type, computing
// it once however many requests ask at the same time.
func (c *compressCache) get(file string, st os.FileInfo, enc string) ([]byte, string, error) {
	k := compressKey{file, st.Size(), st.ModTime().UnixNano(), enc}
	c.mu.Lock()
	e, ok := c.m[k]
	if !ok {
		e = &compressed{}
		c.m[k] = e
	}
	c.mu.Unlock()
	e.once.Do(func() {
		e.data, e.ctype, e.err = compressFile(file, st, enc)
		c.mu.Lock()
		if e.err != nil {
			delete(c.m, k)
		} else if c.bytes += int64(len(e.data)); c.bytes > maxCompressCache {
			c.m, c.bytes = map[compressKey]*compressed{k: e}, int64(len(e.data))
		}
		c.mu.Unlock()
	})
	return e.data, e.ctype, e.err
}

func compressFile(file string, st os.FileInfo, enc string) ([]byte, string, error) {
	plain, err := os.ReadFile(file)
	if err != nil {
		return nil, "", err
	}
	if now, err := os.Stat(file); err != nil || now.Size() != st.Size() || !now.ModTime().Equal(st.ModTime()) || int64(len(plain)) != st.Size() {
		return nil, "", errChanged
	}
	// The type http.ServeContent would give the file sent as it is.
	ctype := mime.TypeByExtension(filepath.Ext(file))
	if ctype == "" {
		ctype = http.DetectContentType(plain[:min(512, len(plain))])
	}
	var buf bytes.Buffer
	buf.Grow(len(plain) / 3)
	switch enc {
	case "br":
		w := brotli.NewWriterLevel(&buf, brotliQuality)
		w.Write(plain)
		err = w.Close()
	case "gzip":
		w, _ := gzip.NewWriterLevel(&buf, gzipLevel)
		w.Write(plain)
		err = w.Close()
	default:
		err = errors.New("unknown encoding " + enc)
	}
	if err != nil {
		return nil, "", err
	}
	return bytes.Clone(buf.Bytes()), ctype, nil
}

var entryAsset = regexp.MustCompile(`(?:src|href)="(/assets/[^"?#]+)"`)

// warm compresses the scripts and style sheets index.html names, in both
// encodings, so the first page load does not wait for them.
func (c *compressCache) warm(dist string) {
	html, err := os.ReadFile(filepath.Join(dist, "index.html"))
	if err != nil {
		return
	}
	for _, m := range entryAsset.FindAllSubmatch(html, -1) {
		file := filepath.Join(dist, filepath.FromSlash(string(m[1])))
		st, err := os.Stat(file)
		if err != nil || !st.Mode().IsRegular() || !compressible(file, st.Size()) {
			continue
		}
		for _, enc := range []string{"br", "gzip"} {
			c.get(file, st, enc)
		}
	}
}
