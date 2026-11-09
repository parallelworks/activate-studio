package main

import (
	"os/exec"
	"testing"
)

// The guard is the running program, so a file replaced at the shell's path
// (an update or a rollback under a running window) is not what runs.
func TestGuardRunsThisProgram(t *testing.T) {
	cmd := exec.Command("/bin/bash", "studio-run.sh", "--once")
	guardCommand(cmd)
	if cmd.Path != "/proc/self/exe" {
		t.Errorf("the guard runs %s, want /proc/self/exe", cmd.Path)
	}
	if len(cmd.Args) < 4 || cmd.Args[1] != "--guard" || cmd.Args[2] != "/bin/bash" || cmd.Args[3] != "studio-run.sh" {
		t.Errorf("guard arguments %q", cmd.Args)
	}
}
