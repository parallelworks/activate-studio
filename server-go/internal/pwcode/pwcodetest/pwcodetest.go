// Package pwcodetest is a fake pw code daemon for tests: an HTTP handler
// served on a Unix socket, or on a process's stdin and stdout the way
// `pw code daemon proxy` bridges an ssh session to the daemon.
package pwcodetest

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Socket serves h on a Unix socket in a short temporary directory (a
// socket path is limited to about 100 bytes) and returns its path.
func Socket(t testing.TB, h http.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pwc")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "code.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close(); os.RemoveAll(dir) })
	return sock
}

// Listen serves h on sock from now on (a daemon that starts later).
func Listen(t testing.TB, sock string, h http.Handler) {
	t.Helper()
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
}

// ServeStdio serves h on this process's stdin and stdout until stdin
// closes: one connection, as the proxy gives.
func ServeStdio(h http.Handler) {
	c := &stdioConn{done: make(chan struct{})}
	l := &oneListener{c: c}
	srv := &http.Server{Handler: h}
	go srv.Serve(l)
	<-c.done
	time.Sleep(50 * time.Millisecond)
}

type stdioConn struct {
	once sync.Once
	done chan struct{}
}

func (c *stdioConn) Read(p []byte) (int, error) {
	n, err := os.Stdin.Read(p)
	if err == io.EOF {
		c.Close()
	}
	return n, err
}
func (c *stdioConn) Write(p []byte) (int, error)      { return os.Stdout.Write(p) }
func (c *stdioConn) Close() error                     { c.once.Do(func() { close(c.done) }); return nil }
func (c *stdioConn) LocalAddr() net.Addr              { return addr{} }
func (c *stdioConn) RemoteAddr() net.Addr             { return addr{} }
func (c *stdioConn) SetDeadline(time.Time) error      { return nil }
func (c *stdioConn) SetReadDeadline(time.Time) error  { return nil }
func (c *stdioConn) SetWriteDeadline(time.Time) error { return nil }

type addr struct{}

func (addr) Network() string { return "stdio" }
func (addr) String() string  { return "stdio" }

type oneListener struct {
	mu   sync.Mutex
	c    net.Conn
	used bool
}

func (l *oneListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.used {
		l.used = true
		l.mu.Unlock()
		return l.c, nil
	}
	l.mu.Unlock()
	select {} // one connection only
}
func (l *oneListener) Close() error   { return nil }
func (l *oneListener) Addr() net.Addr { return addr{} }
