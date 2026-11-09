package core

import (
	"os"
	"path/filepath"
	"testing"
)

// A bundle's modules are found beside the server: app/bin/studio-server
// reads app/occt, and a binary with occt/ next to it reads that.
func TestOcctBesideTheServer(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "app", "bin")
	os.MkdirAll(bin, 0o755)
	if p := occtBeside(bin); p != "" {
		t.Fatalf("found %s with no module", p)
	}
	up := filepath.Join(root, "app", "occt", "occt-import-js.wasm")
	os.MkdirAll(filepath.Dir(up), 0o755)
	os.WriteFile(up, []byte("wasm"), 0o644)
	if p := occtBeside(bin); p != up {
		t.Fatalf("got %q, want %q", p, up)
	}
	next := filepath.Join(bin, "occt", "occt-import-js.wasm")
	os.MkdirAll(filepath.Dir(next), 0o755)
	os.WriteFile(next, []byte("wasm"), 0o644)
	if p := occtBeside(bin); p != next {
		t.Fatalf("got %q, want %q (next to the binary first)", p, next)
	}
	t.Setenv("STUDIO_OCCT_WASM", "/named/occt-import-js.wasm")
	if p := OcctWasm(root); p != "/named/occt-import-js.wasm" {
		t.Fatalf("STUDIO_OCCT_WASM first: %q", p)
	}
}
