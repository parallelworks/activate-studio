package helpdoc

import "testing"

func TestFill(t *testing.T) {
	src := "Studio indexes folders. Studio's index stays put.\n" +
		"Install ACTIVATE Studio, then run `studio-server` or `Studio.app`.\n" +
		"```\nStudio in a code block\n```\n" +
		"STUDIO_EXTRACTORS and activate-studio stay. A Studios word stays.\n" +
		"{appName} and {kbLabel}.\n<!-- kbLabel -->\n"
	got := Fill(src, "Acme Search", "Projects")
	want := "Acme Search indexes folders. Acme Search's index stays put.\n" +
		"Install ACTIVATE Studio, then run `studio-server` or `Studio.app`.\n" +
		"```\nStudio in a code block\n```\n" +
		"STUDIO_EXTRACTORS and activate-studio stay. A Studios word stays.\n" +
		"Acme Search and Projects.\nKnowledge base for this deployment: **Projects**\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if plain := Fill("Studio and {appName}.\n", "Studio", "kb"); plain != "Studio and Studio.\n" {
		t.Fatalf("default name: %q", plain)
	}
}
