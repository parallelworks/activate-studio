//go:build !windows

package main

// The opt-in to the Go front server (STUDIO_GO_SERVER=1): which binaries it
// finds, what the supervisor passes on, and the payload's own studio-run.sh
// starting studio-server in place of Node.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func touch(t *testing.T, file string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
}

func TestGoServerPaths(t *testing.T) {
	tree := t.TempDir()
	touch(t, filepath.Join(tree, "app", "bin", "studio-server"), 0o755)
	touch(t, filepath.Join(tree, "app", "bin", "semantic-map"), 0o755)
	checkout := t.TempDir()
	touch(t, filepath.Join(checkout, "server-go", "bin", "studio-server"), 0o755)
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

	if s, m := goServerPaths(env(nil), tree, filepath.Join(tree, "app")); s != "" || m != "" {
		t.Errorf("off by default, got %q %q", s, m)
	}
	on := map[string]string{"STUDIO_GO_SERVER": "1"}
	if s, m := goServerPaths(env(on), tree, filepath.Join(tree, "app")); s != filepath.Join(tree, "app", "bin", "studio-server") || m != filepath.Join(tree, "app", "bin", "semantic-map") {
		t.Errorf("tree: %q %q", s, m)
	}
	// A checkout without a built semantic-map: the server's default map.
	if s, m := goServerPaths(env(on), "", checkout); s != filepath.Join(checkout, "server-go", "bin", "studio-server") || m != "" {
		t.Errorf("checkout: %q %q", s, m)
	}
	// No studio-server: Node, as before.
	if s, m := goServerPaths(env(on), "", t.TempDir()); s != "" || m != "" {
		t.Errorf("missing binary: %q %q", s, m)
	}
	other := filepath.Join(t.TempDir(), "my-server")
	touch(t, other, 0o755)
	if s, _ := goServerPaths(env(map[string]string{"STUDIO_GO_SERVER": "1", "STUDIO_GO_SERVER_BIN": other}), tree, ""); s != other {
		t.Errorf("STUDIO_GO_SERVER_BIN: %q", s)
	}
}

func TestCommandWithGoServer(t *testing.T) {
	t.Setenv("STUDIO_GO_SERVER_BIN", "/inherited/studio-server")
	data := t.TempDir()
	run := &Supervisor{RunScript: "/tree/studio-run.sh", DataDir: data}
	if env := run.command(4999, nil).Env; slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "STUDIO_GO_SERVER_BIN=") }) {
		t.Error("an inherited STUDIO_GO_SERVER_BIN reached the run script without the opt-in")
	}
	run.GoServer, run.SemanticMap = "/tree/app/bin/studio-server", "/tree/app/bin/semantic-map"
	env := run.command(4999, nil).Env
	for _, want := range []string{"STUDIO_GO_SERVER_BIN=/tree/app/bin/studio-server", "STUDIO_SEMANTIC_MAP_BIN=/tree/app/bin/semantic-map", "STUDIO_RUN_PORT=4999"} {
		if !slices.Contains(env, want) {
			t.Errorf("run script env lacks %s", want)
		}
	}

	direct := &Supervisor{Node: "/usr/bin/node", Script: "/app/server/dist/main.js", AppDir: "/app", DataDir: data, KBRoot: t.TempDir(),
		GoServer: "/app/server-go/bin/studio-server", SemanticMap: "/app/server-go/bin/semantic-map"}
	cmd := direct.command(4999, nil)
	if cmd.Path != "/app/server-go/bin/studio-server" {
		t.Errorf("started %s, want studio-server", cmd.Path)
	}
	for _, want := range []string{"STUDIO_NODE_SPAWN=1", "STUDIO_NODE_BIN=/usr/bin/node", "STUDIO_NODE_MAIN=/app/server/dist/main.js",
		"STUDIO_PROJECT_ROOT=/app", "STUDIO_SEMANTIC_MAP_BIN=/app/server-go/bin/semantic-map", "PORT=4999", "HOST=127.0.0.1"} {
		if !slices.Contains(cmd.Env, want) {
			t.Errorf("studio-server env lacks %s", want)
		}
	}
}

// The payload's studio-run.sh, with a stand-in studio-server that records
// its environment and then serves as the stand-in server does.
func TestRunScriptStartsTheGoServer(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "deploy", "linux", "studio-run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tree, data := t.TempDir(), t.TempDir()
	write := func(f, body string) {
		touch(t, filepath.Join(tree, f), 0o755)
		if err := os.WriteFile(filepath.Join(tree, f), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("studio-run.sh", string(src))
	write("node/bin/node", fmt.Sprintf("#!/bin/bash\necho node >\"$STUDIO_DATA_DIR/started\"\nexec env STUDIO_FAKE_SERVER=1 %q\n", exe))
	write("app/server/dist/main.js", "")
	write("app/bin/studio-server", fmt.Sprintf("#!/bin/bash\n{ echo studio-server; env | grep -E '^(STUDIO_NODE_|STUDIO_PROJECT_ROOT|STUDIO_SEMANTIC_MAP_BIN|PORT=|HOST=)' | sort; } >\"$STUDIO_DATA_DIR/started\"\nexec env STUDIO_FAKE_SERVER=1 %q\n", exe))
	write("app/bin/semantic-map", "")

	t.Setenv("SHELL", "/bin/false") // no login shell to wait on
	srv, smap := goServerPaths(func(k string) string { return map[string]string{"STUDIO_GO_SERVER": "1"}[k] }, tree, filepath.Join(tree, "app"))
	events := make(chan ServerEvent, 16)
	sup := &Supervisor{RunScript: filepath.Join(tree, "studio-run.sh"), DataDir: data, KBRoot: t.TempDir(), GoServer: srv, SemanticMap: smap,
		Events: func(e ServerEvent) { events <- e }}
	go sup.Run()
	t.Cleanup(sup.Stop)
	deadline := time.After(20 * time.Second)
	for ready := false; !ready; {
		select {
		case e := <-events:
			ready = e.Kind == "ready"
			if e.Kind == "failed" {
				t.Fatal(e.Message)
			}
		case <-deadline:
			t.Fatal("the server did not start")
		}
	}
	b, _ := os.ReadFile(filepath.Join(data, "started"))
	got := string(b)
	for _, want := range []string{"studio-server\n", "STUDIO_NODE_SPAWN=1\n", "STUDIO_NODE_BIN=" + filepath.Join(tree, "node", "bin", "node") + "\n",
		"STUDIO_NODE_MAIN=" + filepath.Join(tree, "app", "server", "dist", "main.js") + "\n", "STUDIO_PROJECT_ROOT=" + filepath.Join(tree, "app") + "\n",
		"STUDIO_SEMANTIC_MAP_BIN=" + filepath.Join(tree, "app", "bin", "semantic-map") + "\n", "HOST=127.0.0.1\n", fmt.Sprintf("PORT=%d\n", sup.Port())} {
		if !strings.Contains(got, want) {
			t.Errorf("studio-run.sh started:\n%s\nwant %q", got, want)
		}
	}
}
