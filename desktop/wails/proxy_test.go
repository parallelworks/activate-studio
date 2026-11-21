package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The server sees the host the page asked for, so it can check a
// WebSocket's Origin, and an upgrade passes through the proxy.
func TestProxyForwardsHostAndUpgrades(t *testing.T) {
	var fwd, upgrade string
	studio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fwd, upgrade = r.Header.Get("X-Forwarded-Host"), r.Header.Get("Upgrade")
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		buf.Flush()
		b := make([]byte, 16)
		n, _ := buf.Read(b)
		conn.Write([]byte("echo:" + string(b[:n])))
	}))
	defer studio.Close()
	port, err := strconv.Atoi(studio.URL[strings.LastIndex(studio.URL, ":")+1:])
	if err != nil {
		t.Fatal(err)
	}
	p := NewProxy(&App{})
	p.SetServer(port, true)
	shell := httptest.NewServer(p.LocalHandler())
	defer shell.Close()
	host := strings.TrimPrefix(shell.URL, "http://")
	p.localHost = host
	conn, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "GET /api/session-desktop/a/d/websockify HTTP/1.1\r\nHost: %s\r\nOrigin: http://%s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", host, host)
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil || res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatal(res, err)
	}
	conn.Write([]byte("hi"))
	b := make([]byte, 16)
	n, _ := br.Read(b)
	if string(b[:n]) != "echo:hi" || fwd != host || upgrade != "websocket" {
		t.Fatalf("got %q, X-Forwarded-Host %q, Upgrade %q", b[:n], fwd, upgrade)
	}
}
