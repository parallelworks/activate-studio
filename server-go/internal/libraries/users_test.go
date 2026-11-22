package libraries

import "testing"

func TestShownLabelFollowsTheMode(t *testing.T) {
	own := UserDef{Label: SpaceLabel, SourceDir: "~/studio", Space: &SpaceInfo{}}
	if got := own.ShownLabel(true); got != SpaceLabel {
		t.Fatalf("with sign-in: %q", got)
	}
	if got := own.ShownLabel(false); got != "studio" {
		t.Fatalf("without sign-in: %q", got)
	}
	named := UserDef{Label: "Lab notes", SourceDir: "~/studio", Space: &SpaceInfo{}}
	if got := named.ShownLabel(false); got != "Lab notes" {
		t.Fatalf("a name given: %q", got)
	}
	shared := UserDef{Label: SpaceLabel, SourceDir: "/proj/x", Space: &SpaceInfo{}, Shared: &SharedInfo{}}
	if got := shared.ShownLabel(false); got != SpaceLabel {
		t.Fatalf("shared: %q", got)
	}
	for in, want := range map[string]string{"~": "Home folder", "/": "Home folder", "~/a/b/": "b", "/data/runs": "runs"} {
		if got := FolderLabel(in); got != want {
			t.Errorf("FolderLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
