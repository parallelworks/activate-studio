package codesessions

import "testing"

func TestTitleAndURL(t *testing.T) {
	if got := Title("", " fix\n the  build ", "first message"); got != "fix the build" {
		t.Errorf("title = %q", got)
	}
	if got := Title("", "", ""); got != "Untitled session" {
		t.Errorf("empty title = %q", got)
	}
	if got := URL("https://example.org/", "6894b3f8", "20261111-195119-7f57c8de"); got != "https://example.org/code/6894b3f8/20261111-195119-7f57c8de" {
		t.Errorf("url = %q", got)
	}
}

func TestView(t *testing.T) {
	archived := "2026-11-11T19:30:51Z"
	v := view(session{MachineID: "m1", ID: "s1", Preview: "hello", Status: "awaiting_approval", ArchivedAt: &archived, Approvals: []approval{{ID: "a"}}}, map[string]string{"m1": "gpu"}, "https://h")
	if v.Machine != "gpu" || !v.Archived || v.Waiting != 1 || v.Title != "hello" || v.URL != "https://h/code/m1/s1" {
		t.Errorf("view = %+v", v)
	}
	if v := view(session{MachineID: "m2", ID: "s"}, nil, "https://h"); v.Machine != "m2" {
		t.Errorf("unnamed machine = %q", v.Machine)
	}
	for _, bad := range []string{"", "../x", "a/b", "-x"} {
		if idRe.MatchString(bad) {
			t.Errorf("id %q accepted", bad)
		}
	}
}
