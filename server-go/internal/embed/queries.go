package embed

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Queries is embedWorker.ts: query vectors for search by meaning over
// indexes embedded with the ONNX model. One child, `studio-server
// embed-query --onnx <ModelDir>`, is started at the first such search and
// kept, so the model loads once; it answers JSON lines on its stdin and
// stdout. It is stopped after ten idle minutes and ends with the server
// (Stop, or its input closing). A child that cannot start is not tried
// again for five minutes; Embed answers meanwhile with why.
//
// The model runs in a child, never in the server, so a failure in the
// native runtime cannot take the server down.
type Queries struct {
	// Dir is the model's folder (ModelDir(INDEX_BASE)).
	Dir string
	// Command, when set, runs in place of `<this binary> embed-query`
	// (tests); STUDIO_EMBED_QUERY_BIN does the same from the environment.
	Command []string
	// Env is the child's environment; os.Environ() when nil.
	Env []string

	mu      sync.Mutex
	w       *queryWorker
	nextID  int
	idle    *time.Timer
	down    string
	downTil time.Time
}

const (
	queryStart   = 60 * time.Second
	queryRequest = 20 * time.Second
	queryIdle    = 10 * time.Minute
	queryRetry   = 5 * time.Minute
)

type queryWorker struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	ready   chan struct{}
	readyOK error
	mu      sync.Mutex
	pending map[int]chan queryAnswer
	gone    bool
}

type queryAnswer struct {
	vecs [][]byte
	err  error
}

// Down is why the child is not running, when it failed to start lately.
func (q *Queries) Down() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.down != "" && time.Now().Before(q.downTil) {
		return q.down
	}
	return ""
}

// Stop ends the child; waiting calls fail.
func (q *Queries) Stop() {
	q.mu.Lock()
	w := q.w
	q.w = nil
	if q.idle != nil {
		q.idle.Stop()
		q.idle = nil
	}
	q.mu.Unlock()
	if w != nil {
		w.stop(errors.New("the query embedder stopped"))
	}
}

func (w *queryWorker) stop(why error) {
	w.mu.Lock()
	if w.gone {
		w.mu.Unlock()
		return
	}
	w.gone = true
	for id, ch := range w.pending {
		ch <- queryAnswer{err: why}
		delete(w.pending, id)
	}
	w.mu.Unlock()
	w.stdin.Close()
	if w.cmd.Process != nil {
		w.cmd.Process.Kill()
	}
}

func (q *Queries) start() (*queryWorker, error) {
	argv := q.Command
	if len(argv) == 0 {
		if v := os.Getenv("STUDIO_EMBED_QUERY_BIN"); v != "" {
			argv = []string{v, "--onnx", q.Dir}
		} else {
			exe, err := os.Executable()
			if err != nil {
				return nil, err
			}
			argv = []string{exe, "embed-query", "--onnx", q.Dir}
		}
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	env := q.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = append(env, "OMP_NUM_THREADS=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var errTail tail
	cmd.Stderr = &errTail
	w := &queryWorker{cmd: cmd, stdin: stdin, ready: make(chan struct{}), pending: map[int]chan queryAnswer{}}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("the query embedder could not run: %w", err)
	}
	var once sync.Once
	settle := func(err error) { once.Do(func() { w.readyOK = err; close(w.ready) }) }
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			var msg struct {
				Ready   *bool    `json:"ready"`
				Error   string   `json:"error"`
				ID      *int     `json:"id"`
				Vectors []string `json:"vectors"`
			}
			if json.Unmarshal(sc.Bytes(), &msg) != nil {
				continue
			}
			if msg.Ready != nil {
				if *msg.Ready {
					settle(nil)
				} else {
					e := msg.Error
					if e == "" {
						e = "the query embedder could not start"
					}
					settle(errors.New(e))
				}
				continue
			}
			if msg.ID == nil {
				continue
			}
			w.mu.Lock()
			ch := w.pending[*msg.ID]
			delete(w.pending, *msg.ID)
			w.mu.Unlock()
			if ch == nil {
				continue
			}
			if msg.Error != "" || msg.Vectors == nil {
				e := msg.Error
				if e == "" {
					e = "no vectors"
				}
				ch <- queryAnswer{err: errors.New(e)}
				continue
			}
			vecs := make([][]byte, len(msg.Vectors))
			for i, v := range msg.Vectors {
				vecs[i], _ = base64.StdEncoding.DecodeString(v)
			}
			ch <- queryAnswer{vecs: vecs}
		}
		err := cmd.Wait()
		why := "the query embedder stopped"
		if ee, ok := err.(*exec.ExitError); ok {
			why += fmt.Sprintf(" (exit %d)", ee.ExitCode())
		} else if err == nil {
			why += " (exit 0)"
		}
		if t := errTail.last(); t != "" {
			why += ": " + t
		}
		settle(errors.New(why))
		w.stop(errors.New(why))
		q.mu.Lock()
		if q.w == w {
			q.w = nil
		}
		q.mu.Unlock()
	}()
	go func() {
		select {
		case <-w.ready:
		case <-time.After(queryStart):
			settle(errors.New("the query embedder did not start in time"))
		}
	}()
	return w, nil
}

// Embed is embedQueries: one vector per text, 384 little-endian float32
// values (1,536 bytes) each.
func (q *Queries) Embed(texts []string) ([][]byte, error) {
	if d := q.Down(); d != "" {
		return nil, errors.New(d)
	}
	q.mu.Lock()
	if q.w == nil {
		w, err := q.start()
		if err != nil {
			q.down, q.downTil = err.Error(), time.Now().Add(queryRetry)
			q.mu.Unlock()
			return nil, err
		}
		q.w = w
	}
	w := q.w
	q.mu.Unlock()
	<-w.ready
	if w.readyOK != nil {
		q.mu.Lock()
		q.down, q.downTil = w.readyOK.Error(), time.Now().Add(queryRetry)
		if q.w == w {
			q.w = nil
		}
		q.mu.Unlock()
		w.stop(w.readyOK)
		return nil, w.readyOK
	}
	q.mu.Lock()
	if q.idle != nil {
		q.idle.Stop()
	}
	q.idle = time.AfterFunc(queryIdle, q.Stop)
	q.nextID++
	id := q.nextID
	q.mu.Unlock()
	ch := make(chan queryAnswer, 1)
	w.mu.Lock()
	if w.gone {
		w.mu.Unlock()
		return nil, errors.New("the query embedder stopped")
	}
	w.pending[id] = ch
	w.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"id": id, "texts": texts})
	if _, err := w.stdin.Write(append(b, '\n')); err != nil {
		w.mu.Lock()
		delete(w.pending, id)
		w.mu.Unlock()
		return nil, err
	}
	select {
	case a := <-ch:
		return a.vecs, a.err
	case <-time.After(queryRequest):
		w.mu.Lock()
		delete(w.pending, id)
		w.mu.Unlock()
		return nil, errors.New("the query embedder did not answer in time")
	}
}

// tail keeps the last 2,000 bytes written to it.
type tail struct {
	mu sync.Mutex
	b  []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 2000 {
		t.b = t.b[len(t.b)-2000:]
	}
	return len(p), nil
}

func (t *tail) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.b)), "\n")
	return lines[len(lines)-1]
}
