package conversations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
)

// A stored file with fields this package does not define at every level,
// some of them before the known ones, as Node keeps whatever is stored.
const storedWithExtras = `[{"pinned":true,"id":"c1","title":"Plans","owner":null,"createdAt":"2026-11-01T10:00:00.000Z","updatedAt":"2026-11-01T10:05:00.000Z","activeBranchId":null,` +
	`"messages":[{"id":"m1","role":"user","content":"start it","parentId":null,"timestamp":"2026-11-01T10:00:00.000Z","attachments":[{"name":"a.txt"}]},` +
	`{"id":"m2","role":"assistant","content":"Asking.","parentId":"m1","timestamp":"2026-11-01T10:00:05.000Z","model":"m","rating":{"up":1},` +
	`"parts":[{"kind":"approval","id":"q1","approvalKind":"question","question":"Which system?","questions":[{"q":"a"},{"q":"b"}],"options":["vega","lyra"],"subject":"pick one",` +
	`"instead":[{"tool":"list_clusters","label":"List","icon":"list"}],"expiresAt":"2026-11-02T10:00:00.000Z"},` +
	`{"origin":"held","kind":"tool_call","id":"t1","name":"stop_cluster","args":"{}","status":"held","files":[{"path":"a.md","library":null,"size":3}],` +
	`"decision":{"status":"pending","by":"u1","weight":2},"note":"kept"}],"finishReason":"stop","feedback":"good"}],"folder":"work"}]`

func indented(t *testing.T, compact string) string {
	b, err := jsonobj.IndentJS([]byte(compact), " ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func storeWith(t *testing.T, file string) (*Store, string) {
	ix := t.TempDir()
	path := filepath.Join(ix, "conversations.json")
	if err := os.WriteFile(path, []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	return New(ix, t.TempDir(), Hooks{}), path
}

func TestStoreKeepsUnknownFieldsAcrossARewrite(t *testing.T) {
	orig := indented(t, storedWithExtras)
	s, path := storeWith(t, orig)
	// A settle of a part no message holds rewrites the file unchanged.
	if !s.SettleHeld("c1", "m2", "none", "", "", "", nil, "") {
		t.Fatal("not settled")
	}
	got, _ := os.ReadFile(path)
	if string(got) != orig {
		t.Fatalf("rewrite changed the file\nwant: %s\ngot:  %s", orig, got)
	}
}

func TestStoreKeepsUnknownFieldsWhenPartsChange(t *testing.T) {
	s, path := storeWith(t, indented(t, storedWithExtras))
	if !s.SettleHeld("c1", "m2", "q1", "", "", "", nil, "vega") {
		t.Fatal("approval not settled")
	}
	if !s.SettleHeld("c1", "m2", "t1", "stop_cluster", "ok", "stopped", &Decision{Status: "approved", By: "u1"}, "") {
		t.Fatal("call not settled")
	}
	// Changed in place: a known key stays where it was stored, a new one
	// comes after the stored keys, and every unknown key keeps its value
	// and position. The decision is a new one, so its unknown key is gone,
	// as it is in Node, which assigns a new decision object.
	want := strings.NewReplacer(
		`"expiresAt":"2026-11-02T10:00:00.000Z"}`, `"expiresAt":"2026-11-02T10:00:00.000Z","resolved":true,"answer":"vega"}`,
		`"status":"held"`, `"status":"ok"`,
		`"decision":{"status":"pending","by":"u1","weight":2},"note":"kept"}`, `"decision":{"status":"approved","by":"u1"},"note":"kept","result":"stopped"}`,
	).Replace(storedWithExtras)
	got, _ := os.ReadFile(path)
	if string(got) != indented(t, want) {
		t.Fatalf("file after settling\nwant: %s\ngot:  %s", indented(t, want), got)
	}
}

func TestReadKeepsUnknownFields(t *testing.T) {
	s, _ := storeWith(t, indented(t, storedWithExtras))
	c := s.Get("c1")
	if c == nil {
		t.Fatal("no conversation")
	}
	b, err := jsonobj.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != strings.TrimSuffix(strings.TrimPrefix(storedWithExtras, "["), "]") {
		t.Fatalf("read back\nwant: %s\ngot:  %s", storedWithExtras, b)
	}
	if !c.Messages[1].Parts[0].Extra.Has("options") || !c.Extra.Has("pinned") {
		t.Fatal("extra fields not held")
	}
}
