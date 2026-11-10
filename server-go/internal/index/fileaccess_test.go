//go:build !windows

package index

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/extract"
	"github.com/parallelworks/activate-studio/server-go/internal/posixaccess"
)

// With text following file permissions (a root build, here set by the
// setting), a file whose mode is narrower than its folder's keeps its
// name in the index and no words: anyone who can enter the folder can
// read its db.db. The extract cache is made private. With folder level
// text, the default for a Studio running as its own user, both files'
// words are written, as before.
func TestWordsFollowFilePermissions(t *testing.T) {
	kb, index := t.TempDir(), t.TempDir()
	cache := filepath.Join(t.TempDir(), "extract")
	src := filepath.Join(kb, "shared")
	os.MkdirAll(src, 0o755)
	os.Chmod(src, 0o755)
	os.WriteFile(filepath.Join(src, "open.txt"), []byte("open quokka"), 0o644)
	os.WriteFile(filepath.Join(src, "closed.txt"), []byte("closed narwhal"), 0o600)
	os.Chmod(filepath.Join(src, "closed.txt"), 0o600)
	dbDir := filepath.Join(index, "shared")
	os.MkdirAll(dbDir, 0o755)
	db := filepath.Join(dbDir, "db.db")
	os.WriteFile(db, nil, 0o644)

	found := func() string {
		h, err := sql.Open("sqlite", db)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Close()
		var got []string
		for _, w := range []string{"quokka", "narwhal"} {
			var n int
			h.QueryRow("SELECT count(*) FROM words WHERE words MATCH ?", w).Scan(&n)
			if n > 0 {
				got = append(got, w)
			}
		}
		return fmt.Sprint(got)
	}

	t.Setenv(posixaccess.Env, "file")
	x, err := extract.New(extract.Options{Tools: &extract.Tools{}, Jobs: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.Run(context.Background(), extract.PassOptions{KBRoot: kb, CacheRoot: cache}); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(cache); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("cache: %v %v", st, err)
	}
	res, err := WriteWords(context.Background(), WordsOptions{KBRoot: kb, Index: index, Cache: cache, Jobs: 1})
	if err != nil || res.Files != 1 || res.NameOnly != 1 {
		t.Fatalf("file level: %+v, %v", res, err)
	}
	if got := found(); got != "[quokka]" {
		t.Fatalf("file level found %s", got)
	}

	// Rebuilding with folder level text writes both again, and the
	// opposite way round removes the narrower file's words.
	t.Setenv(posixaccess.Env, "folder")
	res, err = WriteWords(context.Background(), WordsOptions{KBRoot: kb, Index: index, Cache: cache, Jobs: 1})
	if err != nil || res.Files != 2 || res.NameOnly != 0 {
		t.Fatalf("folder level: %+v, %v", res, err)
	}
	if got := found(); got != "[quokka narwhal]" {
		t.Fatalf("folder level found %s", got)
	}
	t.Setenv(posixaccess.Env, "file")
	if _, err := WriteWords(context.Background(), WordsOptions{KBRoot: kb, Index: index, Cache: cache, Jobs: 1}); err != nil {
		t.Fatal(err)
	}
	if got := found(); got != "[quokka]" {
		t.Fatalf("rebuilt at file level found %s", got)
	}
}
