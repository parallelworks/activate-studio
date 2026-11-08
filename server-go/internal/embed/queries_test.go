package embed_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/embed"
	"github.com/parallelworks/activate-studio/server-go/internal/embed/cli"
)

// The test binary stands in for `studio-server embed-query` when run with
// EMBED_TEST_CHILD=query.
func TestMain(m *testing.M) {
	if os.Getenv("EMBED_TEST_CHILD") == "query" {
		os.Exit(cli.QueryMain(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func modelDir(t *testing.T) string {
	d := os.Getenv("EMBED_MODEL_DIR")
	if d == "" {
		d = "/shared/ade-studio/index/models/minilm-l6"
	}
	if _, err := embed.FindLibrary(); !embed.ModelPresent(d) || err != nil {
		t.Skip("needs the ONNX model and onnxruntime (EMBED_MODEL_DIR, STUDIO_ORT_LIB)")
	}
	return d
}

func TestQueriesThroughTheChild(t *testing.T) {
	dir := modelDir(t)
	exe, _ := os.Executable()
	q := &embed.Queries{Dir: dir, Command: []string{exe, "--onnx", dir}, Env: append(os.Environ(), "EMBED_TEST_CHILD=query")}
	defer q.Stop()
	v, err := q.Embed([]string{"a quokka on an island", "turbulence in a jet engine"})
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 2 || len(v[0]) != 1536 {
		t.Fatalf("%d vectors", len(v))
	}
	// The same as in this process for the same batch (the model quantizes
	// its activations per batch, so the batch is part of the answer).
	e, err := embed.New(dir, embed.Options{Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	w, _ := e.Embed([]string{"a quokka on an island", "turbulence in a jet engine"})
	if string(w[0]) != string(v[0]) || string(w[1]) != string(v[1]) {
		t.Fatal("the child's vectors differ")
	}
	// A child that cannot start says why, and is not tried again at once.
	bad := &embed.Queries{Dir: filepath.Join(t.TempDir(), "none"), Command: []string{exe, "--onnx", "/nonexistent"}, Env: append(os.Environ(), "EMBED_TEST_CHILD=query")}
	if _, err := bad.Embed([]string{"x"}); err == nil || bad.Down() == "" {
		t.Fatalf("%v %q", err, bad.Down())
	}
}
