package userlibs

import (
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// Libraries over pw ssh act with their owner's own key: never the
// deployment's on a Studio several people use, and none after Stop using
// the pw CLI on a Studio of one.
func TestRemoteLibrariesNeverUseTheDeploymentCredential(t *testing.T) {
	env := map[string]string{"PW_API_KEY": "deploy-key"}
	p := platform.New(platform.Options{IndexBase: t.TempDir(), Home: t.TempDir(), LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }})
	m := &Module{P: p}
	if k := m.keyOf(who{ID: "u1", Auth: true}, "u1"); k != "" {
		t.Fatalf("a person without a key of their own got %q", k)
	}
	if k := m.keyOf(who{ID: "u1", Auth: true, Own: "mine"}, "u1"); k != "mine" {
		t.Fatal(k)
	}
	if k := m.keyOf(who{}, single); k != "deploy-key" {
		t.Fatal("on a Studio of one, the owner's:", k)
	}
	if err := p.Stop(true); err != nil {
		t.Fatal(err)
	}
	if k := m.keyOf(who{}, single); k != "" {
		t.Fatal("after Stop using the pw CLI:", k)
	}
}
