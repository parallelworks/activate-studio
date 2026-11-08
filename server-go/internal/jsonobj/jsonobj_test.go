package jsonobj

import (
	"encoding/json"
	"testing"
)

func TestKeyOrderKeptAsJavaScriptDoes(t *testing.T) {
	o, err := Parse([]byte(`{"z":1,"a":{"y":[1,2],"b":"<&>"},"m":null,"z":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := o.Keys(); len(got) != 3 || got[0] != "z" || got[1] != "a" || got[2] != "m" {
		t.Fatalf("keys %v", got)
	}
	if string(o.Get("z")) != "3" {
		t.Fatal("a repeated key keeps its first place and its last value")
	}
	o.SetValue("new", []string{})
	o.SetValue("a", map[string]int{"k": 1})
	o.SetValue("m", nil)
	b, _ := o.Indent(" ")
	// JSON.stringify({z:3,a:{k:1},new:[]}, null, 1)
	want := "{\n \"z\": 3,\n \"a\": {\n  \"k\": 1\n },\n \"new\": []\n}"
	if string(b) != want {
		t.Fatalf("got %q", b)
	}
	c, _ := json.Marshal(o)
	if string(c) != `{"z":3,"a":{"k":1},"new":[]}` {
		t.Fatalf("compact %s", c)
	}
	if e, _ := New().Indent(" "); string(e) != "{}" {
		t.Fatal("empty")
	}
	if m, _ := Marshal("<&>"); string(m) != `"<&>"` {
		t.Fatal("no HTML escaping, as JSON.stringify")
	}
	for _, bad := range []string{`[]`, `{"a":1} x`, `{`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s parsed", bad)
		}
	}
}
