package userlibs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// The Studio's helper, built once for this computer's platform, as the
// packages carry it (bin/remote/<platform>/studio-remote).

var (
	helpersOnce sync.Once
	helpersDir  string
	helpersErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if helpersDir != "" {
		os.RemoveAll(helpersDir)
	}
	os.Exit(code)
}

func testHelpers(t *testing.T) string {
	t.Helper()
	helpersOnce.Do(func() {
		dir, err := os.MkdirTemp("", "studio-remote-")
		if err != nil {
			helpersErr = err
			return
		}
		helpersDir = dir
		out := filepath.Join(dir, runtime.GOOS+"-"+runtime.GOARCH, helperName)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", "-s -w", "-o", out, "../../cmd/studio-remote")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			helpersErr = err
			t.Logf("%s", b)
		}
	})
	if helpersErr != nil {
		t.Fatalf("building the helper: %v", helpersErr)
	}
	return helpersDir
}

// The check names the helper a system gets by its uname, and says why one
// with none gets names only.
func TestTheCheckNamesTheHelperByPlatform(t *testing.T) {
	h, _ := buildHarness(t)
	fake := filepath.Join(h.dir, "fake-uname")
	for _, c := range []struct{ s, m, want string }{
		{"Darwin", "arm64", `^Text is extracted there by the Studio's own reader, which the first build sends \(about \d+ MB, once\)`},
		{"FreeBSD", "amd64", `^The Studio has no reader for me@linux-box \(FreeBSD amd64; it has them for Linux x86_64 and aarch64, and macOS arm64\), so the index has names and metadata only\.$`},
	} {
		exe(t, filepath.Join(fake, "uname"), "#!/bin/sh\ncase \"$1\" in -s) echo "+c.s+" ;; -m) echo "+c.m+" ;; *) echo "+c.s+" ;; esac\n")
		h.path["me@linux-box"] = fake
		h.m.Pool.Close()
		h.m.Pool = remote.New(remote.Options{IndexBase: h.m.IndexBase, Getenv: func(k string) string { return h.env[k] }, Spawn: h.spawn,
			SSHLocal: func([]string, time.Duration) remote.LocalResult { return remote.LocalResult{Code: 255} }})
		h.m.Runners = &remote.Runners{Pool: h.m.Pool}
		// A stand-in for the packages' darwin-arm64 helper.
		h.m.Helpers = filepath.Join(h.dir, "helpers")
		os.MkdirAll(filepath.Join(h.m.Helpers, "darwin-arm64"), 0o755)
		os.WriteFile(filepath.Join(h.m.Helpers, "darwin-arm64", helperName), []byte("stand-in"), 0o755)
		r := h.post("/api/user-libraries/ssh/build/test", map[string]any{"destination": "me@linux-box", "sourceRoot": "~/project"})
		match(t, stepDetail(r, "extract"), c.want, c.s)
	}
}
