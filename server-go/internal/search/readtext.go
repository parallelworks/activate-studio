package search

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// IndexTextCap is INDEX_TEXT_CAP: the most characters of a file the
// index keeps.
const IndexTextCap = 500_000

// IndexedText is what a library's index holds for one file (gufi.ts
// IndexedText): State "text" with Text, "none", or "unreadable" with Error.
type IndexedText struct {
	State string
	Text  string
	Error string
}

// IndexedTextState is indexedTextState: the text a library's index holds
// for one file, from the words table in its folder, without the
// converter's provenance line; an index on another system is read there.
func (e *Engine) IndexedTextState(ctx context.Context, indexRoot, dir, name string) IndexedText {
	args := []string{"-n", "1", "--min-level", "0", "--max-level", "0", "-E", "SELECT wordf FROM words WHERE fname = " + q(name) + " LIMIT 1;"}
	var out string
	var err error
	if strings.HasPrefix(indexRoot, libraries.RemotePrefix) {
		out, err = e.RemoteQuery(ctx, indexRoot, args, dir)
	} else {
		target := indexRoot
		if dir != "" {
			target = filepath.Join(indexRoot, dir)
		}
		out, err = e.runLocal(ctx, append(args, target), 60*time.Second)
	}
	if err != nil {
		why, _, _ := strings.Cut(err.Error(), "\n")
		return IndexedText{State: "unreadable", Error: jsSlice(why, 200)}
	}
	text := gufi.WithoutProvenance(strings.TrimSuffix(out, "\n"))
	if strings.TrimSpace(text) == "" {
		return IndexedText{State: "none"}
	}
	return IndexedText{State: "text", Text: text}
}

// GrepFallback is the chat tools' grepFallback: files under kbRoot whose
// text holds query, case-insensitively, for a search with no GUFI here.
// Paths relative to kbRoot, at most limit.
func GrepFallback(ctx context.Context, query, kbRoot string, limit int) []string {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.Command("grep", "-ril", "--include=*.md", "--include=*.txt", "--include=*.py", "--include=*.yaml",
		"--exclude-dir=.git", "--exclude-dir=node_modules", "--exclude-dir=.venv", "--exclude-dir=__pycache__", query, kbRoot)
	ownGroup(cmd)
	var out capBuf
	cmd.Stdout = &out
	if cmd.Start() != nil {
		return []string{}
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			killGroup(cmd)
		case <-done:
		}
	}()
	cmd.Wait()
	close(done)
	rels := []string{}
	for _, p := range strings.Split(out.String(), "\n") {
		if p == "" {
			continue
		}
		if len(rels) >= limit {
			break
		}
		rels = append(rels, strings.TrimPrefix(p, kbRoot+"/"))
	}
	return rels
}

// capBuf keeps at most 8 MiB, as execFile's maxBuffer.
type capBuf struct{ strings.Builder }

func (b *capBuf) Write(p []byte) (int, error) {
	if room := 8<<20 - b.Len(); room < len(p) {
		if room > 0 {
			b.Builder.Write(p[:room])
		}
		return len(p), nil
	}
	return b.Builder.Write(p)
}
