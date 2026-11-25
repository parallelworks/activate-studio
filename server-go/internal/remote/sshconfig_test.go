package remote

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeConfig(t *testing.T, f, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestConfigHosts: the hosts an ssh config names, as names alone.
func TestConfigHosts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".ssh")
	file := writeConfig(t, filepath.Join(dir, "config"), `# my hosts
Include conf.d/*.conf
Include ~/more-hosts
Host example-box lab-2 *.example.com !lab-9 lab-?
  HostName 192.0.2.10
  User alex
  IdentityFile ~/.ssh/id_example
host=workstation
Host "quoted-box" example-box   # again
	Port 2222
Host name@with-at -oProxyCommand=x bad;name
Match host inner-box exec "true"
  User other
  Include matched.conf
Host one-box
  Include only-for-one.conf
Host *
  ServerAliveInterval 30
  Include last.conf
`)
	writeConfig(t, filepath.Join(dir, "conf.d", "a.conf"), "Host first-box\n  Include deeper.conf\nMatch all\nInclude deeper.conf\n")
	writeConfig(t, filepath.Join(dir, "deeper.conf"), "Host deep-box\n")
	writeConfig(t, filepath.Join(dir, "conf.d", "b.txt"), "Host not-included\n")
	writeConfig(t, filepath.Join(home, "more-hosts"), "Host more-box\r\n")
	writeConfig(t, filepath.Join(dir, "matched.conf"), "Host matched-box\n")
	writeConfig(t, filepath.Join(dir, "only-for-one.conf"), "Host nested-box\n")
	writeConfig(t, filepath.Join(dir, "last.conf"), "Host last-box\nInclude last.conf\n")
	want := []string{"first-box", "deep-box", "more-box", "example-box", "lab-2", "workstation", "quoted-box", "one-box", "last-box"}
	if got := ConfigHosts(file); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
}

// TestConfigHostsMissing: no file is no hosts, and a file that includes
// itself is read to ssh's own depth.
func TestConfigHostsMissing(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{filepath.Join(dir, "none"), dir} {
		if got := ConfigHosts(f); got == nil || len(got) != 0 {
			t.Errorf("%s: %v", f, got)
		}
	}
	self := writeConfig(t, filepath.Join(dir, "config"), "Include config\nHost example-box\n")
	if got := ConfigHosts(self); !reflect.DeepEqual(got, []string{"example-box"}) {
		t.Errorf("%v", got)
	}
}
