package platform

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The deployment's own ACTIVATE credential (GatewayKey, or a pw CLI run
// AsDeployment) may serve only a Studio of one, where it is the owner's,
// and the Studio's housekeeping that acts as no person. Work for a person
// on a Studio several people use takes their own credential (KeyFor).
// Each place that reads it is listed here with why; a new one fails this
// test until it is checked and listed.
var deploymentUses = map[string]struct {
	n   int
	why string
}{
	"access/access.go":           {2, "models from an OpenAI-compatible endpoint the administrator set (not ACTIVATE); subscriptions on a Studio of one"},
	"agentsites/agentsites.go":   {1, "AgentKey: a Studio of one only"},
	"app/providers.go":           {1, "GatewayConfigured without the chat track: says whether the deployment has one"},
	"chat/gotools.go":            {1, "the deployment credential the chat module holds (Module.GatewayKey)"},
	"chat/module.go":             {3, "GatewayKey itself; deploymentModelsKey: a Studio of one, or the administrator's OpenAI-compatible endpoint"},
	"chat/pwcodechat.go":         {1, "pw code on a Studio of one"},
	"chat/stream.go":             {1, "changes on a Studio of one (sign-in off)"},
	"chat/toolroutes.go":         {1, "tool options on a Studio of one (sign-in off)"},
	"chat/tooltypes.go":          {1, "envAccess: the deployment credential before the platform answers"},
	"chat/workflowtools.go":      {1, "platformKey on a Studio of one (sign-in off)"},
	"core/brand.go":              {1, "housekeeping: the deployment's own icon from the platform"},
	"extract/image.go":           {2, "housekeeping: image descriptions while indexing, when no person is present"},
	"ragendpoint/ragendpoint.go": {2, "housekeeping: registering the grounded model at /v1 as the deployment, which the administrator turns on"},
	"search/of.go":               {1, "OwnerKeys: a Studio of one only"},
	"userlibs/userlibs.go":       {1, "keyOf: a Studio of one only"},
	"workflows/runs/runs.go":     {1, "SessionKey on a Studio of one only"},
	"workflows/tab.go":           {5, "the Studio's workflow collection: reading the definitions kept in the deployment's account (never running or writing as it)"},
	"platform/token.go":          {1, "housekeeping: renewing the deployment's own pw CLI sign-in"},
}

var deploymentUse = regexp.MustCompile(`GatewayKey\(\)|AsDeployment\b|EnvironAsDeployment\(`)

func TestEveryUseOfTheDeploymentCredentialIsListed(t *testing.T) {
	root, _ := filepath.Abs("..")
	got := map[string]int{}
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		// Where it is defined and the CLI's own plumbing.
		if rel == "platform/viewer.go" || rel == "platform/cli.go" || rel == "platform/platform.go" {
			return nil
		}
		b, _ := os.ReadFile(path)
		for _, line := range strings.Split(string(b), "\n") {
			l := strings.TrimSpace(line)
			if strings.HasPrefix(l, "//") || strings.HasPrefix(l, "func (m *Module) GatewayKey") {
				continue
			}
			got[rel] += len(deploymentUse.FindAllString(l, -1))
		}
		return nil
	})
	for f, n := range got {
		if n == 0 {
			continue
		}
		if want, ok := deploymentUses[f]; !ok || want.n != n {
			t.Errorf("%s reads the deployment's credential %d times (listed: %d). Work for a person on a Studio several people use takes KeyFor; housekeeping that acts as no person is listed here with why.", f, n, want.n)
		}
	}
	for f, want := range deploymentUses {
		if got[f] != want.n {
			t.Errorf("%s is listed with %d uses, has %d", f, want.n, got[f])
		}
	}
}
