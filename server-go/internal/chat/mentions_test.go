package chat

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestMentionsIn(t *testing.T) {
	cases := []struct {
		text string
		want []mention
	}{
		{"no files here", nil},
		{"compare @[reports/site survey.md] with @[archive:specs/pump.pdf]",
			[]mention{{path: "reports/site survey.md"}, {path: "specs/pump.pdf", lib: "archive"}}},
		// A pasted citation link counts, in order with the @ forms.
		{"see [x](#open=file:a/site%20survey.pdf&lib=kb2) and @[b.md]",
			[]mention{{path: "a/site survey.pdf", lib: "kb2"}, {path: "b.md"}}},
		// Each file once; no climbing out of the library.
		{"@[a.md] @[a.md] @[../etc/passwd] @[x/../../y]", []mention{{path: "a.md"}}},
		// A capital or a space before the colon is a path, not a library id.
		{"@[Notes: draft.md]", []mention{{path: "Notes: draft.md"}}},
	}
	for _, c := range cases {
		if got := mentionsIn(c.text); !reflect.DeepEqual(got, c.want) {
			t.Errorf("mentionsIn(%q) = %#v, want %#v", c.text, got, c.want)
		}
	}
	many := ""
	for i := 0; i < 12; i++ {
		many += "@[f" + string(rune('a'+i)) + ".md] "
	}
	if n := len(mentionsIn(many)); n != maxMentions {
		t.Errorf("kept %d mentions, want %d", n, maxMentions)
	}
}

func TestWithEffort(t *testing.T) {
	cases := []struct {
		kwargs, effort string
		wantKw         string
		wantTop        string
	}{
		{"", "", "", ""},
		{"", "high", "", "high"},
		{`{"reasoning_effort":"low"}`, "max", `{"reasoning_effort":"high"}`, ""},
		{`{"reasoning_effort":"low"}`, "medium", `{"reasoning_effort":"medium"}`, ""},
		{`{"reasoning_effort":"low"}`, "", `{"reasoning_effort":"low"}`, ""},
		{`{"enable_thinking":false}`, "high", `{"enable_thinking":true}`, ""},
		{`{"enable_thinking":true}`, "low", `{"enable_thinking":false}`, ""},
		{`{"other":1}`, "low", `{"other":1}`, "low"},
	}
	for _, c := range cases {
		var kw json.RawMessage
		if c.kwargs != "" {
			kw = json.RawMessage(c.kwargs)
		}
		gotKw, gotTop := withEffort(kw, c.effort)
		if string(gotKw) != c.wantKw || gotTop != c.wantTop {
			t.Errorf("withEffort(%s, %q) = %s, %q; want %s, %q", c.kwargs, c.effort, gotKw, gotTop, c.wantKw, c.wantTop)
		}
	}
}

func TestEffortOfAndRefusals(t *testing.T) {
	for in, want := range map[string]string{"High": "high", " max ": "max", "bogus": "", "": "", "none": ""} {
		v := in
		if got := effortOf(&v); got != want {
			t.Errorf("effortOf(%q) = %q, want %q", in, got, want)
		}
	}
	if effortOf(nil) != "" {
		t.Error("effortOf(nil) should be empty")
	}
	for _, s := range []string{
		"400 Unrecognized request argument supplied: reasoning_effort",
		`400 {"message":"Invalid reasoning effort \"max\": valid values are low, medium, high"}`,
		"reasoning_effort is not supported with this model (unsupported_parameter)",
	} {
		if !rejectedEffort(s) {
			t.Errorf("rejectedEffort(%q) = false", s)
		}
	}
	for _, s := range []string{"429 rate limit exceeded", "400 max_tokens is unsupported", "500 internal error"} {
		if rejectedEffort(s) {
			t.Errorf("rejectedEffort(%q) = true", s)
		}
	}
}
