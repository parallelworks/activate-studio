package agentsites

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// This computer is a pw code site on a Studio of one only while the Studio
// has ACTIVATE: its pw code signs in with it.
func TestThisComputerIsASiteOnlyWithActivate(t *testing.T) {
	site := func(env map[string]string) (*Sites, *platform.Platform) {
		env["PW_CREDENTIALS_DIR"] = t.TempDir()
		p := platform.New(platform.Options{IndexBase: t.TempDir(), Home: t.TempDir(), LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }})
		return &Sites{P: p}, p
	}
	if s, _ := site(map[string]string{}); s.LocalSiteAllowed(false) {
		t.Fatal("no ACTIVATE at all")
	}
	s, p := site(map[string]string{"PW_API_KEY": "owner-key"})
	if !s.LocalSiteAllowed(false) || s.LocalSiteAllowed(true) {
		t.Fatal("with ACTIVATE: a site on a Studio of one only")
	}
	os.WriteFile(filepath.Join(p.IndexBase(), "access.json"), []byte(`{"stopped": true}`), 0o644)
	p.Stop(true)
	if s.LocalSiteAllowed(false) {
		t.Fatal("after Stop using the pw CLI")
	}
	if s, _ := site(map[string]string{"PW_API_KEY": "owner-key", "AGENT_LOCAL_SITE": "0"}); s.LocalSiteAllowed(false) {
		t.Fatal("AGENT_LOCAL_SITE=0")
	}
}
