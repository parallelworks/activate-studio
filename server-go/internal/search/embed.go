package search

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Embedder embeds search queries: 384 little-endian float32 values per
// text (embedWorker.ts embedQueries).
//
// TODO(index track, internal/embed): wire embed.Embedder here once
// port-index is merged; WorkerEmbedder is the stand-in, which runs
// indexer/embed_query.py as Node does.
type Embedder interface {
	Present() bool
	Embed(ctx context.Context, texts []string) ([][]byte, error)
}

// WorkerEmbedder is embedWorker.ts: a long-lived embed_query.py over
// stdio, started on first use, stopped after ten idle minutes, not tried
// again for five minutes after it failed to start.
type WorkerEmbedder struct {
	Python      string // PYTHON_BIN
	ProjectRoot string
	IndexBase   string
	Env         []string

	mu      sync.Mutex
	w       *worker
	nextID  int
	idle    *time.Timer
	down    string
	downTil time.Time
}

type worker struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	ready   chan error
	mu      sync.Mutex
	pending map[int]chan workerAnswer
}

type workerAnswer struct {
	vecs [][]byte
	err  error
}

func (e *WorkerEmbedder) dir() string { return filepath.Join(e.IndexBase, "models", "minilm-l6") }

// Present is onnxModelPresent.
func (e *WorkerEmbedder) Present() bool {
	for _, f := range []string{"model.onnx", "tokenizer.json"} {
		if _, err := os.Stat(filepath.Join(e.dir(), f)); err != nil {
			return false
		}
	}
	return true
}

func (e *WorkerEmbedder) start() *worker {
	py := e.Python
	if py == "" {
		py = "python3"
	}
	cmd := exec.Command(py, filepath.Join(e.ProjectRoot, "indexer", "embed_query.py"), "--onnx", e.dir())
	cmd.Env = append(append(os.Environ(), e.Env...), "TOKENIZERS_PARALLELISM=false", "OMP_NUM_THREADS=1", "OPENBLAS_NUM_THREADS=1")
	w := &worker{cmd: cmd, ready: make(chan error, 1), pending: map[int]chan workerAnswer{}}
	w.stdin, _ = cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	var stderr strings.Builder
	cmd.Stderr = &limited{b: &stderr}
	if err := cmd.Start(); err != nil {
		w.ready <- fmt.Errorf("the query embedder could not run: %s", err.Error())
		return w
	}
	timer := time.AfterFunc(60*time.Second, func() { w.ready <- errors.New("the query embedder did not start in time") })
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			var m struct {
				Ready   *bool    `json:"ready"`
				Error   string   `json:"error"`
				ID      *int     `json:"id"`
				Vectors []string `json:"vectors"`
			}
			if json.Unmarshal(sc.Bytes(), &m) != nil {
				continue
			}
			if m.Ready != nil {
				timer.Stop()
				if *m.Ready {
					w.ready <- nil
				} else {
					msg := m.Error
					if msg == "" {
						msg = "the query embedder could not start"
					}
					w.ready <- errors.New(msg)
				}
				continue
			}
			if m.ID == nil {
				continue
			}
			w.mu.Lock()
			ch := w.pending[*m.ID]
			delete(w.pending, *m.ID)
			w.mu.Unlock()
			if ch == nil {
				continue
			}
			if m.Error != "" || m.Vectors == nil {
				msg := m.Error
				if msg == "" {
					msg = "no vectors"
				}
				ch <- workerAnswer{err: errors.New(msg)}
				continue
			}
			var vs [][]byte
			for _, v := range m.Vectors {
				b, _ := base64.StdEncoding.DecodeString(v)
				vs = append(vs, b)
			}
			ch <- workerAnswer{vecs: vs}
		}
		cmd.Wait()
		why := fmt.Sprintf("the query embedder stopped (exit %d)", cmd.ProcessState.ExitCode())
		if s := strings.TrimSpace(stderr.String()); s != "" {
			why += ": " + lastLine(s)
		}
		timer.Stop()
		select {
		case w.ready <- errors.New(why):
		default:
		}
		w.mu.Lock()
		for id, ch := range w.pending {
			ch <- workerAnswer{err: errors.New(why)}
			delete(w.pending, id)
		}
		w.mu.Unlock()
		e.mu.Lock()
		if e.w == w {
			e.w = nil
		}
		e.mu.Unlock()
	}()
	return w
}

type limited struct{ b *strings.Builder }

func (l *limited) Write(p []byte) (int, error) {
	l.b.Write(p)
	if l.b.Len() > 2000 {
		s := l.b.String()
		l.b.Reset()
		l.b.WriteString(s[len(s)-2000:])
	}
	return len(p), nil
}

// Stop ends the worker.
func (e *WorkerEmbedder) Stop() {
	e.mu.Lock()
	w := e.w
	e.w = nil
	e.mu.Unlock()
	if w != nil && w.cmd.Process != nil {
		w.stdin.Close()
		w.cmd.Process.Kill()
	}
}

// Embed is embedQueries.
func (e *WorkerEmbedder) Embed(ctx context.Context, texts []string) ([][]byte, error) {
	e.mu.Lock()
	if e.down != "" && time.Now().Before(e.downTil) {
		d := e.down
		e.mu.Unlock()
		return nil, errors.New(d)
	}
	if e.w == nil {
		e.w = e.start()
	}
	w := e.w
	e.mu.Unlock()
	err := <-w.ready
	// Keep the answer for the next caller.
	select {
	case w.ready <- err:
	default:
	}
	if err != nil {
		e.mu.Lock()
		e.down, e.downTil = err.Error(), time.Now().Add(5*time.Minute)
		e.mu.Unlock()
		e.Stop()
		return nil, err
	}
	e.mu.Lock()
	if e.idle != nil {
		e.idle.Stop()
	}
	e.idle = time.AfterFunc(10*time.Minute, e.Stop)
	e.nextID++
	id := e.nextID
	e.mu.Unlock()
	ch := make(chan workerAnswer, 1)
	w.mu.Lock()
	w.pending[id] = ch
	w.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"id": id, "texts": texts})
	w.stdin.Write(append(b, '\n'))
	select {
	case a := <-ch:
		return a.vecs, a.err
	case <-time.After(20 * time.Second):
		w.mu.Lock()
		delete(w.pending, id)
		w.mu.Unlock()
		return nil, errors.New("the query embedder did not answer in time")
	case <-ctx.Done():
		w.mu.Lock()
		delete(w.pending, id)
		w.mu.Unlock()
		return nil, ctx.Err()
	}
}
