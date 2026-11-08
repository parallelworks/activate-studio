package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
)

// The listing counts /api/whoami as Go's wherever the server answers it:
// through the front with Node off, and through userlibs with the front's
// identity off.
func TestRoutesListWhoami(t *testing.T) {
	for _, env := range []map[string]string{
		{"STUDIO_NODE": "off"},
		{"STUDIO_NODE": "", "STUDIO_NODE_URL": "http://127.0.0.1:9", "STUDIO_FRONT_AUTH": "0"},
	} {
		for k, v := range env {
			t.Setenv(k, v)
		}
		t.Setenv("INDEX_BASE", t.TempDir())
		t.Setenv("KB_ROOT", t.TempDir())
		var out, errb bytes.Buffer
		if code := subcommands["routes"].Run(nil, nil, &out, &errb); code != 0 {
			t.Fatalf("%v: %d %s", env, code, errb.String())
		}
		var got struct{ Ported, Unported []string }
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(got.Ported, "GET /api/whoami") || slices.Contains(got.Unported, "GET /api/whoami") {
			t.Errorf("%v: whoami not counted as Go's", env)
		}
	}
}
