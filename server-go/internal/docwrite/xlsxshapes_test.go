package docwrite

import "testing"

func TestCloseJSON(t *testing.T) {
	for in, want := range map[string]string{
		`{"sheets":[{"name":"A","rows":[["x",1]]}`: `{"sheets":[{"name":"A","rows":[["x",1]]}]}`,
		`{"a":[1,2`:          `{"a":[1,2]}`,
		`{"a":"b]"`:          `{"a":"b]"}`,
		`{"a":"cut off insi`: "",
		`{"a":[1,`:           "",
		`{"a":[1}`:           "",
		`{"a":1}`:            "",
	} {
		got, ok := CloseJSON(in)
		if want == "" {
			if ok {
				t.Errorf("%s: closed as %s", in, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("%s: got %s %v, want %s", in, got, ok, want)
		}
	}
}

// Rows as models send them: a workbook's JSON lacking its last brackets,
// and a bare row.
func TestSheetsTakesTheShapesModelsSend(t *testing.T) {
	s, err := Sheets(`{"sheets":[{"name":"Sales","rows":[["Date","Region"],["2026-01-21","North"]]}`)
	if err != nil || len(s) != 1 || s[0].Name != "Sales" || len(s[0].Rows) != 2 {
		t.Fatalf("%+v %v", s, err)
	}
	s, err = Sheets(`["2026-01-22","South",12]`)
	if err != nil || len(s) != 1 || len(s[0].Rows) != 1 || len(s[0].Rows[0]) != 3 {
		t.Fatalf("a bare row: %+v %v", s, err)
	}
	if _, err := Sheets(`{"sheets":[{"name":"S","rows":[["a"]]}]] trailing`); err == nil {
		t.Error("a mistake inside the JSON was taken")
	}
}
