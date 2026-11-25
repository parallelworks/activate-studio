package conversations

import "testing"

func TestADefaultTitleIsTheStartOfTheFirstMessage(t *testing.T) {
	long := "Find the **newest** spreadsheet in `data/` and say what changed since last week's run"
	for title, want := range map[string]bool{
		"": true, "Untitled session": true, "pw code in solver": true,
		"Find the newest spreadsheet in data/ and say...": true,
		"Find the newest spreadsheet":                     true,
		"Spreadsheet review":                              false,
		"newest spreadsheet":                              false,
	} {
		if got := IsDefaultTitle(title, long); got != want {
			t.Errorf("%q: %v", title, got)
		}
	}
	if IsDefaultTitle("Budget", "") {
		t.Error("a title with no message is a chosen one")
	}
}

func TestANameIsOneLineAndCut(t *testing.T) {
	if got := oneName("  two\n lines "); got != "two lines" {
		t.Errorf("%q", got)
	}
	long := ""
	for range 200 {
		long += "é"
	}
	if got := []rune(oneName(long)); len(got) != maxName || got[maxName-1] != '…' {
		t.Errorf("%d", len(got))
	}
}
