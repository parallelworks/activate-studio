package chat

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
)

type chipJSON struct {
	Text, Why, Kind, Path, Lib, Title string
	Also                              []string
	Folder                            bool
}

func suggestionsOf(t *testing.T, parts ...conversations.Part) (said []string, open []chipJSON) {
	t.Helper()
	answer := "The report is written."
	c := &conversations.Conversation{Messages: []conversations.Message{
		{ID: "u1", Role: "user"},
		{ID: "a1", Role: "assistant", Content: &answer, Parts: parts},
	}}
	b, err := json.Marshal(SuggestNext(c))
	if err != nil {
		t.Fatal(err)
	}
	var v struct{ Suggestions []chipJSON }
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	for _, s := range v.Suggestions {
		if s.Kind == "open" {
			open = append(open, s)
		} else {
			said = append(said, s.Text)
		}
	}
	return said, open
}

func wrote(name, args, result string) conversations.Part {
	return conversations.Part{Kind: "tool_call", ID: name + args, Name: name, Args: args, Status: "ok", Result: &result}
}

// A document written as Markdown and then made into a PDF in the same
// turn is one chip, which opens the PDF and names the Markdown; nothing
// is offered as a message to send.
func TestAFileWrittenInTwoFormatsIsOneChipThatOpensIt(t *testing.T) {
	said, open := suggestionsOf(t,
		wrote("write_kb_file", `{"path":"reports/q3-summary.md","content":"# Q3"}`, "Wrote reports/q3-summary.md (5 bytes) and re-indexed in 3 ms. show_in_viewer can display it."),
		wrote("write_kb_file", `{"path":"reports/q3-summary.pdf","source":"reports/q3-summary.md"}`, "Wrote reports/q3-summary.pdf (PDF, 2 pages, 900 bytes, from reports/q3-summary.md) Give the user its link: [reports/q3-summary.pdf](#open=file:reports/q3-summary.pdf) It opens in the Library viewer."))
	for _, s := range said {
		if strings.Contains(s, "q3-summary") {
			t.Errorf("a written file offered as a message: %q", s)
		}
	}
	want := []chipJSON{{Text: "q3-summary.pdf", Why: "a file was written", Kind: "open", Path: "reports/q3-summary.pdf",
		Title: "Open reports/q3-summary.pdf in the Library. Also written as q3-summary.md", Also: []string{"reports/q3-summary.md"}}}
	if !reflect.DeepEqual(open, want) {
		t.Errorf("chips %+v\nwant  %+v", open, want)
	}
}

// The formats made beside a Markdown file by one call, and a file written
// and then edited, are one chip each; a Word file opens when there is no
// PDF, and a failed write offers nothing to open.
func TestOneChipPerDocumentWritten(t *testing.T) {
	_, open := suggestionsOf(t,
		wrote("write_kb_file", `{"path":"notes/plan.md","content":"x","also":["docx"]}`, "Wrote notes/plan.md (1 bytes; made from it: notes/plan.docx (Word))"),
		wrote("edit_kb_file", `{"path":"notes/plan.md","edits":[]}`, "Edited notes/plan.md (1 place changed; 1 bytes) and re-indexed in 2 ms. Link: [notes/plan.md](#open=file:notes/plan.md)"),
		conversations.Part{Kind: "tool_call", ID: "bad", Name: "write_kb_file", Args: `{"path":"notes/other.md"}`, Status: "error"})
	if len(open) != 1 || open[0].Path != "notes/plan.docx" || !reflect.DeepEqual(open[0].Also, []string{"notes/plan.md"}) {
		t.Errorf("chips %+v, want one that opens notes/plan.docx", open)
	}
}

// A file the write placed in the person's own library opens there, and
// the title names that library.
func TestAChipOpensTheLibraryTheFileWasWrittenTo(t *testing.T) {
	_, open := suggestionsOf(t,
		wrote("write_kb_file", `{"path":"My library/drafts/a very long report name for the third quarter.md","content":"x","also":"pdf"}`,
			"Wrote My library/drafts/a very long report name for the third quarter.md (1 bytes) Saved to [My library/drafts/a very long report name for the third quarter.md](#open=file:drafts/a%20very%20long%20report%20name%20for%20the%20third%20quarter.md&lib=u-example). Give the user their links: [My library/drafts/a very long report name for the third quarter.md](#open=file:drafts/a%20very%20long%20report%20name%20for%20the%20third%20quarter.md&lib=u-example) [My library/drafts/a very long report name for the third quarter.pdf](#open=file:drafts/a%20very%20long%20report%20name%20for%20the%20third%20quarter.pdf&lib=u-example)"))
	if len(open) != 1 {
		t.Fatalf("chips %+v", open)
	}
	c := open[0]
	if c.Path != "drafts/a very long report name for the third quarter.pdf" || c.Lib != "u-example" ||
		c.Title != "Open drafts/a very long report name for the third quarter.pdf in My library. Also written as a very long report name for the third quarter.md" {
		t.Errorf("chip %+v", c)
	}
}

// Past two documents the last chip opens the folder they were written to,
// and the turn's other suggestions stay.
func TestManyFilesWrittenEndInAFolderChip(t *testing.T) {
	parts := []conversations.Part{}
	for _, n := range []string{"a", "b", "c", "d"} {
		parts = append(parts,
			wrote("write_kb_file", `{"path":"reports/q3/`+n+`.md","content":"x"}`, "Wrote reports/q3/"+n+".md"),
			wrote("write_kb_file", `{"path":"reports/q3/`+n+`.pdf","source":"reports/q3/`+n+`.md"}`, "Wrote reports/q3/"+n+".pdf"))
	}
	parts = append(parts, wrote("write_kb_file", `{"path":"reports/summary.md","content":"x"}`, "Wrote reports/summary.md"))
	parts = append(parts, conversations.Part{Kind: "tool_call", ID: "f", Name: "list_kb_dir", Args: `{"path":"x"}`, Status: "error"})
	said, open := suggestionsOf(t, parts...)
	if len(open) != 2 || open[0].Path != "reports/q3/a.pdf" {
		t.Fatalf("chips %+v", open)
	}
	if f := open[1]; f.Text != "Open the 5 files written" || f.Path != "reports" || !f.Folder || f.Title != "Open reports in the Library" {
		t.Errorf("folder chip %+v", f)
	}
	if len(said) != 1 || !strings.HasPrefix(said[0], "Retry the step that failed") {
		t.Errorf("other suggestions %q", said)
	}
}
