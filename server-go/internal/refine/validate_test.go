package refine

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

var cases = []string{"", "pump house", `"covid-19" AND masks`, "covid-19", "a:b", `"open`, "(a OR b", "a) b", "AND a", "a OR", "a NOT", "a* OR b", "NEAR(a b, 2)", "a + + b", "^a", "{x y}: z"}

func TestValidateText(t *testing.T) {
	if ValidateText("pump house") != nil || ValidateText("  ") != nil {
		t.Error("a plain search is refused")
	}
	if err := ValidateText(`"open`); err == nil || err.Error() != `The text search cannot be read (a quotation mark is not closed). Put a word with punctuation in double quotes, such as "covid-19", and write AND, OR and NOT in capitals between words.` {
		t.Errorf("%v", err)
	}
}

// With STUDIO_GOLDEN_NODE set, every case answers as refine.ts does.
func TestValidateTextMatchesNode(t *testing.T) {
	node := os.Getenv("STUDIO_GOLDEN_NODE")
	if node == "" {
		t.Skip("STUDIO_GOLDEN_NODE unset")
	}
	in, _ := json.Marshal(cases)
	script := `const { validateText } = await import(process.argv[1] + '/server/dist/refine.js'); const out = []; for (const c of JSON.parse(process.argv[2])) { try { await validateText(c); out.push(null) } catch (e) { out.push(e.message) } } console.log(JSON.stringify(out))`
	b, err := exec.Command(node, "--input-type=module", "-e", script, "../../..", string(in)).Output()
	if err != nil {
		t.Fatal(err)
	}
	var want []*string
	json.Unmarshal(b, &want)
	for i, c := range cases {
		err := ValidateText(c)
		got := ""
		if err != nil {
			got = err.Error()
		}
		w := ""
		if want[i] != nil {
			w = *want[i]
		}
		if got != w {
			t.Errorf("%q\nnode %q\ngo   %q", c, w, got)
		}
	}
}
