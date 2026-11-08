package jsonv

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

type golden struct {
	JSON []struct {
		Out   string `json:"out"`
		Error bool   `json:"error"`
	} `json:"json"`
	YAML []struct {
		Out   string `json:"out"`
		Error bool   `json:"error"`
	} `json:"yaml"`
}

func load(t *testing.T) (cases struct{ JSON, YAML []string }, g golden) {
	t.Helper()
	b, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return
}

// TestMatchesNode: decoding then encoding gives the bytes JSON.stringify of
// JSON.parse (or of yaml's parse) gives in Node.
func TestMatchesNode(t *testing.T) {
	cases, g := load(t)
	for i, s := range cases.JSON {
		v, err := DecodeString(s)
		if g.JSON[i].Error {
			if err == nil {
				t.Errorf("json %d: want an error", i)
			}
			continue
		}
		if err != nil {
			t.Errorf("json %d: %v", i, err)
			continue
		}
		if got := string(MustMarshal(v)); got != g.JSON[i].Out {
			t.Errorf("json %d:\n got %s\nwant %s", i, got, g.JSON[i].Out)
		}
	}
	for i, s := range cases.YAML {
		v, err := ParseYAML(s)
		if g.YAML[i].Error {
			if err == nil {
				t.Errorf("yaml %d: want an error", i)
			}
			continue
		}
		if err != nil {
			t.Errorf("yaml %d: %v", i, err)
			continue
		}
		if got := string(MustMarshal(v)); got != g.YAML[i].Out {
			t.Errorf("yaml %d:\n got %s\nwant %s", i, got, g.YAML[i].Out)
		}
	}
}

func TestGoldenIsCurrent(t *testing.T) {
	node := os.Getenv("STUDIO_GOLDEN_NODE")
	if node == "" {
		t.Skip("STUDIO_GOLDEN_NODE unset")
	}
	out, err := exec.Command(node, "../../scripts/golden/jsonv.mjs", "testdata/cases.json", "../../..").Output()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile("testdata/golden.json")
	var a, b any
	json.Unmarshal(out, &a)
	json.Unmarshal(want, &b)
	if string(MustMarshal(a)) != string(MustMarshal(b)) {
		t.Fatal("Node's output changed: regenerate testdata/golden.json")
	}
}

func TestObjectOrder(t *testing.T) {
	o := NewObject("b", 1.0, "a", 2.0)
	o.Set("3", 3.0).Set("1", 1.0).Set("b", 5.0)
	if got := string(MustMarshal(o)); got != `{"1":1,"3":3,"b":5,"a":2}` {
		t.Fatal(got)
	}
	o.Delete("3")
	if got := string(MustMarshal(o)); got != `{"1":1,"b":5,"a":2}` {
		t.Fatal(got)
	}
}
