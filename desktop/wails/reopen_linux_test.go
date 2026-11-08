package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// startShell runs a copy of this test binary as tree/bin/activate-studio-wk41, a
// stand-in for the window's program.
func startShell(t *testing.T, tree string) (*exec.Cmd, string) {
	t.Helper()
	b := selfProgram(t)
	exe := filepath.Join(tree, "bin", "activate-studio-wk41")
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
	return cmd, exe
}

func TestShellChangedWhenItsFileWasReplaced(t *testing.T) {
	root := t.TempDir()
	tree := filepath.Join(root, "activate-studio")
	cmd, inst := startShell(t, tree)
	self := fmt.Sprintf("/proc/%d/exe", cmd.Process.Pid)
	if shellChangedFrom(self, inst) {
		t.Fatal("the program the install holds counts as changed")
	}
	// An update: the tree goes beside the app, and the new one holds a
	// program with the same bytes.
	b, _ := os.ReadFile(inst)
	prev := filepath.Join(root, ".activate-studio.previous")
	if err := os.Rename(tree, prev); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(tree, "bin"), 0o755)
	os.WriteFile(inst, b, 0o755)
	if !shellChangedFrom(self, inst) {
		t.Fatal("a shell running from the copy beside the app does not count as changed")
	}
	// The next update deletes that copy: the program runs from a deleted
	// file, and stat still follows it.
	os.RemoveAll(prev)
	if !shellChangedFrom(self, inst) {
		t.Fatal("a shell whose own file was deleted does not count as changed")
	}
	// No program at the install's path: nothing to reopen into.
	os.Remove(inst)
	if shellChangedFrom(self, inst) {
		t.Fatal("changed, with no program in the install")
	}
}
