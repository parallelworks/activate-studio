package index

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Log is the index log (indexLog.ts): every job and sweep writes what it
// does, one timestamped line at a time, in LOG_DIR/indexing.log; at 10 MB
// the file moves to indexing.log.1. Writing never fails the indexing it
// describes.
type Log struct {
	File string
	mu   sync.Mutex
	size int64
}

const logMaxBytes = 10 * 1024 * 1024

// NewLog is the log in dir.
func NewLog(dir string) *Log { return &Log{File: filepath.Join(dir, "indexing.log"), size: -1} }

// Printf writes one line, or several, each with the time (UTC, as Node's
// toISOString) in front.
func (l *Log) Write(msg string) {
	at := time.Now().UTC().Format("2006-01-02 15:04:05")
	var b strings.Builder
	for _, line := range strings.Split(msg, "\n") {
		if strings.TrimSpace(line) != "" {
			b.WriteString(at + " " + line + "\n")
		}
	}
	text := b.String()
	if text == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.File), 0o755); err != nil {
		return
	}
	if l.size < 0 {
		if st, err := os.Stat(l.File); err == nil {
			l.size = st.Size()
		} else {
			l.size = 0
		}
	}
	if l.size >= logMaxBytes {
		os.Rename(l.File, l.File+".1")
		l.size = 0
	}
	f, err := os.OpenFile(l.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	n, _ := f.WriteString(text)
	f.Close()
	l.size += int64(n)
}

// Tail is the last n lines, read from the end.
func (l *Log) Tail(n int) string {
	f, err := os.Open(l.File)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	total := st.Size()
	want := min(total, max(64*1024, int64(n)*400))
	buf := make([]byte, want)
	if _, err := f.ReadAt(buf, total-want); err != nil && err != io.EOF {
		return ""
	}
	all := strings.Split(string(buf), "\n")
	if want < total {
		all = all[1:]
	}
	if len(all) > 0 && all[len(all)-1] == "" {
		all = all[:len(all)-1]
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	if len(all) == 0 {
		return ""
	}
	return strings.Join(all, "\n") + "\n"
}

// Info is indexLogInfo: {path, bytes}.
func (l *Log) Info() map[string]any {
	var bytes int64
	if st, err := os.Stat(l.File); err == nil {
		bytes = st.Size()
	}
	return map[string]any{"path": l.File, "bytes": bytes}
}
