package server

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// An open page's socket goes through the whole server: the sign-in gate in
// front of it and of each read made over it, and the route itself.
func TestThePagesSocketReadsAsTheSignedInPerson(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	b64 := base64.RawURLEncoding
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "p1", "alg": "RS256", "n": b64.EncodeToString(key.N.Bytes()), "e": b64.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	}))
	defer platform.Close()
	sign := func(user string, d time.Duration) string {
		hb, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "p1"})
		now := time.Now()
		pb, _ := json.Marshal(map[string]any{"sub": "user:" + user, "preferred_username": user, "iss": "pw-session-proxy", "iat": now.Unix(), "exp": now.Add(d).Unix()})
		in := b64.EncodeToString(hb) + "." + b64.EncodeToString(pb)
		h := sha256.Sum256([]byte(in))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
		return in + "." + b64.EncodeToString(sig)
	}
	// A route that gives the person a cookie.
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "k=v")
		w.Write([]byte(`{}`))
	}))
	defer node.Close()
	u, _ := url.Parse(node.URL)
	dir := t.TempDir()
	cfg := config.Config{WebDist: webDist(t), GufiBin: t.TempDir(), GufiIndex: filepath.Join(dir, "none"), IndexBase: filepath.Join(dir, "ix"), FrontAuth: true,
		Auth: config.Auth{JWKSURL: platform.URL, Header: "x-pw-user-token", Issuer: "pw-session-proxy", Required: true}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(New(cfg, log, u, auth.NewFront(cfg, log, nil, nil)))
	defer srv.Close()
	dial := func(tok string) (*websocket.Conn, error) {
		wc, _ := websocket.NewConfig(strings.Replace(srv.URL, "http", "ws", 1)+"/api/live", srv.URL)
		if tok != "" {
			wc.Header.Set("x-pw-user-token", tok)
		}
		return websocket.DialConfig(wc)
	}
	if _, err := dial(""); err == nil {
		t.Fatal("opened without signing in")
	}
	next := func(ws *websocket.Conn) (map[string]any, error) {
		ws.SetReadDeadline(time.Now().Add(10 * time.Second))
		var m map[string]any
		err := websocket.JSON.Receive(ws, &m)
		return m, err
	}
	ws, err := dial(sign("grace", time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	websocket.JSON.Send(ws, map[string]any{"want": map[string][2]int{"/api/whoami": {1000, 0}}})
	f, err := next(ws)
	if err != nil {
		t.Fatal(err)
	}
	who, _ := f["body"].(map[string]any)
	if user, _ := who["user"].(map[string]any); f["status"] != 200.0 || user["username"] != "grace" {
		t.Fatalf("%v", f)
	}
	// Another person's answer sets a cookie: nothing for this one's socket.
	visit := func(tok string) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/node-only", nil)
		req.Header.Set("x-pw-user-token", tok)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.Header.Get("Set-Cookie") == "" {
			t.Fatal("no cookie")
		}
	}
	visit(sign("bob", time.Hour))
	if f, err = next(ws); err != nil {
		t.Fatalf("ended by another person's cookie: %v", err)
	}
	// Their own: the socket would read on with the old ones, so it ends.
	visit(sign("grace", time.Hour))
	for err == nil {
		_, err = next(ws)
	}
	if strings.Contains(err.Error(), "timeout") {
		t.Fatal("open with the old cookies")
	}
	// A sign-in that runs out while a socket is open.
	if ws, err = dial(sign("grace", 2*time.Second)); err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	websocket.JSON.Send(ws, map[string]any{"want": map[string][2]int{"/api/whoami": {1000, 0}}})
	for deadline := time.Now().Add(120 * time.Second); err == nil; {
		if time.Now().After(deadline) {
			t.Fatal("open after the sign-in ran out")
		}
		if f, err = next(ws); err == nil && f["status"] != nil && f["status"] != 200.0 {
			t.Fatalf("%v", f)
		}
	}
}
