package chat

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

// With STUDIO_PW_E2E=1 and the pw CLI on PATH: the steps the Studio runs
// on a machine, run here by a real pw under a scratch HOME (no sign-in, no
// settings of the person running the test): pw code takes the entry at
// user scope, the Studio reads it back as its own, and a second add with
// --force replaces it in place rather than adding another.
func TestTheStepsWithARealPW(t *testing.T) {
	if os.Getenv("STUDIO_PW_E2E") != "1" {
		t.Skip("STUDIO_PW_E2E=1 runs it")
	}
	pw, err := exec.LookPath("pw")
	if err != nil {
		t.Skip("no pw CLI")
	}
	home := t.TempDir()
	env := []string{"HOME=" + home, "PATH=" + filepath.Dir(pw) + ":/usr/bin:/bin", "XDG_CONFIG_HOME=" + filepath.Join(home, ".config")}
	sh := func(script string, args ...string) string {
		t.Helper()
		cmd := exec.Command("sh", append([]string{"-c", script, "studio-step"}, args...)...)
		cmd.Env, cmd.Dir = env, home
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return string(out)
	}
	url := "https://platform.example/me/session/ada/studio/api/mcp"
	if l := parseBridgeLook(sh(bridgeLook, studioMCPName)); l.pw == "" || l.entry != nil {
		t.Fatalf("before: %+v", l)
	}
	add := func(url string) {
		cmd := exec.Command(pw, "code", "mcp", "add", "--scope", "user", "--force", studioMCPName, "--", "sh", "-c", bridgeShim, bridgeShimName, url, pw)
		cmd.Env, cmd.Dir = env, home
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("add: %v: %s", err, out)
		}
	}
	add("https://platform.example/me/session/cy/old/api/mcp")
	add(url)
	l := parseBridgeLook(sh(bridgeLook, studioMCPName))
	if l.entry == nil {
		t.Fatalf("not read back: %s", sh(bridgeLook, studioMCPName))
	}
	at, ours, current := l.entry.target()
	if at != url || !ours || !current {
		t.Fatalf("read back as %q ours=%v current=%v: %+v", at, ours, current, l.entry.Config)
	}
	list := exec.Command(pw, "code", "mcp", "list", "--scope", "user")
	list.Env, list.Dir = env, home
	out, _ := list.CombinedOutput()
	if n := len(regexp.MustCompile(`(?m)^\s*studio\s+stdio\s`).FindAllString(string(out), -1)); n != 1 {
		t.Errorf("entries: %s", out)
	}
}
