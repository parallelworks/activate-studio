// Package cli is the command line of the embedder, run as subcommands of
// studio-server so a crash in the native ONNX Runtime library ends a
// child, never the server:
//
//	studio-server embed --index DIR [--model GGUF] [--onnx DIR] [--backend auto|onnx|lembed]
//	  [--max-tokens 128|256] [--gufi-sqlite3 PATH] [--subdir REL] [--no-recurse] [--jobs N]
//	  [--cache FILE] [--prune] [--max-chunks N] [--documents-only] [--progress] [--report FILE]
//
// is indexer/embed.py, with its arguments, output lines and exit codes, and
//
//	studio-server embed-query --onnx DIR
//
// is indexer/embed_query.py: JSON lines on stdin and stdout, as
// embedWorker.ts speaks them.
package cli

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/parallelworks/activate-studio/server-go/internal/embed"
)

// Main is `embed`; it returns the exit status: 0, 1 when the pass failed,
// 2 for a bad command line.
func Main(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("embed", flag.ContinueOnError)
	fs.SetOutput(stderr)
	o := embed.PassOptions{}
	fs.StringVar(&o.Index, "index", "", "GUFI index tree top")
	fs.StringVar(&o.Model, "model", "", "GGUF embedding model path, for the lembed backend")
	fs.StringVar(&o.ONNX, "onnx", "", "folder with model.onnx and tokenizer.json (default: minilm-l6 beside --model)")
	fs.StringVar(&o.Backend, "backend", "auto", "auto: ONNX when its library and model are here, else lembed")
	fs.IntVar(&o.MaxTokens, "max-tokens", 256, "ONNX backend: tokens of each passage embedded")
	gufiBin := os.Getenv("GUFI_BIN")
	if gufiBin == "" {
		gufiBin = "/opt/gufi/bin"
	}
	fs.StringVar(&o.GufiSqlite3, "gufi-sqlite3", filepath.Join(gufiBin, "gufi_sqlite3"), "gufi_sqlite3 binary")
	fs.StringVar(&o.Subdir, "subdir", "", "restrict to this KB-relative subtree")
	fs.BoolVar(&o.NoRecurse, "no-recurse", false, "process only the subdir itself, not its children")
	fs.IntVar(&o.Jobs, "jobs", 0, "threads (default: the CPU count)")
	fs.StringVar(&o.Cache, "cache", "", "embedding cache (SQLite), reused across runs")
	fs.BoolVar(&o.Prune, "prune", false, "drop cache entries this index no longer uses (whole-index runs only)")
	fs.IntVar(&o.MaxChunks, "max-chunks", embed.MaxChunks, "passages embedded per file, from its start")
	fs.BoolVar(&o.DocumentsOnly, "documents-only", false, "embed documents and images only")
	progress := fs.Bool("progress", false, "print @progress lines on stdout")
	report := fs.String("report", "", "write what was embedded to this JSON file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if o.Index == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: studio-server embed --index DIR [options]")
		return 2
	}
	switch o.Backend {
	case "auto", "onnx", "lembed":
	default:
		fmt.Fprintln(stderr, "embed: --backend must be auto, onnx or lembed")
		return 2
	}
	if o.MaxTokens != 128 && o.MaxTokens != 256 {
		fmt.Fprintln(stderr, "embed: --max-tokens must be 128 or 256")
		return 2
	}
	o.Scratch = os.Getenv("STUDIO_TMP")
	var mu sync.Mutex
	out := &lockedWriter{w: stdout, mu: &mu}
	o.Out, o.Err = out, &lockedWriter{w: stderr, mu: &mu}
	if *progress {
		o.Progress = func(step string, done, total int) {
			b, _ := json.Marshal(map[string]any{"phase": "meaning", "step": step, "done": done, "total": total})
			fmt.Fprintf(out, "@progress %s\n", b)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := embed.Pass(ctx, o)
	if err != nil {
		fmt.Fprintln(o.Err, err)
		return 1
	}
	if *report != "" {
		if err := os.WriteFile(*report, res.MarshalReport(), 0o644); err != nil {
			fmt.Fprintln(o.Err, err)
			return 1
		}
	}
	return 0
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// QueryMain is `embed-query`: it prints {"ready": true, "model": ...} (or
// {"ready": false, "error": ...} and exits 1), then answers each
// {"id", "texts"} line with {"id", "vectors"} (base64 blobs) or
// {"id", "error"}, until its input closes.
func QueryMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("embed-query", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("onnx", "", "folder with model.onnx and tokenizer.json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprintln(stderr, "usage: studio-server embed-query --onnx DIR")
		return 2
	}
	enc := json.NewEncoder(stdout)
	send := func(v any) { enc.Encode(v) }
	e, err := embed.New(*dir, embed.Options{Threads: 1})
	if err != nil {
		send(map[string]any{"ready": false, "error": err.Error()})
		return 1
	}
	defer e.Close()
	send(map[string]any{"ready": true, "model": embed.ModelID})
	sc := bufio.NewScanner(stdin)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(trimSpace(line)) == 0 {
			continue
		}
		var req struct {
			ID    any   `json:"id"`
			Texts []any `json:"texts"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			send(map[string]any{"id": nil, "error": err.Error()})
			continue
		}
		texts := make([]string, len(req.Texts))
		for i, t := range req.Texts {
			s, ok := t.(string)
			if !ok {
				b, _ := json.Marshal(t)
				s = string(b)
			}
			texts[i] = headRunes(s, 2000)
		}
		blobs, err := e.Embed(texts)
		if err != nil {
			send(map[string]any{"id": req.ID, "error": err.Error()})
			continue
		}
		vecs := make([]string, len(blobs))
		for i, b := range blobs {
			vecs[i] = base64.StdEncoding.EncodeToString(b)
		}
		send(map[string]any{"id": req.ID, "vectors": vecs})
	}
	return 0
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\r' || b[0] == '\n') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t' || b[len(b)-1] == '\r' || b[len(b)-1] == '\n') {
		b = b[:len(b)-1]
	}
	return b
}

func headRunes(s string, n int) string {
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}
