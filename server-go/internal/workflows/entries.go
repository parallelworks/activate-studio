// Package workflows is the Workflows tab and the workflow routes of the
// Node server (workflowsTab.ts, workflowReadme.ts, workflowEntries.ts,
// githubWorkflows.ts, workflowSchema.ts, workflowCompose.ts and the
// /api/workflows routes of routes.ts).
package workflows

import (
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// Entry kinds.
const (
	KindAccount     = "account"
	KindMarketplace = "marketplace"
	KindGitHub      = "github"
)

// Entry is workflowEntries.ts's WorkflowEntry: a workflow saved on the
// account (name), a marketplace workflow (marketplace/<slug>), or a
// workflow.yaml in a GitHub repository (github.com/<owner>/<repo>[/path][@ref]).
type Entry struct {
	Kind  string
	Entry string
	// Name is an account workflow's name; Slug a marketplace one's.
	Name, Slug string
	// Owner, Repo, Path and Ref (nil for HEAD) name a GitHub one.
	Owner, Repo, Path string
	Ref               *string
}

var (
	entryName   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,120}$`)
	entryMarket = regexp.MustCompile(`^marketplace/([A-Za-z0-9_.-]{1,120})$`)
	entryGitHub = regexp.MustCompile(`^github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)((?:/[A-Za-z0-9_.-]+)*)(?:@([A-Za-z0-9_./-]{1,120}))?$`)
	httpScheme  = regexp.MustCompile(`^https?://`)
	yamlFile    = regexp.MustCompile(`\.ya?ml$`)
)

// jsTrim is String.prototype.trim.
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
			return true
		}
		return r >= 0x2000 && r <= 0x200A
	})
}

// utf16Len is a string's JavaScript length.
func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// sliceJS is s.slice(0, n) in UTF-16 units.
func sliceJS(s string, n int) string {
	u := utf16.Encode([]rune(s))
	if len(u) <= n {
		return s
	}
	return string(utf16.Decode(u[:n]))
}

// ParseEntry is parseWorkflowEntry: nil when raw is none of the forms.
func ParseEntry(raw string) *Entry {
	s := jsTrim(raw)
	if s == "" || utf16Len(s) > 300 || strings.Contains(s, "..") {
		return nil
	}
	if entryName.MatchString(s) {
		return &Entry{Kind: KindAccount, Entry: s, Name: s}
	}
	if m := entryMarket.FindStringSubmatch(s); m != nil {
		return &Entry{Kind: KindMarketplace, Entry: s, Slug: m[1]}
	}
	bare := httpScheme.ReplaceAllString(s, "")
	m := entryGitHub.FindStringSubmatchIndex(bare)
	if m == nil {
		return nil
	}
	sub := func(i int) (string, bool) {
		if m[2*i] < 0 {
			return "", false
		}
		return bare[m[2*i]:m[2*i+1]], true
	}
	owner, _ := sub(1)
	repo, _ := sub(2)
	p, _ := sub(3)
	e := &Entry{Kind: KindGitHub, Entry: bare, Owner: owner, Repo: repo, Path: strings.TrimPrefix(p, "/")}
	if ref, ok := sub(4); ok {
		e.Ref = &ref
	}
	return e
}

// RefOf is e.ref ?? 'HEAD'.
func (e *Entry) RefOf() string {
	if e.Ref == nil {
		return "HEAD"
	}
	return *e.Ref
}

// JSON is the entry as the Node object serializes ({kind, entry, ...}).
func (e *Entry) JSON() *jsonv.Object {
	switch e.Kind {
	case KindAccount:
		return jsonv.NewObject("kind", e.Kind, "entry", e.Entry, "name", e.Name)
	case KindMarketplace:
		return jsonv.NewObject("kind", e.Kind, "entry", e.Entry, "slug", e.Slug)
	}
	var ref any
	if e.Ref != nil {
		ref = *e.Ref
	}
	return jsonv.NewObject("kind", e.Kind, "entry", e.Entry, "owner", e.Owner, "repo", e.Repo, "path", e.Path, "ref", ref)
}

func rawBase() string { return gh.RawBase() }

// GitHubYAMLURLs is githubYamlUrls: the named file, or workflow.yaml then
// workflow.yml in the named directory.
func GitHubYAMLURLs(e *Entry) []string {
	base := rawBase() + "/" + e.Owner + "/" + e.Repo + "/" + e.RefOf()
	if yamlFile.MatchString(e.Path) {
		return []string{base + "/" + e.Path}
	}
	dir := base
	if e.Path != "" {
		dir = base + "/" + e.Path
	}
	return []string{dir + "/workflow.yaml", dir + "/workflow.yml"}
}

var lastSegment = regexp.MustCompile(`/?[^/]+$`)

// GitHubThumbnail is githubThumbnail: where the platform's convention puts it.
func GitHubThumbnail(e *Entry) string {
	dir := e.Path
	if yamlFile.MatchString(e.Path) {
		dir = lastSegment.ReplaceAllString(e.Path, "")
	}
	if dir != "" {
		dir += "/"
	}
	return rawBase() + "/" + e.Owner + "/" + e.Repo + "/" + e.RefOf() + "/" + dir + "thumbnail.png"
}

var workflowYAMLTail = regexp.MustCompile(`/?workflow\.ya?ml$`)

// EntryTitle is entryTitle: the repository, or its last path segment.
func EntryTitle(e *Entry) string {
	switch e.Kind {
	case KindAccount:
		return e.Name
	case KindMarketplace:
		return e.Slug
	}
	tail := ""
	for _, s := range strings.Split(workflowYAMLTail.ReplaceAllString(e.Path, ""), "/") {
		if s != "" {
			tail = s
		}
	}
	if tail != "" {
		return tail
	}
	return e.Repo
}
