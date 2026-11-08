package workflows

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// Workflows defined in GitHub repositories (githubWorkflows.ts). One tree
// request per repository and ref, cached ten minutes, keeps within GitHub's
// limit for anonymous clients; GITHUB_TOKEN or GH_TOKEN is used when set.

var gh platform.GitHub

const treeTTL = 10 * time.Minute

type treeHit struct {
	at    time.Time
	paths map[string]bool
}

type readmeHit struct {
	at                 time.Time
	title, description string
}

var (
	ghMu        sync.Mutex
	treeCache   = map[string]treeHit{}
	readmeCache = map[string]readmeHit{}
)

// ResetGitHubCaches is resetGithubCaches.
func ResetGitHubCaches() {
	ghMu.Lock()
	treeCache = map[string]treeHit{}
	readmeCache = map[string]readmeHit{}
	ghMu.Unlock()
}

func rawPath(e *Entry, p string) string { return "/" + e.Owner + "/" + e.Repo + "/" + e.RefOf() + "/" + p }

func rawURL(e *Entry, p string) string { return rawBase() + rawPath(e, p) }

func entryFor(e *Entry, p string) string {
	s := "github.com/" + e.Owner + "/" + e.Repo
	if p != "" {
		s += "/" + p
	}
	if e.Ref != nil {
		s += "@" + *e.Ref
	}
	return s
}

// treeOf is every file path in the repository at the entry's ref.
func treeOf(ctx context.Context, e *Entry) (map[string]bool, error) {
	key := e.Owner + "/" + e.Repo + "@" + e.RefOf()
	ghMu.Lock()
	hit, ok := treeCache[key]
	ghMu.Unlock()
	if ok && time.Since(hit.at) < treeTTL {
		return hit.paths, nil
	}
	res, err := gh.API(ctx, "/repos/"+e.Owner+"/"+e.Repo+"/git/trees/"+platform.EncodeURIComponent(e.RefOf())+"?recursive=1", 20*time.Second)
	if err != nil {
		return nil, platform.KbError(502, "Could not reach GitHub for "+key+".")
	}
	switch {
	case res.Status == 404:
		return nil, platform.KbError(404, "No public repository or ref "+key+" on GitHub.")
	case res.Status == 403 || res.Status == 429:
		return nil, platform.KbError(503, "GitHub is rate limiting this host; try again in a few minutes, or set GITHUB_TOKEN.")
	case !res.OK:
		return nil, platform.KbError(502, "GitHub answered "+strconv.Itoa(res.Status)+" for "+key+".")
	}
	if res.Body == nil {
		return nil, errNotJSON
	}
	paths := map[string]bool{}
	for _, t := range jsonv.Arr(jsonv.Path(res.Body, "tree")) {
		if jsonv.Path(t, "type") == "blob" {
			paths[jsonv.String(jsonv.Path(t, "path"))] = true
		}
	}
	ghMu.Lock()
	treeCache[key] = treeHit{time.Now(), paths}
	ghMu.Unlock()
	return paths, nil
}

type plainError string

func (e plainError) Error() string { return string(e) }

// errNotJSON is res.json() failing on a body that is not JSON (a 500).
var errNotJSON = plainError("Unexpected token in JSON")

func sortedPaths(paths map[string]bool) []string {
	out := make([]string, 0, len(paths))
	for p := range paths {
		out = append(out, p)
	}
	sort.Strings(out) // [...set].sort() compares UTF-16 units; paths are ASCII in practice
	return out
}

// filesIn is the definition files a directory provides: its workflow.yaml,
// or each yamls/<variant>.yaml.
func filesIn(paths map[string]bool, dir string) []string {
	pre := ""
	if dir != "" {
		pre = dir + "/"
	}
	for _, n := range []string{"workflow.yaml", "workflow.yml"} {
		if paths[pre+n] {
			return []string{pre + n}
		}
	}
	out := []string{}
	for _, p := range sortedPaths(paths) {
		if strings.HasPrefix(p, pre+"yamls/") && yamlFile.MatchString(p) && !strings.Contains(p[len(pre+"yamls/"):], "/") {
			out = append(out, p)
		}
	}
	return out
}

// ResolveGitHubEntry is resolveGithubEntry: the runnable entries behind e.
func ResolveGitHubEntry(ctx context.Context, e *Entry) ([]string, error) {
	if yamlFile.MatchString(e.Path) {
		return []string{e.Entry}, nil
	}
	paths, err := treeOf(ctx, e)
	if err != nil {
		return nil, err
	}
	files := filesIn(paths, e.Path)
	if len(files) == 0 {
		return nil, platform.KbError(404, "No workflow.yaml or yamls/*.yaml at "+e.Entry+".")
	}
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = entryFor(e, f)
	}
	return out, nil
}

