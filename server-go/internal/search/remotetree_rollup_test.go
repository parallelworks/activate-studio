package search

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// A library on another system whose index was rolled up (gufi_rollup)
// lists one folder at a time, as one that was not: a rolled-up folder's
// database holds everything under it, which the tree leaves to the
// folders below. Run with the real GUFI tools where they are installed.
func TestRemoteTreeOfARolledUpIndex(t *testing.T) {
	gufi := "/opt/gufi/bin"
	if v := os.Getenv("GUFI_BIN"); v != "" {
		gufi = v
	}
	for _, tool := range []string{"gufi_query", "gufi_dir2index", "gufi_rollup"} {
		if _, err := os.Stat(filepath.Join(gufi, tool)); err != nil {
			t.Skipf("no %s in %s", tool, gufi)
		}
	}
	e := scopeEngine(t)
	home := filepath.Join(filepath.Dir(e.IndexBase), "home")
	bin := filepath.Join(home, "gufi", "bin", "gufi_query")
	os.Remove(bin)
	if err := os.Symlink(filepath.Join(gufi, "gufi_query"), bin); err != nil {
		t.Fatal(err)
	}
	// top/{f-top.txt, adumpre/, cgiparse/{news/, AI/{aix/, apple/}}}, a
	// file in each folder.
	src := filepath.Join(t.TempDir(), "top")
	for _, d := range []string{"", "adumpre", "cgiparse", "cgiparse/news", "cgiparse/AI", "cgiparse/AI/aix", "cgiparse/AI/apple"} {
		dir := filepath.Join(src, d)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "f-"+filepath.Base(dir)+".txt"), []byte("x"), 0o644)
	}
	idx := filepath.Join(home, "idx", "a")
	os.RemoveAll(idx)
	for _, c := range [][]string{{filepath.Join(gufi, "gufi_dir2index"), "-n", "2", src, idx}, {filepath.Join(gufi, "gufi_rollup"), idx}} {
		if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", c[0], err, out)
		}
	}
	libs, _, err := e.Reg.Scope([]string{"u-a"}, libraries.ViewerOf(""), "kb")
	if err != nil || len(libs) != 1 {
		t.Fatalf("library: %v %v", libs, err)
	}
	names := func(rel string) []string {
		es, err := e.RemoteTree(context.Background(), libs[0].IndexRoot, rel)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		var out []string
		for _, x := range es {
			s := x.Path
			if x.Kind == kb.KindDir {
				s += "/"
			}
			out = append(out, s)
		}
		return out
	}
	for rel, want := range map[string][]string{
		"":                {"top/"},
		"top":             {"top/adumpre/", "top/cgiparse/", "top/f-top.txt"},
		"top/cgiparse":    {"top/cgiparse/AI/", "top/cgiparse/news/", "top/cgiparse/f-cgiparse.txt"},
		"top/cgiparse/AI": {"top/cgiparse/AI/aix/", "top/cgiparse/AI/apple/", "top/cgiparse/AI/f-AI.txt"},
	} {
		if got := names(rel); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, want %v", rel, got, want)
		}
	}
}
