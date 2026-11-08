package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestVerify(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a"), []byte("abc"), 0o644)
	const abc = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	var errb bytes.Buffer
	if code := run([]string{"verify", dir, "a=" + abc}, &errb, &errb); code != 0 {
		t.Fatalf("intact: %d %s", code, errb.String())
	}
	for _, c := range []struct {
		arg, says string
		code      int
	}{
		{"a=" + strings.Repeat("0", 64), "has SHA-256", 1},
		{"b=" + abc, "is missing", 1},
		{"../a=" + abc, "is not NAME=SHA256", 2},
	} {
		errb.Reset()
		if code := run([]string{"verify", dir, c.arg}, &errb, &errb); code != c.code || !strings.Contains(errb.String(), c.says) {
			t.Errorf("%s: %d %q", c.arg, code, errb.String())
		}
	}
}

// The helper as a build there runs it, built for this system: the text pass
// over a small tree (index folders with an empty db.db each, as
// gufi_dir2index leaves them) and the check of a kit. The macOS job of
// ci.yml runs it, for the darwin-arm64 helper.
func TestHelperRunsTheTextPass(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "studio-remote")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", exe, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	src, idx := filepath.Join(dir, "src"), filepath.Join(dir, "idx", "src")
	files := map[string]string{
		"notes.md":        "# Hangar\nThe mooring mast is checked at dawn.\n",
		"crew/roster.txt": "Crew roster for the night watch.\n",
		"crew/empty.txt":  "",
	}
	for name, body := range files {
		p := filepath.Join(src, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	for _, d := range []string{idx, filepath.Join(idx, "crew")} {
		os.MkdirAll(d, 0o755)
		db, err := sql.Open("sqlite", filepath.Join(d, "db.db"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("CREATE TABLE summary (name TEXT)"); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	report := filepath.Join(dir, "text.json")
	out, err := exec.CommandContext(ctx, exe, "text", "--kb-root", src, "--index", idx, "--extract-cache", filepath.Join(dir, "idx", ".extract"),
		"--report", report, "--no-ocr").CombinedOutput()
	if err != nil {
		t.Fatalf("text: %v\n%s", err, out)
	}
	if !strings.HasPrefix(string(out), "enriched 2 directories: 2 files indexed") {
		t.Errorf("text said %q", out)
	}
	words := 0
	for _, d := range []string{idx, filepath.Join(idx, "crew")} {
		db, _ := sql.Open("sqlite", filepath.Join(d, "db.db"))
		var n int
		if err := db.QueryRow("SELECT count(*) FROM words").Scan(&n); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
		db.Close()
		words += n
	}
	if words != 2 {
		t.Errorf("%d rows in the words tables, want 2", words)
	}
	var r map[string]any
	b, _ := os.ReadFile(report)
	if err := json.Unmarshal(b, &r); err != nil || r["textFiles"] != float64(2) || r["ocr"] != false {
		t.Errorf("report %s (%v)", b, err)
	}
	sum := sha256.Sum256(b)
	if out, err := exec.CommandContext(ctx, exe, "verify", dir, "text.json="+hex.EncodeToString(sum[:])).CombinedOutput(); err != nil {
		t.Errorf("verify: %v %s", err, out)
	}
	if err := exec.CommandContext(ctx, exe, "verify", dir, "text.json="+strings.Repeat("0", 64)).Run(); err == nil {
		t.Error("verify passed a wrong digest")
	}
	if out, _ := exec.CommandContext(ctx, exe, "version").Output(); !strings.HasPrefix(string(out), "studio-remote "+runtime.GOOS+"-"+runtime.GOARCH) {
		t.Errorf("version %q", out)
	}
}