var (
	h1Re        = regexp.MustCompile(`^#\s+`)
	skipLineRe  = regexp.MustCompile("^(#|!\\[|<|\\||```)")
	mdLinkRe    = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	mdMarkRe    = regexp.MustCompile("[*_`]")
	trailWordRe = regexp.MustCompile(`\s+\S*$`)
)

// sentenceEnd is /^.*[.!?](?=\s)/s on the first 320 units: the longest
// prefix ending in a sentence mark followed by whitespace.
func sentenceEnd(s string) (string, bool) {
	rs := []rune(s)
	for i := len(rs) - 2; i >= 0; i-- {
		if (rs[i] == '.' || rs[i] == '!' || rs[i] == '?') && isJSSpace(rs[i+1]) {
			return string(rs[:i+1]), true
		}
	}
	return "", false
}

func isJSSpace(r rune) bool { return jsTrim(string(r)) == "" }

// readmeOf is a directory's README title and first paragraph.
func readmeOf(ctx context.Context, e *Entry, dir string) (string, string) {
	key := e.Owner + "/" + e.Repo + "@" + e.RefOf() + ":" + dir
	ghMu.Lock()
	hit, ok := readmeCache[key]
	ghMu.Unlock()
	if ok && time.Since(hit.at) < treeTTL {
		return hit.title, hit.description
	}
	title := dir[strings.LastIndex(dir, "/")+1:]
	if title == "" {
		title = e.Repo
	}
	description := ""
	p := "README.md"
	if dir != "" {
		p = dir + "/README.md"
	}
	if res, err := gh.Raw(ctx, rawPath(e, p), 15*time.Second); err == nil && res.OK {
		lines := strings.Split(string(res.Raw), "\n")
		h1 := -1
		for i, l := range lines {
			if h1Re.MatchString(l) {
				h1 = i
				break
			}
		}
		if h1 >= 0 {
			title = jsTrim(h1Re.ReplaceAllString(lines[h1], ""))
		}
		var para []string
		for _, l := range lines[h1+1:] {
			t := jsTrim(l)
			if t == "" {
				if len(para) > 0 {
					break
				}
				continue
			}
			if skipLineRe.MatchString(t) {
				if len(para) > 0 {
					break
				}
				continue
			}
			para = append(para, t)
		}
		text := mdLinkRe.ReplaceAllString(strings.Join(para, " "), "$1")
		text = mdMarkRe.ReplaceAllString(text, "")
		if strings.HasSuffix(text, ":") {
			text = strings.TrimSuffix(text, ":") + "."
		}
		if utf16Len(text) <= 320 {
			description = text
		} else if s, ok := sentenceEnd(sliceJS(text, 320)); ok {
			description = s
		} else {
			description = trailWordRe.ReplaceAllString(sliceJS(text, 320), "") + "…"
		}
	}
	ghMu.Lock()
	readmeCache[key] = readmeHit{time.Now(), title, description}
	ghMu.Unlock()
	return title, description
}

// GitHubWorkflow is one workflow a repository lists.
type GitHubWorkflow struct{ Entry, Title, Description string }

// JSON is {entry, title, description}.
func (g GitHubWorkflow) JSON() *jsonv.Object {
	return jsonv.NewObject("entry", g.Entry, "title", g.Title, "description", g.Description)
}

var (
	variantDir = regexp.MustCompile(`/?(yamls/)?[^/]+$`)
	dirOfDef   = regexp.MustCompile(`^(?:(.*)/)?(?:workflow\.ya?ml|yamls/[^/]+\.ya?ml)$`)
)

