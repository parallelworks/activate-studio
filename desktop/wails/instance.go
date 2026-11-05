package main

// One shell per data folder. The first takes an exclusive lock on
// run/shell.lock and listens on a socket; a second finds the lock taken,
// asks the first over the socket to bring its window forward, and exits.
// The lock goes when the process does, however it ends, so a crash leaves
// nothing to clean up by hand. The socket is in the user's runtime folder
// (or the temporary one), since a socket path is limited to about a
// hundred characters and a data folder's can be longer.

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type instance struct {
	lock    *os.File
	sock    net.Listener
	sockDir string
	pidFile string
}

func socketPath(dataDir string) string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	sum := sha256.Sum256([]byte(dataDir))
	return filepath.Join(dir, fmt.Sprintf("activate-studio-%d-%x.sock", os.Getuid(), sum[:6]))
}

// acquireInstance takes the data folder's lock. other is true when another
// shell holds it; that shell was asked to show its window (via show, which
// the first shell runs).
func acquireInstance(dataDir string, show func()) (inst *instance, other bool, err error) {
	runDir := filepath.Join(dataDir, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(filepath.Join(runDir, "shell.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, err
	}
	path := socketPath(dataDir)
	if lockFile(f) {
		_ = os.Remove(path)
		l, err := net.Listen("unix", path)
		if err != nil {
			f.Close()
			return nil, false, fmt.Errorf("the shell could not listen on %s: %w", path, err)
		}
		_ = os.Chmod(path, 0o600)
		i := &instance{lock: f, sock: l, sockDir: path, pidFile: filepath.Join(runDir, "shell.pid")}
		_ = os.WriteFile(i.pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644)
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go func() {
					defer c.Close()
					line, _ := bufio.NewReader(c).ReadString('\n')
					if strings.TrimSpace(line) == "show" {
						show()
					}
				}()
			}
		}()
		return i, false, nil
	}
	f.Close()
	// The first one may still be starting and not listening yet.
	var last error
	for i := 0; i < 100; i++ {
		c, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			_, _ = c.Write([]byte("show\n"))
			c.Close()
			return nil, true, nil
		}
		last = err
		time.Sleep(100 * time.Millisecond)
	}
	return nil, false, errors.New("another ACTIVATE Studio window is using this data folder but did not answer: " + last.Error())
}

// Release drops the lock, the socket and the pid file.
func (i *instance) Release() {
	if i == nil || i.lock == nil {
		return
	}
	_ = i.sock.Close()
	_ = os.Remove(i.sockDir)
	_ = os.Remove(i.pidFile)
	_ = i.lock.Close()
	i.lock = nil
}
