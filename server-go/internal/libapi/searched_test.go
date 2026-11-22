package libapi

import (
	"fmt"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// A library stopped at the search time limit is not reported as searched
// (it is in slow); one that answered in part, or with nothing, was searched.
func TestSearchedLeavesOutLibrariesNotSearched(t *testing.T) {
	libs := []libraries.Library{{ID: "kb"}, {ID: "slow"}, {ID: "part"}, {ID: "gone"}}
	om := []libraries.Omission{
		{ID: "slow", TimeLimit: true, Partial: true},
		{ID: "part", Partial: true},
		{ID: "gone"},
	}
	if got := fmt.Sprint(searchedOf(libs, om)); got != "[kb part gone]" {
		t.Fatalf("searched %s", got)
	}
}
