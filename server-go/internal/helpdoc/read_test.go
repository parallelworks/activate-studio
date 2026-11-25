package helpdoc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const readDoc = `Opening line.

### Quick start

Start here, and share a folder.

# Basics

## Library

Files and folders.

### Share a folder

Right-click a folder and choose Share.

- A person
- A group

### Copy a link

Copy link copies a link.

## Assistant

Ask a question.
`

func TestReadSection(t *testing.T) {
	got := Read(readDoc, Request{Section: "share a folder"}, 1000)
	if !strings.Contains(got, "Right-click a folder") || !strings.Contains(got, "- A group") || strings.Contains(got, "Copy link copies") {
		t.Fatalf("section: %q", got)
	}
	// A page has the sections under it, and not the next page.
	got = Read(readDoc, Request{Section: "Library"}, 1000)
	if !strings.Contains(got, "### Copy a link") || strings.Contains(got, "Ask a question") {
		t.Fatalf("page: %q", got)
	}
	got = Read(readDoc, Request{Section: "## Library, Copy a link"}, 1000)
	if !strings.Contains(got, "Copy link copies") {
		t.Fatalf("named with its page: %q", got)
	}
	if got = Read(readDoc, Request{Section: "nothing here"}, 1000); !strings.Contains(got, "No section is named") || !strings.Contains(got, "- Library") || !strings.Contains(got, "  - Share a folder") {
		t.Fatalf("no such section: %q", got)
	}
}

func TestReadFind(t *testing.T) {
	got := Read(readDoc, Request{Find: "how do I share a folder"}, 1000)
	if !strings.HasPrefix(got, "## Quick start") || !strings.Contains(got, "## Library, Share a folder") || !strings.Contains(got, "Right-click a folder and choose Share.") || strings.Contains(got, "Ask a question") {
		t.Fatalf("find: %q", got)
	}
	if got = Read(readDoc, Request{Find: "zebras"}, 1000); !strings.Contains(got, "Nothing in it has those words") {
		t.Fatalf("no words: %q", got)
	}
	// Common words find nothing on their own.
	if got = Read(readDoc, Request{Find: "how do you"}, 1000); !strings.Contains(got, "Nothing in it has those words") {
		t.Fatalf("common words: %q", got)
	}
}

func TestReadWhole(t *testing.T) {
	if got := Read(readDoc, Request{}, 5000); got != strings.TrimSpace(readDoc) {
		t.Fatalf("a short document is whole: %q", got)
	}
	long := readDoc + "\n## More\n\n" + strings.Repeat("One line of text.\n", 300)
	got := Read(long, Request{}, 600)
	if !strings.HasPrefix(got, "Opening line.") || !strings.Contains(got, "- More") || len(got) > 700 {
		t.Fatalf("a long one is its opening and its sections: %d %q", len(got), got)
	}
	a, b := Read(long, Request{Section: "More"}, 1000), Read(long, Request{Section: "More", Part: 2}, 1000)
	if !strings.Contains(a, "[Part 1 of 7; call again with part 2 for the next.]") || !strings.Contains(b, "[Part 2 of 7") || a == b {
		t.Fatalf("parts: %q %q", a[len(a)-80:], b[len(b)-80:])
	}
	if got := Read("<!-- ai -->Ask.<!-- /ai -->", Request{}, 100); got != "Ask." {
		t.Fatalf("marks: %q", got)
	}
}

// Every section of the guide is one answer, so the assistant can read any of
// it in one call.
func TestGuideSectionsFitOneAnswer(t *testing.T) {
	md, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "HELP.md"))
	if err != nil {
		t.Skip("no guide beside the sources")
	}
	for _, s := range split(string(md)) {
		if s.level == 2 {
			got := Read(string(md), Request{Section: s.title}, 24_000)
			if strings.Contains(got, "[Part ") {
				t.Errorf("%s does not fit one answer", s.title)
			}
		}
	}
	for q, want := range map[string]string{"share folder person": "Share…", "copy link signed out": "open the link again", "add bucket": "List and index"} {
		if got := Read(string(md), Request{Find: q}, 24_000); !strings.Contains(got, want) {
			t.Errorf("%q does not find %q", q, want)
		}
	}
}
