package pdf

import (
	"fmt"
	"strings"
	"testing"
)

// TestArrayOperandsReusedSafely: array buffers are pooled across
// operators, so every operator must still see exactly its own arrays —
// two arrays in one operator must not share a buffer, and a later TJ
// must not disturb what an earlier one delivered.
func TestArrayOperandsReusedSafely(t *testing.T) {
	content := "[(ab) -20 (cd)] TJ [1 2 3] [4 5] x [(long) (er) (array) (here)] TJ [(z)] TJ"
	var seen []string
	interpretContent([]byte(content), func(op []byte, args []operand) {
		var parts []string
		for _, a := range args {
			if a.kind != opArr {
				continue
			}
			var items []string
			for _, el := range a.arr {
				switch el.kind {
				case opStr:
					items = append(items, string(el.str))
				case opNum:
					items = append(items, fmt.Sprint(el.num))
				}
			}
			parts = append(parts, "["+strings.Join(items, " ")+"]")
		}
		seen = append(seen, string(op)+" "+strings.Join(parts, " "))
	})
	want := []string{
		"TJ [ab -20 cd]",
		"x [1 2 3] [4 5]",
		"TJ [long er array here]",
		"TJ [z]",
	}
	if strings.Join(seen, "; ") != strings.Join(want, "; ") {
		t.Fatalf("operators = %q, want %q", seen, want)
	}
}
