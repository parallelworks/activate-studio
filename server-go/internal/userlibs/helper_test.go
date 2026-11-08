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
