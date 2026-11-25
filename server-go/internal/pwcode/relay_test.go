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
		"GET /v1/sessions/s1":                   "/api/code/machines/m%202/sessions/s1",
		"GET /v1/sessions/s1?history=40":        "/api/code/machines/m%202/sessions/s1?history=40",
		"POST /v1/sessions/s1/messages":         "/api/code/machines/m%202/sessions/s1/messages",
		"POST /v1/sessions/s1/approvals/ap1":    "/api/code/machines/m%202/sessions/s1/approvals/ap1",
		"POST /v1/sessions/s1/interrupt":        "/api/code/machines/m%202/sessions/s1/interrupt",
		"POST /v1/sessions/s1/stop":             "/api/code/machines/m%202/sessions/s1/stop",
		"POST /v1/sessions/s1/direction":        "/api/code/machines/m%202/sessions/s1/direction",
		"PATCH /v1/sessions/s1/settings":        "/api/code/machines/m%202/sessions/s1/settings",
		"GET /v1/sessions/s1/transcript?tail=1": "/api/code/machines/m%202/sessions/s1/transcript?tail=1",
		"GET /v1/sessions/s1/models":            "/api/code/machines/m%202/sessions/s1/models",
		"DELETE /v1/sessions/s1/queued/t1":      "/api/code/machines/m%202/sessions/s1/queued/t1",
		// The session view's commands, images, subagents and the rest.
		"POST /v1/sessions/s1/shell":                 "/api/code/machines/m%202/sessions/s1/shell",
		"POST /v1/sessions/s1/commands":              "/api/code/machines/m%202/sessions/s1/commands",
		"POST /v1/sessions/s1/clear":                 "/api/code/machines/m%202/sessions/s1/clear",
		"POST /v1/sessions/s1/goal":                  "/api/code/machines/m%202/sessions/s1/goal",
		"GET /v1/sessions/s1/images/img-1.png":       "/api/code/machines/m%202/sessions/s1/images/img-1.png",
		"GET /v1/sessions/s1/images/r1?preview=true": "/api/code/machines/m%202/sessions/s1/images/r1?preview=true",
		"POST /v1/sessions/s1/subagents/a1/messages": "/api/code/machines/m%202/sessions/s1/subagents/a1/messages",
		"POST /v1/sessions/s1/subagents/stop-all":    "/api/code/machines/m%202/sessions/s1/subagents/stop-all",
		"GET /v1/sessions/s1/claude-code/accounts":   "/api/code/machines/m%202/sessions/s1/claude-code/accounts",
		// The folders a new session could work in.
		"GET /v1/directories?path=%2Fhome&filter=": "/api/code/machines/m%202/directories?path=%2Fhome&filter=",
		"POST /v1/directories?path=%2Fhome":        "",
		"GET /v1/directories":                      "",
		// The platform relays these; the Studio does not send them.
		"DELETE /v1/sessions/s1":              "",
		"PATCH /v1/sessions/s1":               "",
		"POST /v1/sessions/s1":                "",
		"GET /v1/sessions/s1/messages":        "",
		"DELETE /v1/sessions/s1/queued/":      "",
		"DELETE /v1/sessions/s1/queued/a/b":   "",
		"POST /v1/sessions/s1/pin":            "",
		"PUT /v1/sessions/s1/group":           "",
		"POST /v1/sessions/s1/worktree/leave": "",
		"POST /v1/sessions/s1/subagents/x/y":  "",
		"GET /v1/sessions/s1/images/a/b":      "",
		"GET /v1/sessions/s1/images/..":       "",
		"DELETE /v1/sessions/s1/mcp/x":        "",
		"GET /v1/sessions/s1/state":           "",
		"POST /v1/sessions/s1/fork":           "",
		"PUT /v1/sessions/s1/settings":        "",
		"POST /v1/sessions/s1/archive":        "",
		"POST /v1/sessions/s1/rename":         "/api/code/machines/m%202/sessions/s1/rename",
		"GET /v1/sessions/s1/rename":          "",
		"POST /v1/sessions/s1/approvals/":     "",
		"POST /v1/sessions/s1/approvals/a/b":  "",
		"POST /v1/sessions/s1/attach":         "",
		"GET /v1/sessions/s1/mcp":             "",
		"GET /v1/sessions/s1/goal":            "",
		"GET /v1/sessions":                    "",
		"GET /v1/status":                      "",
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

func TestASilentRelayEndsTheTurnAndHeartbeatsKeepItOpen(t *testing.T) {
	was := relayIdle
	relayIdle = 300 * time.Millisecond
	defer func() { relayIdle = was }()
	quit := make(chan struct{})
	defer close(quit)
	srv := httptest.NewServer(websocket.Server{Handler: func(ws *websocket.Conn) {
		if ws.Request().URL.Path == "/ws/code/machines/m2/sessions/beats/events" {
			// Heartbeats only, for longer than the idle limit, then done.
			for range 6 {
				websocket.Message.Send(ws, `{"type":"heartbeat","seq":0}`)
				time.Sleep(100 * time.Millisecond)
			}
			websocket.Message.Send(ws, `{"seq":7,"type":"done"}`)
			return
		}
		websocket.Message.Send(ws, `{"seq":5,"type":"chunk","content":"hi"}`)
		<-quit
	}})
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got []string
	seen := func(ev *jsonv.Object) bool {
		got = append(got, jsonv.String(jsonv.Path(ev, "type")))
		return jsonv.String(jsonv.Path(ev, "type")) != "done"
	}
	started := time.Now()
	err := Follow(ctx, Relay(srv.URL, "k", "m2"), "silent", 0, seen)
	if err == nil || !strings.Contains(err.Error(), "sent nothing on the session's events") || time.Since(started) > 2*time.Second || strings.Join(got, ",") != "chunk" {
		t.Errorf("a silent link: %v after %s, saw %v", err, time.Since(started), got)
	}
	got = nil
	if err := Follow(ctx, Relay(srv.URL, "k", "m2"), "beats", 0, seen); err != nil || strings.Join(got, ",") != "done" {
		t.Errorf("heartbeats: %v, saw %v", err, got)
	}
}
