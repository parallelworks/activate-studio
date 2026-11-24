package libraries

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeploymentKeysAfterStopUsingThePwCLI(t *testing.T) {
	ix := t.TempDir()
	get := func(k string) string { return map[string]string{"PW_API_KEY": "owner-key"}[k] }
	k := DeploymentKeys{Getenv: get, IndexBase: ix}
	if k.KeyOf("single") != "owner-key" {
		t.Fatal("the owner's")
	}
	if (DeploymentKeys{Getenv: get, IndexBase: ix, AuthEnabled: true}).KeyOf("single") != "" {
		t.Fatal("a Studio several people use")
	}
	os.WriteFile(filepath.Join(ix, "access.json"), []byte(`{"stopped":true}`), 0o644)
	if k.KeyOf("single") != "" {
		t.Fatal("after Stop using the pw CLI")
	}
}
