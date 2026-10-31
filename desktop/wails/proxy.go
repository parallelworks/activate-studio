package main

// The HTTP side of the shell. One handler serves:
//
//   /__shell/                the start page (spinner, or the error with Try Again)
//   /__shell/stream-test     the self test's streaming endpoint, proxied the same
//                            way as the Studio, so it crosses the same layers
//   anything else            in assetserver and localproxy modes, the Studio's
//                            server, through a reverse proxy that puts the shim
//                            (shell/shim.js) first in every HTML page's <head>;
//                            in direct mode, the start page
//
// In assetserver mode this handler is Wails' AssetServer.Handler: the
// window's origin is Wails' own (wails://wails on Linux, http://wails.localhost
// on Windows), so the Wails runtime and bindings are in the page. In
// localproxy mode the same handler also listens on a 127.0.0.1 port of its
// own and the window opens that, an ordinary http origin.

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

//go:embed shell/start.html
var startPage []byte

type Proxy struct {
	app *App

	mu        sync.RWMutex
	port      int  // the Studio's server
	ready     bool // it answered /api/version
	testPort  int  // the self test's stream server, 0 outside test mode
	localHost string

	studio *httputil.ReverseProxy
	test   *httputil.ReverseProxy
}

func NewProxy(app *App) *Proxy {
	p := &Proxy{app: app}
	p.studio = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(&url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", p.Port())})
			// Loopback: compression buys nothing, and the HTML is edited.
			r.Out.Header.Del("Accept-Encoding")
			// No 304s: Wails v2 on Windows turns a 304 from the asset
			// handler into a 500 (WebView2 hangs on 304s from a custom
			// handler), so conditional requests are not passed on.
			r.Out.Header.Del("If-None-Match")
			r.Out.Header.Del("If-Modified-Since")
		},
		// Write each chunk as it arrives: chat answers and other streams
		// must not wait for a buffer to fill.
		FlushInterval:  -1,
		ModifyResponse: p.injectShim,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "The Studio's server is not answering: "+err.Error(), http.StatusBadGateway)
		},
	}
	p.test = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			p.mu.RLock()
			port := p.testPort
			p.mu.RUnlock()
			r.SetURL(&url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port)})
			r.Out.URL.Path = strings.TrimPrefix(r.Out.URL.Path, "/__shell/stream-test")
			if r.Out.URL.Path == "" {
				r.Out.URL.Path = "/stream"
			}
		},
		FlushInterval: -1,
	}
	return p
}

func (p *Proxy) Port() int { p.mu.RLock(); defer p.mu.RUnlock(); return p.port }

// SetServer points the proxy at the server, ready or not.
func (p *Proxy) SetServer(port int, ready bool) {
	p.mu.Lock()
	p.port, p.ready = port, ready
	p.mu.Unlock()
}

// AssetHandler is Wails' AssetServer.Handler. In assetserver mode it
// proxies the Studio; in the other modes the asset origin only has the
// start page.
func (p *Proxy) AssetHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.serve(w, r, p.app.cfg.Mode == ModeAssetServer)
	})
}

// LocalHandler serves the localproxy listener on 127.0.0.1. Only requests
// addressed to it by that name are answered, so a page elsewhere that
// points a host name of its own at 127.0.0.1 cannot reach it.
func (p *Proxy) LocalHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.RLock()
		host := p.localHost
		p.mu.RUnlock()
		if r.Host != host {
			http.Error(w, "wrong host", http.StatusForbidden)
			return
		}
		p.serve(w, r, true)
	})
}

func (p *Proxy) serve(w http.ResponseWriter, r *http.Request, proxyStudio bool) {
	p.mu.RLock()
	ready, testPort := p.ready, p.testPort
	p.mu.RUnlock()
	path := r.URL.Path
	switch {
	case path == "/__shell/" || path == "/__shell/index.html":
		p.serveStart(w)
	case strings.HasPrefix(path, "/__shell/stream-test"):
		if testPort == 0 {
			http.NotFound(w, r)
			return
		}
		p.test.ServeHTTP(w, r)
	case strings.HasPrefix(path, "/__shell/"):
		http.NotFound(w, r)
	case !proxyStudio:
		p.serveStart(w)
	case !ready:
		if r.Method == http.MethodGet && (path == "/" || strings.Contains(r.Header.Get("Accept"), "text/html")) {
			p.serveStart(w)
			return
		}
		http.Error(w, "The Studio's server is starting", http.StatusServiceUnavailable)
	default:
		p.studio.ServeHTTP(w, r)
	}
}

func (p *Proxy) serveStart(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(startPage)
}

var headTag = regexp.MustCompile(`(?i)<head(\s[^>]*)?>`)

// injectShim puts the shim first in the <head> of every HTML page the
// Studio serves, so window.webkit.messageHandlers.studioShell exists before
// any of the page's own scripts run.
func (p *Proxy) injectShim(res *http.Response) error {
	if res.Request.Method != http.MethodGet || res.StatusCode != http.StatusOK ||
		!strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") || res.Header.Get("Content-Encoding") != "" {
		return nil
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return err
	}
	script := []byte("<script>" + p.app.shimScript() + "</script>")
	if loc := headTag.FindIndex(body); loc != nil {
		body = append(body[:loc[1]:loc[1]], append(script, body[loc[1]:]...)...)
	} else {
		body = append(script, body...)
	}
	res.Body = io.NopCloser(bytes.NewReader(body))
	res.ContentLength = int64(len(body))
	res.Header.Set("Content-Length", strconv.Itoa(len(body)))
	res.Header.Del("ETag")
	res.Header.Set("Cache-Control", "no-store")
	return nil
}
