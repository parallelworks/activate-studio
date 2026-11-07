package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
)

func TestNegotiate(t *testing.T) {
	for accept, want := range map[string]string{
		"":                           "",
		"gzip, deflate, br, zstd":    "br",
		"gzip":                       "gzip",
		"br;q=0, gzip":               "gzip",
		"gzip;q=1.0, br;q=0.5":       "gzip",
		"br;q=0.8, gzip;q=0.8":       "br",
		"*":                          "br",
		"*;q=0":                      "",
		"identity":                   "",
		"deflate, x-gzip":            "gzip",
		" BR ; q=0.3 , gzip ; q=0 ":  "br",
		"gzip;q=0, br;q=0, identity": "",
	} {
		if got := negotiate(accept); got != want {
			t.Errorf("negotiate(%q) = %q, want %q", accept, got, want)
		}
	}
}

// bigDist is a web app whose script and style sheet are worth compressing,
// beside a font that is compressed already and a small file.
func bigDist(t *testing.T) (dist string, js []byte) {
	t.Helper()
	dist = webDist(t)
	js = []byte(strings.Repeat("export function f(x) { return x * 2 }\n", 2000))
	os.WriteFile(filepath.Join(dist, "assets", "index-Big12345.js"), js, 0o644)
	os.WriteFile(filepath.Join(dist, "assets", "index-Big12345.css"), []byte(strings.Repeat(".a { color: red }\n", 500)), 0o644)
	os.WriteFile(filepath.Join(dist, "assets", "font-Big12345.woff2"), bytes.Repeat([]byte{1, 2, 3}, 2000), 0o644)
	os.WriteFile(filepath.Join(dist, "assets", "data-Big12345.json"), []byte(strings.Repeat(`{"a":1}`, 400)), 0o644)
	os.WriteFile(filepath.Join(dist, "index.html"), []byte(`<html><script src="/assets/index-Big12345.js"></script><link href="/assets/index-Big12345.css"></html>`), 0o644)
	return dist, js
}

func fetch(t *testing.T, method, u string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, u, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	// A bare Transport: it would otherwise ask for gzip itself and undo it.
	res, err := (&http.Transport{DisableCompression: true}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, b
}

func TestStaticCompression(t *testing.T) {
	dist, js := bigDist(t)
	srv := front(t, dist, nil)
	u := srv.URL + "/assets/index-Big12345.js"

	plain, body := fetch(t, "GET", u, nil)
	if plain.Header.Get("Content-Encoding") != "" || !bytes.Equal(body, js) {
		t.Fatalf("without Accept-Encoding: encoding %q, %d bytes", plain.Header.Get("Content-Encoding"), len(body))
	}
	if plain.Header.Get("Vary") != "Accept-Encoding" {
		t.Fatalf("identity Vary %q", plain.Header.Get("Vary"))
	}
	etags := map[string]string{"": plain.Header.Get("ETag")}

	for enc, decode := range map[string]func([]byte) ([]byte, error){
		"br": func(b []byte) ([]byte, error) { return io.ReadAll(brotli.NewReader(bytes.NewReader(b))) },
		"gzip": func(b []byte) ([]byte, error) {
			r, err := gzip.NewReader(bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			return io.ReadAll(r)
		},
	} {
		res, body := fetch(t, "GET", u, map[string]string{"Accept-Encoding": enc})
		if res.StatusCode != 200 || res.Header.Get("Content-Encoding") != enc {
			t.Fatalf("%s: %d, encoding %q", enc, res.StatusCode, res.Header.Get("Content-Encoding"))
		}
		if res.Header.Get("Vary") != "Accept-Encoding" || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/javascript") {
			t.Fatalf("%s: Vary %q, type %q", enc, res.Header.Get("Vary"), res.Header.Get("Content-Type"))
		}
		if res.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Fatalf("%s: Cache-Control %q", enc, res.Header.Get("Cache-Control"))
		}
		if res.ContentLength != int64(len(body)) || len(body) >= len(js)/4 {
			t.Fatalf("%s: length %d, body %d of %d", enc, res.ContentLength, len(body), len(js))
		}
		got, err := decode(body)
		if err != nil || !bytes.Equal(got, js) {
			t.Fatalf("%s: does not decode to the file (%v)", enc, err)
		}
		tag := res.Header.Get("ETag")
		for e, other := range etags {
			if tag == other {
				t.Fatalf("%s has the ETag of %q: %s", enc, e, tag)
			}
		}
		etags[enc] = tag
		// Its own ETag revalidates to a 304 with no body.
		res, body = fetch(t, "GET", u, map[string]string{"Accept-Encoding": enc, "If-None-Match": tag})
		if res.StatusCode != http.StatusNotModified || len(body) != 0 || res.Header.Get("ETag") != tag {
			t.Fatalf("%s revalidation: %d, %d bytes, ETag %q", enc, res.StatusCode, len(body), res.Header.Get("ETag"))
		}
		// HEAD: the headers of the compressed answer, no body.
		res, body = fetch(t, "HEAD", u, map[string]string{"Accept-Encoding": enc})
		if res.StatusCode != 200 || res.Header.Get("Content-Encoding") != enc || len(body) != 0 {
			t.Fatalf("%s HEAD: %d %q %d", enc, res.StatusCode, res.Header.Get("Content-Encoding"), len(body))
		}
	}
	// The identity ETag stays Node's, and does not match a compressed answer.
	if !strings.HasPrefix(etags[""], `W/"`) || strings.Contains(etags[""], "br") {
		t.Fatalf("identity ETag %q", etags[""])
	}
	res, _ := fetch(t, "GET", u, map[string]string{"Accept-Encoding": "br", "If-None-Match": etags[""]})
	if res.StatusCode != 200 || res.Header.Get("Content-Encoding") != "br" {
		t.Fatalf("br with the identity ETag: %d %q", res.StatusCode, res.Header.Get("Content-Encoding"))
	}
	// A range of the compressed body is not served; the whole body is.
	res, _ = fetch(t, "GET", u, map[string]string{"Accept-Encoding": "br", "Range": "bytes=0-9"})
	if res.StatusCode != 200 {
		t.Fatalf("range with br: %d", res.StatusCode)
	}
	// The JSON's type is the file's, not a sniff of compressed bytes.
	res, _ = fetch(t, "GET", srv.URL+"/assets/data-Big12345.json", map[string]string{"Accept-Encoding": "gzip"})
	if res.Header.Get("Content-Encoding") != "gzip" || res.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("json: %q %q", res.Header.Get("Content-Encoding"), res.Header.Get("Content-Type"))
	}
}

