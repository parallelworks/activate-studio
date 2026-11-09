package libapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// TestRemoteStatsFollowTheLibraries checks that remote-stats.json keeps
// counts only for libraries that exist: entries of libraries removed
// earlier go when the server starts, and a library removed now takes its
// entry with it.
func TestRemoteStatsFollowTheLibraries(t *testing.T) {
	dir := t.TempDir()
	ix := filepath.Join(dir, "ix")
	h := sha256.Sum256([]byte("single"))
	hash := hex.EncodeToString(h[:])[:16]
	userDir := filepath.Join(ix, "users", hash)
	ub, _ := json.Marshal([]map[string]any{
		{"id": "u-keep", "label": "Keep", "site": "me@host", "resource": "x", "sourceDir": "/data/keep", "sourceRoot": "/data/keep", "indexRoot": "/idx/keep", "gufiBin": "", "state": "ready", "builtAt": "2026-11-01T00:00:00Z"},
		{"id": "u-also", "label": "Also", "site": "me@host", "resource": "x", "sourceDir": "/data/also", "sourceRoot": "/data/also", "indexRoot": "/idx/also", "gufiBin": "", "state": "ready", "builtAt": "2026-11-01T00:00:00Z"},
	})
	put(t, filepath.Join(userDir, "libraries.json"), string(ub))
	put(t, filepath.Join(userDir, "owner"), "single")
	keep, also := libraries.RemoteRootOf("single", "u-keep"), libraries.RemoteRootOf("single", "u-also")
	gone := libraries.RemoteRootOf("single", "u-gone")
	stranger := "remote:00000000000000ff:u-x"
	stat := `{"builtAt":"2026-11-01T00:00:00Z","stats":{"available":true,"files":3,"dirs":1,"totalBytes":10,"byExt":[]}}`
	statsFile := filepath.Join(ix, "remote-stats.json")
	put(t, statsFile, `{"`+keep+`":`+stat+`,"`+gone+`":`+stat+`,"`+also+`":`+stat+`,"`+stranger+`":`+stat+`}`)

	env := map[string]string{"KB_ROOT": filepath.Join(dir, "kb"), "INDEX_BASE": ix, "GUFI_BIN": filepath.Join(dir, "no-gufi"), "STUDIO_NODE": "off", "SWEEP_INTERVAL_SEC": "0", "HOST": "127.0.0.1"}
	t.Setenv("HOME", dir)
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	prevResolver, prevKnown := kb.LibraryResolver, kb.RemoteStatsKnown
	t.Cleanup(func() { kb.LibraryResolver, kb.RemoteStatsKnown = prevResolver, prevKnown })
	Register(http.NewServeMux(), a)

	keys := func() []string {
		t.Helper()
		b, err := os.ReadFile(statsFile)
		if err != nil {
			t.Fatal(err)
		}
		// Key order as written, which is the order kept.
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.Token()
		var out []string
		for dec.More() {
			k, _ := dec.Token()
			out = append(out, k.(string))
			var v json.RawMessage
			dec.Decode(&v)
		}
		return out
	}
	if got := keys(); len(got) != 2 || got[0] != keep || got[1] != also {
		t.Fatalf("at start: %v", got)
	}
	b, _ := os.ReadFile(statsFile)
	var m map[string]json.RawMessage
	json.Unmarshal(b, &m)
	if string(m[keep]) != stat {
		t.Errorf("kept entry changed: %s", m[keep])
	}

	// Removed as userlibs removes it: the list saved without it, then told.
	users := libraries.Of(a).Users()
	users.Modify("single", func(defs []libraries.UserDef) ([]libraries.UserDef, error) {
		return defs[1:], nil
	})
	users.TellRemoved(keep)
	if got := keys(); len(got) != 1 || got[0] != also {
		t.Fatalf("after removal: %v", got)
	}
	// One not kept is a no-op.
	users.TellRemoved(gone)
	if got := keys(); len(got) != 1 || got[0] != also {
		t.Fatalf("after a second removal: %v", got)
	}
}
