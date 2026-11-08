package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// IndexLock is instanceLock.ts: one Studio at a time does index work in an
// index folder. The Studio that holds the folder (INDEX_BASE/.studio-instance,
// naming its process) runs the background sync and the index jobs; a second
// one still searches and queries, and refuses index jobs, saying which
// process holds the folder. The file's format is Node's, and a holder is a
// Studio when it is a node running main.js or a studio-server, so a Node
// Studio and a Go Studio on the same folder see each other.
type IndexLock struct {
	file string
	port int

	mu    sync.Mutex
	held  bool
	other *Holder
}

// Holder is the lock file's contents.
type Holder struct {
	PID       int    `json:"pid"`
	Port      *int   `json:"port"`
	StartedAt string `json:"startedAt"`
}

// NewIndexLock is the lock of indexBase for a server on port.
func NewIndexLock(indexBase string, port int) *IndexLock {
	return &IndexLock{file: filepath.Join(indexBase, ".studio-instance"), port: port}
}

func (l *IndexLock) read() *Holder {
	b, err := os.ReadFile(l.file)
	if err != nil {
		return nil
	}
	var h Holder
	if json.Unmarshal(b, &h) != nil || h.PID <= 0 {
		return nil
	}
	return &h
}

var studioCommand = regexp.MustCompile(`\bstudio-server\b`)
var nodeWord = regexp.MustCompile(`\bnode\b`)
var mainJS = regexp.MustCompile(`main\.(js|ts)\b`)

// IsStudio reports whether pid is a Studio still running: it exists, and
// is a node running main.js or a studio-server, which a process that was
// given the number of one that died is not.
func IsStudio(pid int) bool {
	if !alive(pid) {
		return false
	}
	cmd := commandOf(pid)
	return (nodeWord.MatchString(cmd) && mainJS.MatchString(cmd)) || studioCommand.MatchString(cmd)
}

func commandOf(pid int) string {
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil && len(b) > 0 {
		return strings.ReplaceAll(strings.TrimRight(string(b), "\x00"), "\x00", " ")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// Acquire takes the index folder for this process. A Studio that is still
// stopping (a restart) is waited for, up to wait; one that keeps it is
// named in log, and this one goes on without index work (false).
func (l *IndexLock) Acquire(ctx context.Context, log func(string), wait time.Duration) bool {
	me := os.Getpid()
	var port *int
	if l.port > 0 {
		p := l.port
		port = &p
	}
	mine, _ := json.Marshal(Holder{PID: me, Port: port, StartedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	deadline := time.Now().Add(wait)
	os.MkdirAll(filepath.Dir(l.file), 0o755)
	for {
		h := l.read()
		if h == nil || h.PID == me || !IsStudio(h.PID) {
			// A lock that is not one (damaged, or of a process that is gone)
			// is cleared; a free folder has nothing to clear.
			if _, err := os.Stat(l.file); h != nil || err == nil {
				os.Remove(l.file)
			}
			f, err := os.OpenFile(l.file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if err == nil {
				_, err = f.Write(mine)
				f.Close()
				l.set(true, nil)
				return true
			}
			// Another Studio wrote it between the look and the write: look again.
			if !errors.Is(err, os.ErrExist) {
				log("index folder lock not written: " + err.Error())
				l.set(true, nil)
				return true
			}
		}
		if !time.Now().Before(deadline) {
			other := l.read()
			if other != nil && other.PID != me {
				l.set(false, other)
				log(fmt.Sprintf("another Studio (process %d%s) is using the index folder %s; this one searches and queries but does no index work", other.PID, portPart(other), filepath.Dir(l.file)))
				return false
			}
			l.set(true, nil)
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func portPart(h *Holder) string {
	if h.Port != nil && *h.Port != 0 {
		return fmt.Sprintf(", port %d", *h.Port)
	}
	return ""
}

func (l *IndexLock) set(held bool, other *Holder) {
	l.mu.Lock()
	l.held, l.other = held, other
	l.mu.Unlock()
}

// Held reports whether this process holds the folder.
func (l *IndexLock) Held() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.held
}

// IndexWorkAllowed is indexWorkAllowed: true unless another Studio holds
// the folder (a process that never asked is allowed).
func (l *IndexLock) IndexWorkAllowed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.other == nil
}

// RequireIndexWork is requireIndexWork: a 409 naming the other Studio.
func (l *IndexLock) RequireIndexWork() error {
	l.mu.Lock()
	other := l.other
	l.mu.Unlock()
	if other == nil {
		return nil
	}
	return apierr.New(409, "Another Studio (process %d%s) is using this index folder, and indexes it. Stop it, or use this one only to search and query.", other.PID, portPart(other))
}

// Release removes the lock file when it is still this process's, as Node
// does on exit.
func (l *IndexLock) Release() {
	if h := l.read(); h != nil && h.PID == os.Getpid() {
		os.Remove(l.file)
	}
	l.set(false, l.other)
}
