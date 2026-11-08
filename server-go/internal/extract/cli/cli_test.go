package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestExtractorsFile(t *testing.T) {
	if p, req := ExtractorsFile("/x.json", env(map[string]string{"STUDIO_EXTRACTORS": "/y.json"})); p != "/x.json" || !req {
		t.Errorf("flag: %s %v", p, req)
	}
	if p, req := ExtractorsFile("", env(map[string]string{"STUDIO_EXTRACTORS": "/y.json", "INDEX_BASE": "/b"})); p != "/y.json" || !req {
		t.Errorf("env: %s %v", p, req)
	}
	if p, req := ExtractorsFile("", env(map[string]string{"INDEX_BASE": "/b"})); p != filepath.Join("/b", "extractors.json") || req {
		t.Errorf("default: %s %v", p, req)
	}
}

func TestMainExtractorsDefault(t *testing.T) {
	base, kb, cache := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("INDEX_BASE", base)
	t.Setenv("STUDIO_EXTRACTORS", "")
	var out, errb bytes.Buffer
	// No default file: the pass runs.
	if rc := Main([]string{"--kb-root", kb, "--extract-cache", cache, "--no-ocr"}, &out, &errb); rc != 0 {
		t.Fatalf("rc %d: %s", rc, errb.String())
	}
	// A broken default file is reported and the pass still runs.
	os.WriteFile(filepath.Join(base, "extractors.json"), []byte("{"), 0o644)
	errb.Reset()
	if rc := Main([]string{"--kb-root", kb, "--extract-cache", cache, "--no-ocr"}, &out, &errb); rc != 0 || !strings.Contains(errb.String(), "not used") {
		t.Fatalf("rc %d: %s", rc, errb.String())
	}
	// A broken file named on purpose stops it.
	t.Setenv("STUDIO_EXTRACTORS", filepath.Join(base, "extractors.json"))
	if rc := Main([]string{"--kb-root", kb, "--extract-cache", cache, "--no-ocr"}, &out, &errb); rc != 2 {
		t.Fatalf("named broken file: rc %d", rc)
	}
	t.Setenv("STUDIO_EXTRACTORS", filepath.Join(base, "missing.json"))
	if rc := Main([]string{"--kb-root", kb, "--extract-cache", cache, "--no-ocr"}, &out, &errb); rc != 2 {
		t.Fatalf("named missing file: rc %d", rc)
	}
}
