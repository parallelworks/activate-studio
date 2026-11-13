package agentsites

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// fakeSSH is a pw whose ssh runs the command here, as on a cluster that
// shares this machine's files; with blind set it answers as one that does
// not, printing nothing for the library check.
func fakeSSH(t *testing.T, blind bool) string {
	p := filepath.Join(t.TempDir(), "pw")
	check := `sh -c "$1"`
	if blind {
		check = `case "$1" in *` + LibraryMark + `*) ;; *) echo /home/u/studio-agents/ws ;; esac`
	}
	os.WriteFile(p, []byte(`#!/bin/sh
case "$1" in
  ssh) shift; [ "$1" = --platform-host ] && shift 2; shift; `+check+` ;;
esac
`), 0o755)
	return p
}

func TestStartInLibrary(t *testing.T) {
	ctx := context.Background()
	lib := filepath.Join(t.TempDir(), "my library")
	os.MkdirAll(lib, 0o755)

	f := newFixture(t, map[string]string{"PW_CLI": fakeSSH(t, false)})
	r, err := f.s.Start(ctx, StartAgent{Site: "cluster-a", Key: "user-key", Objective: "Write the survey report", Library: lib}, false)
	if err != nil {
		t.Fatal(err)
	}
	rel := jsonv.String(jsonv.Path(r, "library"))
	ws := jsonv.String(jsonv.Path(r, "workspace"))
	if !strings.HasPrefix(rel, "agents/") || !strings.HasSuffix(rel, "-write-the-survey-report") || ws != filepath.Join(lib, rel) {
		t.Fatalf("library %q workspace %q", rel, ws)
	}
	if _, err := os.Stat(filepath.Join(ws, LibraryMark)); !os.IsNotExist(err) {
		t.Error("the check file was left behind")
	}
	if got, ok := InLibrary(lib, ws); !ok || got != rel {
		t.Errorf("InLibrary = %q %v", got, ok)
	}

	r, err = f.s.Start(ctx, StartAgent{Site: "cluster-a", Key: "user-key", Objective: "x", Library: lib, Folder: "reports/q4/"}, false)
	if err != nil || jsonv.Path(r, "library") != "reports/q4" || jsonv.Path(r, "workspace") != filepath.Join(lib, "reports", "q4") {
		t.Fatal(string(jsonv.MustMarshal(r)), err)
	}
	for _, bad := range []string{"../out", ".hidden/x", "a/../../b"} {
		if _, err := f.s.Start(ctx, StartAgent{Site: "cluster-a", Key: "user-key", Objective: "x", Library: lib, Folder: bad}, false); err == nil || !strings.HasPrefix(err.Error(), "folder:") {
			t.Errorf("%s: %v", bad, err)
		}
	}

	// A cluster that does not see the library works in ~/studio-agents,
	// and the folder made here for the check is gone again.
	lib2 := t.TempDir()
	g := newFixture(t, map[string]string{"PW_CLI": fakeSSH(t, true)})
	r, err = g.s.Start(ctx, StartAgent{Site: "cluster-a", Key: "user-key", Objective: "x", Library: lib2}, false)
	if err != nil || jsonv.Path(r, "workspace") != "/home/u/studio-agents/ws" || jsonv.Path(r, "library") != jsonv.Undefined {
		t.Fatal(string(jsonv.MustMarshal(r)), err)
	}
	if left, _ := os.ReadDir(lib2); len(left) != 0 {
		t.Errorf("left in the library: %v", left)
	}
	if _, err := g.s.Start(ctx, StartAgent{Site: "cluster-a", Key: "user-key", Objective: "x", Library: lib2, Folder: "reports"}, false); err == nil || !strings.Contains(err.Error(), "does not see this Studio's library") {
		t.Fatal(err)
	}

	if _, ok := InLibrary(lib, filepath.Dir(lib)); ok {
		t.Error("a folder above the library counted as in it")
	}
	if _, ok := InLibrary(lib, lib); ok {
		t.Error("the library's top counted as a folder in it")
	}
}
