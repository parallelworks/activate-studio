// Package version is version.ts: what build is running. A deployed Studio
// has no git checkout, so the build stamps itself into build-info.json at
// the project root and that is the authority; running from the repository
// the tag is read from git; else package.json's version.
package version

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Info is buildInfo()'s object, keys in Node's order.
type Info struct {
	// Version is the release tag, e.g. v1.0; "dev" when nothing is tagged.
	Version string `json:"version"`
	// Commit is the short commit the build came from, when known.
	Commit *string `json:"commit"`
	// BuiltAt is when the bundle was built, ISO, when known.
	BuiltAt *string `json:"builtAt"`
	// StartedAt is when this process started.
	StartedAt string `json:"startedAt"`
	// Source is where the answer came from: bundle, git or package.
	Source string `json:"source"`
}

var startedAt = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

var (
	mu     sync.Mutex
	cached = map[string]Info{}
)

// Of is the build at projectRoot, resolved once per root: the answer
// cannot change without the process restarting.
func Of(projectRoot string) Info {
	mu.Lock()
	defer mu.Unlock()
	if i, ok := cached[projectRoot]; ok {
		return i
	}
	i, ok := fromStamp(projectRoot)
	if !ok {
		i, ok = fromGit(projectRoot)
	}
	if !ok {
		i = fromPackage(projectRoot)
	}
	cached[projectRoot] = i
	return i
}

func strp(s string) *string { return &s }

func fromStamp(root string) (Info, bool) {
	b, err := os.ReadFile(filepath.Join(root, "build-info.json"))
	if err != nil {
		return Info{}, false
	}
	var raw map[string]any
	if json.Unmarshal(b, &raw) != nil {
		return Info{}, false
	}
	v := jsString(raw["version"])
	if v == "" {
		return Info{}, false
	}
	i := Info{Version: v, StartedAt: startedAt, Source: "bundle"}
	if c := jsString(raw["commit"]); c != "" {
		i.Commit = strp(c)
	}
	if t := jsString(raw["builtAt"]); t != "" {
		i.BuiltAt = strp(t)
	}
	return i, true
}

// jsString is String(v) for a truthy JSON value, "" for a falsy one.
func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if !x {
			return ""
		}
		return "true"
	case float64:
		if x == 0 {
			return ""
		}
		b, _ := json.Marshal(x)
		return string(b)
	default:
		return "[object Object]"
	}
}

func git(root string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

var vTag = regexp.MustCompile(`^v`)

func fromGit(root string) (Info, bool) {
	commit := git(root, "rev-parse", "--short", "HEAD")
	if commit == "" {
		return Info{}, false
	}
	// Exact tag if this commit is one, otherwise the last tag with a
	// distance suffix. Only the app's own tags: the repository also has
	// gufi-* tags.
	described := git(root, "describe", "--tags", "--match", "v*", "--always", "--dirty")
	i := Info{Version: "dev", Commit: strp(commit), StartedAt: startedAt, Source: "git"}
	if described != "" && vTag.MatchString(described) {
		i.Version = described
	}
	if t := git(root, "log", "-1", "--format=%cI"); t != "" {
		i.BuiltAt = strp(t)
	}
	return i, true
}

func fromPackage(root string) Info {
	version := "0.0.0"
	if b, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var p map[string]any
		if json.Unmarshal(b, &p) == nil && p["version"] != nil {
			switch v := p["version"].(type) {
			case string:
				version = v
			default:
				b, _ := json.Marshal(v)
				version = string(b)
			}
		}
	}
	return Info{Version: "v" + version, StartedAt: startedAt, Source: "package"}
}
