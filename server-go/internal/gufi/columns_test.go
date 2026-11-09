package gufi

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// The cases are shared with server/test/queryColumns.test.mjs, which runs
// them through query.ts's selectColumnNames.
func TestSelectColumnNames(t *testing.T) {
	raw, err := os.ReadFile("testdata/select_columns.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		SQL   string   `json:"sql"`
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if got := SelectColumnNames(c.SQL); !slices.Equal(got, c.Names) || (got == nil) != (c.Names == nil) {
			t.Errorf("%q: got %q, want %q", c.SQL, got, c.Names)
		}
	}
}

func TestRawColumns(t *testing.T) {
	sql := "SELECT name AS file, size AS bytes FROM vrpentries;"
	if got := RawColumns(sql, 2, true); !slices.Equal(got, []string{"file", "bytes"}) {
		t.Errorf("aliases: %q", got)
	}
	if got := RawColumns(sql, 1, false); !slices.Equal(got, []string{"file", "bytes"}) {
		t.Errorf("no rows still names them: %q", got)
	}
	// Rows wider or narrower than the list, or *, fall back to col1..colN.
	if got := RawColumns(sql, 3, true); !slices.Equal(got, []string{"col1", "col2", "col3"}) {
		t.Errorf("width mismatch: %q", got)
	}
	if got := RawColumns("SELECT * FROM vrpentries", 1, false); !slices.Equal(got, []string{"col1"}) {
		t.Errorf("star: %q", got)
	}
}

func TestRawRowsNamedByAlias(t *testing.T) {
	p := &Planner{}
	plan, err := RawPlan("SELECT name AS file, size AS bytes FROM vrpentries", "")
	if err != nil {
		t.Fatal(err)
	}
	res := p.Rows(plan, "a.txt"+Delim+"10\nb.txt"+Delim+"20\n")
	if !slices.Equal(res.Columns, []string{"file", "bytes"}) || len(res.Rows) != 2 {
		t.Errorf("got %q %q", res.Columns, res.Rows)
	}
}
