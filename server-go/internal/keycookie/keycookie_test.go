package keycookie

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fixture struct {
	Secret string   `json:"secret"`
	Sub    string   `json:"sub"`
	Chunks []string `json:"chunks"`
	Want   struct {
		Key     string          `json:"key"`
		BaseURL string          `json:"baseUrl"`
		Options json.RawMessage `json:"options"`
	} `json:"want"`
}

func load(t *testing.T) (fixture, string) {
	t.Helper()
	var f fixture
	b, _ := os.ReadFile("testdata/node-sealed.json")
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	key, _ := base64.StdEncoding.DecodeString(f.Secret)
	os.WriteFile(filepath.Join(dir, ".credentials-secret"), key, 0o600)
	return f, dir
}

func header(name string, chunks []string) string {
	var parts []string
	for i, c := range chunks {
		parts = append(parts, name+string(rune('0'+i))+"="+c)
	}
	return strings.Join(parts, "; ")
}

// A cookie Node sealed (testdata, made by scripts/golden/keycookie.mjs
// with a test secret) opens here, for its own subject only.
func TestOpensNodeSealedCookie(t *testing.T) {
	f, dir := load(t)
	s := New(dir)
	for _, name := range []string{Cookie, CookieP} {
		k, ok := s.FromCookies(f.Sub, "other=1; "+header(name, f.Chunks))
		if !ok || k.Key != f.Want.Key || k.BaseURL == nil || *k.BaseURL != f.Want.BaseURL {
			t.Fatalf("%s: %+v %v", name, k, ok)
		}
		var got, want any
		json.Unmarshal(k.Options, &got)
		json.Unmarshal(f.Want.Options, &want)
		gb, _ := json.Marshal(got)
		wb, _ := json.Marshal(want)
		if string(gb) != string(wb) {
			t.Fatalf("options %s, want %s", gb, wb)
		}
	}
	if _, ok := s.FromCookies("user-2", header(Cookie, f.Chunks)); ok {
		t.Fatal("opened for another subject")
	}
	if _, ok := New(t.TempDir()).FromCookies(f.Sub, header(Cookie, f.Chunks)); ok {
		t.Fatal("opened under another secret")
	}
}

func TestSealRoundTripAndChunks(t *testing.T) {
	s := New(t.TempDir())
	long := strings.Repeat("k", 6000)
	chunks, err := s.Seal(Key{Sub: "u", Key: long})
	if err != nil || len(chunks) < 2 {
		t.Fatalf("%d chunks %v", len(chunks), err)
	}
	for _, c := range chunks[:len(chunks)-1] {
		if len(c) != chunk {
			t.Fatal("chunk size")
		}
	}
	k, ok := s.FromCookies("u", header(CookieP, chunks))
	if !ok || k.Key != long || k.BaseURL != nil {
		t.Fatal("round trip")
	}
	st, _ := os.Stat(s.file)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("secret mode %v", st.Mode())
	}
}

// With STUDIO_GOLDEN_NODE set, Node opens what Go sealed and Go what Node
// sealed, on one secret.
func TestWithNode(t *testing.T) {
	node := os.Getenv("STUDIO_GOLDEN_NODE")
	if node == "" {
		t.Skip("STUDIO_GOLDEN_NODE unset")
	}
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command(node, append([]string{"../../scripts/golden/keycookie.mjs", "../../.."}, args...)...)
		cmd.Env = append(os.Environ(), "INDEX_BASE="+dir, "KB_ROOT="+filepath.Join(dir, "kb"))
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	var chunks []string
	json.Unmarshal([]byte(run("seal", "u1", "sk-node", "-", "-")), &chunks)
	s := New(dir)
	if k, ok := s.FromCookies("u1", header(Cookie, chunks)); !ok || k.Key != "sk-node" {
		t.Fatal("Go did not open Node's cookie")
	}
	base := "https://m.example.org/v1"
	mine, _ := s.Seal(Key{Sub: "u2", Key: "sk-go", BaseURL: &base})
	var cred struct {
		Key     string `json:"key"`
		BaseURL string `json:"baseUrl"`
	}
	json.Unmarshal([]byte(run("open", "u2", header(Cookie, mine))), &cred)
	if cred.Key != "sk-go" || cred.BaseURL != base {
		t.Fatalf("Node did not open Go's cookie: %+v", cred)
	}
}
