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
	// Without sign-in My library is the main library: any library of one's
	// own given that name, not only a space, is shown by its folder.
	built := UserDef{Label: SpaceLabel, SourceDir: "~/runs/campaign"}
	if got := built.ShownLabel(false); got != "campaign" {
		t.Fatalf("a built library named My library: %q", got)
	}
	if got := built.ShownLabel(true); got != SpaceLabel {
		t.Fatalf("with sign-in it keeps its name: %q", got)
	}
	for in, want := range map[string]string{"~": "Home folder", "/": "Home folder", "~/a/b/": "b", "/data/runs": "runs"} {
		if got := FolderLabel(in); got != want {
			t.Errorf("FolderLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
