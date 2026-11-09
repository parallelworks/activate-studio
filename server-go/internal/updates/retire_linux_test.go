package updates

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// A copy of this test binary stands in for a running program (an ELF file
// everywhere, where sleep can be a script or a multi-call binary): with
// STUDIO_TEST_SLEEP=1 it only sleeps.
func init() {
	if os.Getenv("STUDIO_TEST_SLEEP") == "1" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

func selfProgram(t *testing.T) []byte {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Skip(err)
	}
	return b
}

// A process whose program was deleted from an earlier copy does not hold
// the copy now beside the app, though its program's last name is there.
func TestInUseIgnoresDeletedPrograms(t *testing.T) {
	b := selfProgram(t)
	prev := filepath.Join(t.TempDir(), ".activate-studio.previous")
	start := func(name string) *exec.Cmd {
		exe := filepath.Join(prev, "bin", name)
		os.MkdirAll(filepath.Dir(exe), 0o755)
		if err := os.WriteFile(exe, b, 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(exe)
		cmd.Env = append(os.Environ(), "STUDIO_TEST_SLEEP=1")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
		return cmd
	}
	gone, kept := start("activate-studio-wk41"), start("studio-server")
	os.Remove(filepath.Join(prev, "bin", "activate-studio-wk41"))
	pids := procsUnder(prev)
	if slices.Contains(pids, gone.Process.Pid) {
		t.Fatal("a process running a deleted program counts as in use")
	}
	if !slices.Contains(pids, kept.Process.Pid) {
		t.Fatal("a process running from the copy does not count as in use")
	}
}