// Fonts already compressed, small files and the API are sent as they are.
func TestStaticNotCompressed(t *testing.T) {
	dist, _ := bigDist(t)
	node := fakeNode(t)
	u, _ := url.Parse(node.URL)
	srv := front(t, dist, u)
	for _, p := range []string{"/assets/font-Big12345.woff2", "/assets/index-AbCd1234.js", "/logo.svg"} {
		res, _ := fetch(t, "GET", srv.URL+p, map[string]string{"Accept-Encoding": "br, gzip"})
		if res.StatusCode != 200 || res.Header.Get("Content-Encoding") != "" || res.Header.Get("Vary") != "" {
			t.Fatalf("%s: %d, encoding %q, Vary %q", p, res.StatusCode, res.Header.Get("Content-Encoding"), res.Header.Get("Vary"))
		}
	}
	for _, p := range []string{"/api/config", "/api/stream"} {
		res, _ := fetch(t, "GET", srv.URL+p, map[string]string{"Accept-Encoding": "br, gzip"})
		if res.Header.Get("Content-Encoding") != "" {
			t.Fatalf("%s compressed: %q", p, res.Header.Get("Content-Encoding"))
		}
	}
}

// A rebuilt file is compressed again: the cache is keyed by size and time.
func TestStaticCompressionFollowsRebuild(t *testing.T) {
	dist, _ := bigDist(t)
	srv := front(t, dist, nil)
	u := srv.URL + "/assets/index-Big12345.css"
	res, _ := fetch(t, "GET", u, map[string]string{"Accept-Encoding": "gzip"})
	first := res.Header.Get("ETag")
	css := []byte(strings.Repeat(".b { color: blue }\n", 600))
	os.WriteFile(filepath.Join(dist, "assets", "index-Big12345.css"), css, 0o644)
	res, body := fetch(t, "GET", u, map[string]string{"Accept-Encoding": "gzip"})
	r, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	if res.Header.Get("ETag") == first || !bytes.Equal(got, css) {
		t.Fatalf("after a rebuild: ETag %q (was %q), body equal %v", res.Header.Get("ETag"), first, bytes.Equal(got, css))
	}
}
