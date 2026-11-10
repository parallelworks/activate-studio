package libconfig

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

func routeFixture(t *testing.T) (*app.App, *http.ServeMux, string) {
	t.Helper()
	dir := t.TempDir()
	kb, base := filepath.Join(dir, "kb"), filepath.Join(dir, "ix")
	for _, d := range []string{kb, base, filepath.Join(dir, "shared-index")} {
		os.MkdirAll(d, 0o755)
	}
	t.Setenv("HOME", dir)
	env := map[string]string{"KB_ROOT": kb, "INDEX_BASE": base, "GUFI_BIN": filepath.Join(dir, "no-gufi"), "STUDIO_NODE": "off", "SWEEP_INTERVAL_SEC": "0"}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	added := []libraries.Def{{ID: "shared", Label: "Shared", IndexRoot: filepath.Join(dir, "shared-index"),
		ReadThrough: &libraries.ReadThrough{Destination: "admin@files.example", SSHOptions: []string{"-i", "/k"}}}}
	b, _ := json.Marshal(added)
	os.WriteFile(filepath.Join(base, "libraries.json"), b, 0o644)
	users := libraries.Of(a).Users()
	if err := users.Save("single", []libraries.UserDef{{ID: "u-idx", Label: "Index server", Transport: "ssh", Site: "admin@index.example",
		Destination: sp("admin@index.example"), IndexRoot: sp("/gufi/index"), GufiBin: sp("/opt/gufi/bin"), State: "ready"}}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, a)
	return a, mux, dir
}

func call(t *testing.T, mux *http.ServeMux, u string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", u, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var m map[string]any
	json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func TestExportAndCheckRoutes(t *testing.T) {
	_, mux, dir := routeFixture(t)
	code, ans := call(t, mux, "/api/libraries/export", map[string]any{})
	if code != 200 {
		t.Fatalf("export: %d %v", code, ans)
	}
	text, _ := json.Marshal(ans["file"])
	if strings.Contains(string(text), "admin@") || strings.Contains(string(text), `"/k"`) || strings.Contains(string(text), `"id"`) {
		t.Errorf("export: %s", text)
	}
	if ans["name"] != "studio.studio-libraries.json" {
		t.Errorf("name: %v", ans["name"])
	}
	if libs := ans["file"].(map[string]any)["libraries"].([]any); len(libs) != 2 {
		t.Errorf("want the index and the ssh library: %s", text)
	}

	// Checked against the same Studio, both are already here.
	code, chk := call(t, mux, "/api/libraries/import/check", map[string]any{"file": ans["file"]})
	if code != 200 {
		t.Fatalf("check: %d %v", code, chk)
	}
	entries := chk["entries"].([]any)
	for _, e := range entries {
		if s := e.(map[string]any)["state"]; s != "present" {
			t.Errorf("entry: %v", e)
		}
	}
	if chk["localUser"] == "" {
		t.Error("no local user")
	}

	// As text, with an index that is not here yet and an entry of a kind
	// this Studio does not know.
	other := filepath.Join(dir, "other-index")
	code, chk = call(t, mux, "/api/libraries/import/check", map[string]any{"text": string(file(
		`{"kind":"index","indexRoot":"`+other+`","label":"Other"}`, `{"kind":"tape"}`))})
	if code != 200 {
		t.Fatalf("check text: %d %v", code, chk)
	}
	entries = chk["entries"].([]any)
	if entries[0].(map[string]any)["state"] != "new" || entries[1].(map[string]any)["state"] != "invalid" {
		t.Errorf("entries: %v", entries)
	}

	if code, _ := call(t, mux, "/api/libraries/import/check", map[string]any{"text": `{"format":"x"}`}); code != 400 {
		t.Errorf("not a list: %d", code)
	}
	if code, _ := call(t, mux, "/api/libraries/export", map[string]any{"ids": []any{}}); code != 400 {
		t.Errorf("nothing chosen: %d", code)
	}
}
