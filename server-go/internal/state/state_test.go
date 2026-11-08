package state

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"testing"
	"time"
)

// scenario runs, on Go's side, what scripts/golden/state.mjs runs on
// stateFormat.ts, and returns the same normalized record.
func scenario(t *testing.T) map[string]any {
	t.Helper()
	var fx struct {
		Files map[string]string `json:"files"`
	}
	b, _ := os.ReadFile("testdata/fixture.json")
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	ix := filepath.Join(base, "ix")
	conf := filepath.Join(base, "data", "studio.env")
	for rel, body := range fx.Files {
		file := filepath.Join(ix, rel)
		if rel == "@config" {
			file = conf
		}
		os.MkdirAll(filepath.Dir(file), 0o755)
		os.WriteFile(file, []byte(body), 0o644)
	}
	env := map[string]string{"STUDIO_CONFIG_FILE": conf}
	// A clock that moves on between calls: two snapshots in one millisecond
	// would sort in either order (Node's too).
	clock := time.Now()
	d := Dir{Base: ix, Getenv: func(k string) string { return env[k] }, Now: func() time.Time { clock = clock.Add(5 * time.Millisecond); return clock }}
	out := map[string]any{}
	out["files"] = d.Files()
	out["formatBefore"] = d.StateFormat()
	r, err := d.Reconcile("v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	out["first"] = string(r)
	var m Marker
	mb, _ := os.ReadFile(filepath.Join(ix, MarkerName))
	json.Unmarshal(mb, &m)
	out["marker"] = map[string]any{"format": m.Format, "writtenBy": m.WrittenBy}
	r, _ = d.Reconcile("v1", nil)
	out["again"] = string(r)
	to := "v2"
	snap, err := d.TakeSnapshot(SnapshotOpts{From: "v1", To: &to, Reason: "update"})
	if err != nil {
		t.Fatal(err)
	}
	out["snapshot"] = map[string]any{"from": snap.From, "to": *snap.To, "reason": snap.Reason, "format": snap.Format, "files": snap.Files,
		"idShape": regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d-\d\d-\d\d-\d{3}Z-\d+-[0-9a-z]{4}$`).MatchString(snap.ID)}
	var listed []string
	root := filepath.Join(d.SnapshotsDir(), snap.ID, "files")
	filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if p != root {
			rel, _ := filepath.Rel(root, p)
			listed = append(listed, rel)
		}
		return nil
	})
	sort.Strings(listed)
	out["snapshotFiles"] = listed
	d.WriteMarker(2, "v2")
	os.WriteFile(filepath.Join(ix, "settings.json"), []byte(`{"appName":"New","updates":{"check":false,"channel":"preview"},"extra":[1,{"a":"b"}]}`), 0o644)
	os.WriteFile(filepath.Join(ix, "users", "u1", "new.json"), []byte("{}"), 0o644)
	os.Remove(filepath.Join(ix, "tasks", "t.json"))
	out["formatNewer"] = d.StateFormat()
	var logs []string
	r, err = d.Reconcile("v1", func(s string) { logs = append(logs, regexp.MustCompile(`taken \S+`).ReplaceAllString(s, "taken <at>")) })
	if err != nil {
		t.Fatal(err)
	}
	out["downgrade"] = string(r)
	out["log"] = logs
	out["after"] = d.Files()
	sb, _ := os.ReadFile(filepath.Join(ix, "settings.json"))
	out["settings"] = string(sb)
	cb, _ := os.ReadFile(conf)
	out["config"] = string(cb)
	mb, _ = os.ReadFile(filepath.Join(ix, MarkerName))
	json.Unmarshal(mb, &m)
	out["markerAfter"] = m.Format
	var snaps []any
	for _, s := range d.ListSnapshots() {
		var to any
		if s.To != nil {
			to = *s.To
		}
		snaps = append(snaps, map[string]any{"from": s.From, "to": to, "reason": regexp.MustCompile(`of \S+$`).ReplaceAllString(s.Reason, "of <at>"), "format": s.Format, "files": len(s.Files)})
	}
	out["snapshots"] = snaps
	os.RemoveAll(d.SnapshotsDir())
	d.WriteMarker(3, "v3")
	r, _ = d.Reconcile("v1", nil)
	out["unreadable"] = string(r)
	return out
}

func normalize(t *testing.T, v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	json.Unmarshal(b, &out)
	return out
}

func TestMatchesStateFormatTs(t *testing.T) {
	want, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var w any
	json.Unmarshal(want, &w)
	got := normalize(t, scenario(t))
	if !reflect.DeepEqual(got, w) {
		g, _ := json.MarshalIndent(got, "", " ")
		t.Fatalf("Go differs from stateFormat.ts:\n%s", g)
	}
}

// The marker and the snapshot record are written as JSON.stringify(x, null, 1) + "\n".
func TestMarkerLayout(t *testing.T) {
	d := Dir{Base: t.TempDir(), Getenv: func(string) string { return "" }}
	d.WriteMarker(1, "v9")
	b, _ := os.ReadFile(filepath.Join(d.Base, MarkerName))
	if !regexp.MustCompile(`^\{\n "format": 1,\n "writtenBy": "v9",\n "at": "\d{4}-\d\d-\d\dT[\d:.]+Z"\n\}\n$`).Match(b) {
		t.Fatalf("marker layout: %q", b)
	}
	if d.StateFormat() != 1 {
		t.Fatal("format")
	}
	os.WriteFile(filepath.Join(d.Base, MarkerName), []byte(`{"format":"2"}`), 0o644)
	if d.StateFormat() != 1 {
		t.Fatal("a format that is not a number counts as 1")
	}
}

func TestGoldenIsCurrent(t *testing.T) {
	node := os.Getenv("STUDIO_GOLDEN_NODE")
	if node == "" {
		t.Skip("STUDIO_GOLDEN_NODE unset")
	}
	out, err := exec.Command(node, "../../scripts/golden/state.mjs", "testdata/fixture.json", "../../..").Output()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile("testdata/golden.json")
	if !bytes.Equal(bytes.TrimSpace(out), bytes.TrimSpace(want)) {
		t.Fatal("stateFormat.ts's output changed: regenerate testdata/golden.json")
	}
}