// BrowseGitHub is browseGithub: every directory with a workflow.yaml or
// yamls/ variants, one entry per file, titled from its README.
func BrowseGitHub(ctx context.Context, e *Entry) ([]GitHubWorkflow, error) {
	if yamlFile.MatchString(e.Path) {
		title, desc := readmeOf(ctx, e, variantDir.ReplaceAllString(e.Path, ""))
		return []GitHubWorkflow{{e.Entry, title, desc}}, nil
	}
	paths, err := treeOf(ctx, e)
	if err != nil {
		return nil, err
	}
	under := strings.TrimSuffix(e.Path, "/")
	dirs := map[string]bool{}
	for _, p := range sortedPaths(paths) {
		if under != "" && !strings.HasPrefix(p, under+"/") && p != under {
			continue
		}
		if m := dirOfDef.FindStringSubmatch(p); m != nil {
			dirs[m[1]] = true
		}
	}
	list := sortedPaths(dirs)
	if len(list) > 200 {
		list = list[:200]
	}
	out := []GitHubWorkflow{}
	for i := 0; i < len(list); i += 8 {
		batch := list[i:min(i+8, len(list))]
		res := make([][]GitHubWorkflow, len(batch))
		var wg sync.WaitGroup
		for j, dir := range batch {
			wg.Add(1)
			go func() {
				defer wg.Done()
				files := filesIn(paths, dir)
				title, desc := readmeOf(ctx, e, dir)
				for _, f := range files {
					t := title
					if len(files) > 1 {
						t = title + " (" + yamlFile.ReplaceAllString(f[strings.LastIndex(f, "/")+1:], "") + ")"
					}
					res[j] = append(res[j], GitHubWorkflow{entryFor(e, f), t, desc})
				}
			}()
		}
		wg.Wait()
		for _, r := range res {
			out = append(out, r...)
		}
	}
	return out, nil
}

// GitHubDefinition is githubDefinition's answer.
type GitHubDefinition struct {
	Entry              string
	YAML               any
	Title, Description string
	Permissions        []string
}

var (
	entryRepoPrefix = regexp.MustCompile(`^github\.com/[^/]+/[^/@]+/?`)
	atRef           = regexp.MustCompile(`@.*$`)
)

// GitHubDefinitionOf is githubDefinition: the YAML behind e, resolved to
// one file, with its declared permissions.
func GitHubDefinitionOf(ctx context.Context, e *Entry) (*GitHubDefinition, error) {
	files, err := ResolveGitHubEntry(ctx, e)
	if err != nil {
		return nil, err
	}
	if len(files) > 1 {
		return nil, platform.KbError(409, e.Entry+" holds several workflows; name one: "+strings.Join(files, ", "))
	}
	fileEntry := files[0]
	p := atRef.ReplaceAllString(entryRepoPrefix.ReplaceAllString(fileEntry, ""), "")
	res, err := gh.Raw(ctx, rawPath(e, p), 20*time.Second)
	if err != nil || !res.OK {
		if err == nil && res.Status == 404 {
			return nil, platform.KbError(404, "No workflow file at "+fileEntry+". Private repositories still run, but their form cannot be shown here.")
		}
		return nil, platform.KbError(502, "Could not read "+fileEntry+" from GitHub.")
	}
	doc, err := jsonv.ParseYAML(string(res.Raw))
	if err != nil {
		return nil, err
	}
	title, desc := readmeOf(ctx, e, variantDir.ReplaceAllString(p, ""))
	perms := []string{}
	for _, x := range jsonv.Arr(jsonv.Path(doc, "permissions")) {
		perms = append(perms, jsonv.String(x))
	}
	return &GitHubDefinition{fileEntry, doc, title, desc, perms}, nil
}

// GitHubSummary is githubSummary's answer: a tile's title, summary and
// icon (empty when there is none).
type GitHubSummary struct{ Title, Description, Thumbnail string }

var imageExt = regexp.MustCompile(`(?i)\.(png|jpe?g|webp)$`)

// GitHubSummaryOf is githubSummary.
func GitHubSummaryOf(ctx context.Context, e *Entry) GitHubSummary {
	dir := strings.TrimSuffix(e.Path, "/")
	if yamlFile.MatchString(e.Path) {
		dir = variantDir.ReplaceAllString(e.Path, "")
	}
	var title, desc string
	var paths map[string]bool
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); title, desc = readmeOf(ctx, e, dir) }()
	go func() { defer wg.Done(); paths, _ = treeOf(ctx, e) }()
	wg.Wait()
	pre := ""
	if dir != "" {
		pre = dir + "/"
	}
	variant := ""
	if paths != nil && yamlFile.MatchString(e.Path) && len(filesIn(paths, dir)) > 1 {
		variant = " (" + yamlFile.ReplaceAllString(e.Path[strings.LastIndex(e.Path, "/")+1:], "") + ")"
	}
	thumb := ""
	if paths != nil {
		for _, p := range sortedPaths(paths) {
			if strings.HasPrefix(p, pre+"thumbnails/") && imageExt.MatchString(p) {
				thumb = p
				break
			}
		}
		if thumb == "" && paths[pre+"thumbnail.png"] {
			thumb = pre + "thumbnail.png"
		}
	}
	s := GitHubSummary{Title: title + variant, Description: desc}
	if thumb != "" {
		s.Thumbnail = rawURL(e, thumb)
	}
	return s
}
