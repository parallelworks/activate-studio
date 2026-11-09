package ppt

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCorpusAgainstPython reads every OLE2 file under PPT_CORPUS (any
// extension: .xls, .doc and .msg files exercise the container reader as
// well) with Text and with enrich.py's extract_ppt, ENRICH_PY and
// ENRICH_DEPS set, and compares them.
func TestCorpusAgainstPython(t *testing.T) {
	root, enrich := os.Getenv("PPT_CORPUS"), os.Getenv("ENRICH_PY")
	if root == "" || enrich == "" {
		t.Skip("PPT_CORPUS and ENRICH_PY not set")
	}
	var files []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		head := make([]byte, 8)
		n, _ := f.Read(head)
		f.Close()
		if n == 8 && bytes.Equal(head, []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}) {
			files = append(files, p)
		}
		return nil
	})
	list := filepath.Join(t.TempDir(), "files.json")
	b, _ := json.Marshal(files)
	os.WriteFile(list, b, 0o644)
	script := `import importlib.util, sys, json, pathlib
sys.path.append(sys.argv[3])
spec = importlib.util.spec_from_file_location('enrich', sys.argv[1]); m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
out = [m.extract_ppt(pathlib.Path(p)) for p in json.load(open(sys.argv[2]))]
sys.stdout.write(json.dumps(out))`
	cmd := exec.Command("python3", "-I", "-c", script, enrich, list, os.Getenv("ENRICH_DEPS"))
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatal(err)
	}
	texts, same := 0, 0
	for i, p := range files {
		b, _ := os.ReadFile(p)
		got, err := Text(bytes.NewReader(b), int64(len(b)), 500_000)
		if err != nil {
			got = ""
		}
		if want[i] != "" {
			texts++
		}
		if got == want[i] {
			same++
		} else {
			t.Errorf("%s: go %d bytes (%v), python %d", p, len(got), err, len(want[i]))
		}
	}
	t.Logf("%d OLE2 files, %d with text in Python, %d equal", len(files), texts, same)
}
