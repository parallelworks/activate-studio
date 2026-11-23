package pwcode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// After Stop using the pw CLI, the Studio does not start a pw code daemon,
// which would run on the CLI's sign-in: it says so, and runs no pw CLI.
func TestStoppedStudioDoesNotStartPwCode(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	cli := filepath.Join(dir, "pw")
	os.WriteFile(cli, []byte("#!/bin/sh\necho \"$@\" >> "+log+"\n"), 0o755)
	p := newPlatform(t, map[string]string{"PW_CLI": cli, "PW_CODE_SOCKET": filepath.Join(dir, "none.sock")})
	if err := p.Stop(true); err != nil {
		t.Fatal(err)
	}
	_, err := New(p).EnsureDaemon(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not using the pw CLI") {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Fatalf("the pw CLI ran: %s", b)
	}
}
