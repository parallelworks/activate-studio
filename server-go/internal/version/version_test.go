package version

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStampThenPackage(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "build-info.json"), []byte(`{"version":"v1.200","commit":"abc1234","builtAt":"2026-11-01T00:00:00Z"}`), 0o644)
	i := Of(dir)
	if i.Version != "v1.200" || *i.Commit != "abc1234" || i.Source != "bundle" {
		t.Fatal(i)
	}
	b, _ := json.Marshal(i)
	var keys []string
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.Token()
	for dec.More() {
		k, _ := dec.Token()
		keys = append(keys, k.(string))
		var v any
		dec.Decode(&v)
	}
	if len(keys) != 5 || keys[0] != "version" || keys[4] != "source" {
		t.Fatal(keys)
	}
	pkg := t.TempDir()
	os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"version":"2.0.0"}`), 0o644)
	// No stamp and not a checkout: package.json.
	if p := fromPackage(pkg); p.Version != "v2.0.0" || p.Commit != nil || p.Source != "package" {
		t.Fatal(p)
	}
}
