package pwcode

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

func TestTheRelayMapsOnlyTheRoutesThePlatformRelays(t *testing.T) {
	c := Relay("https://platform.example/", "k", "m 2")
	for route, want := range map[string]string{
		"GET /v1/sessions/s1":                "/api/code/machines/m%202/sessions/s1",
		"GET /v1/sessions/s1?history=40":     "/api/code/machines/m%202/sessions/s1?history=40",
		"POST /v1/sessions/s1/messages":      "/api/code/machines/m%202/sessions/s1/messages",
		"POST /v1/sessions/s1/approvals/ap1": "/api/code/machines/m%202/sessions/s1/approvals/ap1",
		"POST /v1/sessions/s1/interrupt":     "/api/code/machines/m%202/sessions/s1/interrupt",
		// The platform relays these; the Studio does not send them.
		"DELETE /v1/sessions/s1":             "",
		"PATCH /v1/sessions/s1":              "",
		"POST /v1/sessions/s1":               "",
		"GET /v1/sessions/s1/messages":       "",
		"POST /v1/sessions/s1/stop":          "",
		"POST /v1/sessions/s1/direction":     "",
		"POST /v1/sessions/s1/archive":       "",
		"PATCH /v1/sessions/s1/settings":     "",
		"GET /v1/sessions/s1/transcript":     "",
		"POST /v1/sessions/s1/approvals/":    "",
		"POST /v1/sessions/s1/approvals/a/b": "",
		"POST /v1/sessions/s1/attach":        "",
		"GET /v1/sessions/s1/mcp":            "",
		"GET /v1/sessions/s1/goal":           "",
		"GET /v1/sessions":                   "",
		"GET /v1/status":                     "",
	} {
		method, route, _ := strings.Cut(route, " ")
		if got := c.relayPath(method, route); got != want {
			t.Errorf("%s: %q, want %q", route, got, want)
		}
	}
}

func TestTheRelayRefusesWhatItDoesNotOfferWithoutAsking(t *testing.T) {
	var asked int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&asked, 1)
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"s1","lastSeq":4}`))
	}))
	defer srv.Close()
	c := Relay(srv.URL, "k", "m2")
	if _, err := AttachSession(context.Background(), c, "s1"); err == nil || atomic.LoadInt32(&asked) != 0 {
		t.Errorf("attach: %v, asked %d", err, asked)
	}
	var de *DaemonError
	if _, err := SessionMCP(context.Background(), c, "s1"); !errors.As(err, &de) || de.Status != http.StatusNotImplemented {
		t.Errorf("mcp: %v", err)
	}
	if _, err := c.Call(context.Background(), http.MethodDelete, "/v1/sessions/s1", nil, 0); !errors.As(err, &de) || de.Status != http.StatusNotImplemented {
		t.Errorf("delete: %v", err)
	}
	if _, err := ArchiveSession(context.Background(), c, "s1"); err == nil {
		t.Error("archive was sent")
	}
	if d, err := GetSession(context.Background(), c, "s1"); err != nil || d == nil || atomic.LoadInt32(&asked) != 1 {
		t.Errorf("read: %v %v", d, err)
	}
	bad := Relay(srv.URL, "other", "m2")
	if _, err := GetSession(context.Background(), bad, "s1"); !errors.As(err, &de) || de.Status != 401 || de.Message == "" {
		t.Errorf("refused: %v", err)
	}
}

func TestTheRelaysEventsOpenOnAPlatformThatOffersHTTP2(t *testing.T) {
	var seen struct{ auth, origin, query string }
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws/code/machines/m2/sessions/s1/events" {
			w.WriteHeader(404)
			return
		}
		seen.auth, seen.origin, seen.query = r.Header.Get("Authorization"), r.Header.Get("Origin"), r.URL.RawQuery
		websocket.Server{Handler: func(ws *websocket.Conn) {
			websocket.Message.Send(ws, `{"seq":5,"type":"chunk","content":"hi"}`)
			websocket.Message.Send(ws, `{"seq":6,"type":"done"}`)
		}}.ServeHTTP(w, r)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	// The platform's transport, trusting the stand-in, offers HTTP/2 as
	// Go's does.
	tr := platform.HTTPClient.Transport.(*http.Transport)
	was := tr.TLSClientConfig
	tr.TLSClientConfig = &tls.Config{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs, NextProtos: []string{"h2", "http/1.1"}}
	defer func() { tr.TLSClientConfig = was }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var got []string
	err := Follow(ctx, Relay(srv.URL, "k", "m2"), "s1", 4, func(ev *jsonv.Object) bool {
		got = append(got, jsonv.String(jsonv.Path(ev, "type")))
		return jsonv.String(jsonv.Path(ev, "type")) != "done"
	})
	if err != nil || strings.Join(got, ",") != "chunk,done" {
		t.Fatalf("follow: %v %v", got, err)
	}
	if seen.auth != "Bearer k" || seen.origin != strings.ToLower(srv.URL) || seen.query != "after=4&heartbeat=10" {
		t.Errorf("handshake: %+v", seen)
	}
}
