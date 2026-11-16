package search

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
// A folder without that text is read for the file's chunks in an external
// database in GUFI's common schema (ext_bm25), joined in order.
func (e *Engine) IndexedTextState(ctx context.Context, indexRoot, dir, name string) IndexedText {
	args := []string{"-n", "1", "--min-level", "0", "--max-level", "0", "-E", "SELECT wordf FROM words WHERE fname = " + q(name) + " LIMIT 1;"}
	got := e.indexedText(ctx, indexRoot, dir, args)
	if got.State == "text" {
		return got
	}
	remote := strings.HasPrefix(indexRoot, libraries.RemotePrefix)
	if ext := externalTextArgs(name); ext != nil && (remote || hasExternal(filepath.Join(indexRoot, dir))) {
		if alt := e.indexedText(ctx, indexRoot, dir, ext); alt.State == "text" {
			return alt
		}
	}
	return got
}

// hasExternal says whether a folder of an index here holds a file other
// than db.db that a walk attaches as an external database.
func hasExternal(dir string) bool {
	p := gufi.ExternalPattern()
	if p == nil {
		return false
	}
	re, err := regexp.Compile(*p)
	if err != nil {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Name() != "db.db" && e.Type().IsRegular() && re.MatchString(e.Name()) {
			return true
		}
	}
	return false
}

// externalTextArgs is a walk of one folder that joins a file's chunks in
// the folder's external databases (ext_bm25, matched to the entry by inode
// and mtime) in the order of their asset and sequence, kept to
// IndexTextCap characters; nil when external databases are off. GUFI
// attaches a folder's external databases only in a folder above
// --max-level, so the walk goes to level 1 and every statement keeps to
// level 0 (the table lasts from one folder to the next in a thread).
func externalTextArgs(name string) []string {
	p := gufi.ExternalPattern()
	if p == nil {
		return nil
	}
	return []string{"-n", "1", "--min-level", "0", "--max-level", "1",
		"-I", "CREATE TABLE c (a TEXT, s INT64, x TEXT);",
		"--external-copy", *p, "INSERT INTO c SELECT b.asset_id, CAST(b.chunk_sequence AS INT64), b.content FROM extdb.ext_bm25 b " +
			"JOIN vrpentries e ON CAST(b.inode AS INT64) = e.inode AND CAST(b.mtime AS INT64) = e.mtime WHERE level() = 0 AND e.name = " + q(name) + ";",
		"-E", fmt.Sprintf("SELECT substr(group_concat(x, char(10) || char(10)), 1, %d) FROM (SELECT x FROM c WHERE level() = 0 ORDER BY a, s) HAVING count(*) > 0;", IndexTextCap)}
}

func (e *Engine) indexedText(ctx context.Context, indexRoot, dir string, args []string) IndexedText {
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
