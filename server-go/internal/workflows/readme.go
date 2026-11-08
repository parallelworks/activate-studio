package workflows

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// A workflow's README, shown beside its form (workflowReadme.ts). Every
// kind of entry resolves to a repository, a ref and a file, read through
// the platform's repository route with the viewer's key; GitHub falls
// back to its raw host.

// ReadmeSource is where a README lives: repo is OWNER/REPO on GitHub,
// HOST/GROUP/PROJECT on GitLab.
type ReadmeSource struct {
	Repo, Ref, Path string
	GitHub          bool
}

const readmeMax = 100 * 1024

var (
	dotGit       = regexp.MustCompile(`\.git$`)
	trailSlashes = regexp.MustCompile(`/+$`)
	repoURL      = regexp.MustCompile(`^https?://([^/]+)/(.+)$`)
	repoPath     = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)+$`)
	leadSlashes  = regexp.MustCompile(`^/+`)
	yamlsDir     = regexp.MustCompile(`(^|/)yamls$`)
)

// RepoKey is repoKey: a repository address in the platform's form.
func RepoKey(u string) (key string, github bool, ok bool) {
	s := trailSlashes.ReplaceAllString(dotGit.ReplaceAllString(jsTrim(u), ""), "")
	host := "github.com"
	if m := repoURL.FindStringSubmatch(s); m != nil {
		host, s = strings.ToLower(m[1]), m[2]
	}
	if !repoPath.MatchString(s) {
		return "", false, false
	}
	github = host == "github.com"
	if github && len(strings.Split(s, "/")) != 2 {
		return "", false, false
	}
	if github {
		return s, true, true
	}
	return host + "/" + s, false, true
}

// SourceFromRemote is sourceFromRemote: where a repository-backed record
// keeps its README, from its remote block.
func SourceFromRemote(remote any) *ReadmeSource {
	repo, readme := at(remote, "repo"), at(remote, "readme")
	if !truthy(repo) || !truthy(readme) {
		return nil
	}
	key, github, ok := RepoKey(jsStr(repo))
	p := leadSlashes.ReplaceAllString(jsStr(readme), "")
	if !ok || p == "" || strings.Contains(p, "..") {
		return nil
	}
	ref := "main"
	if b := at(remote, "branch"); truthy(b) {
		ref = jsStr(b)
	}
	return &ReadmeSource{key, ref, p, github}
}

// GitHubReadmeSource is githubReadmeSource: the README in the workflow's directory.
func GitHubReadmeSource(e *Entry) *ReadmeSource {
	dir := e.Path
	if yamlFile.MatchString(dir) {
		parts := strings.Split(dir, "/")
		dir = strings.Join(parts[:len(parts)-1], "/")
	}
	dir = yamlsDir.ReplaceAllString(dir, "")
	p := "README.md"
	if dir != "" {
		p = dir + "/README.md"
	}
	return &ReadmeSource{e.Owner + "/" + e.Repo, e.RefOf(), p, true}
}

// BlobURL is blobUrl: a file's page on the repository's own site.
func BlobURL(src *ReadmeSource, p string) string {
	if src.GitHub {
		return "https://github.com/" + src.Repo + "/blob/" + src.Ref + "/" + p
	}
	return "https://" + src.Repo + "/-/blob/" + src.Ref + "/" + p
}

var (
	imagePath = regexp.MustCompile(`(?i)\.(png|jpe?g|gif|svg|webp|ico|bmp)$`)
	absoluteU = regexp.MustCompile(`(?i)^(?:[a-z][a-z0-9+.-]*:|//|#)`)
	queryFrag = regexp.MustCompile(`[?#].*$`)
)

// ResolveInRepo is resolveInRepo: a relative URL against the README's
// directory, as a path in the repository, or "".
func ResolveInRepo(readmePath, rel string) string {
	bare := queryFrag.ReplaceAllString(rel, "")
	if bare == "" {
		return ""
	}
	var parts []string
	if !strings.HasPrefix(bare, "/") {
		ps := strings.Split(readmePath, "/")
		parts = append(parts, ps[:len(ps)-1]...)
	}
	for _, seg := range strings.Split(leadSlashes.ReplaceAllString(bare, ""), "/") {
		switch seg {
		case "", ".":
		case "..":
			if len(parts) == 0 {
				return ""
			}
			parts = parts[:len(parts)-1]
		default:
			parts = append(parts, seg)
		}
	}
	return strings.Join(parts, "/")
}

var (
	mdLinkURL = regexp.MustCompile(`(!?)(\[[^\]]*\]\()([^)\s]+)(\s*(?:"[^"]*"\s*)?\))`)
	mdRefDef  = regexp.MustCompile(`(?m)^(\[[^\]]+\]:\s+)(\S+)(.*)$`)
	htmlImg   = regexp.MustCompile(`(?i)(<img\b[^>]*?\ssrc\s*=\s*["'])([^"']+)(["'])`)
	htmlA     = regexp.MustCompile(`(?i)(<a\b[^>]*?\shref\s*=\s*["'])([^"']+)(["'])`)
	// codeChunk is the split's capture: fenced blocks and code spans.
	codeChunk = regexp.MustCompile("(?ms)^```.*?^```|^~~~.*?^~~~|`+[^`\\n]+`+")
)

func replaceGroups(re *regexp.Regexp, s string, f func(m []string) string) string {
	return re.ReplaceAllStringFunc(s, func(x string) string { return f(re.FindStringSubmatch(x)) })
}

// RewriteReadme is rewriteReadme: relative links and images made to work
// outside the repository, code left as written.
func RewriteReadme(markdown string, src *ReadmeSource, entry string) string {
	fix := func(u string, image bool) string {
		if absoluteU.MatchString(u) {
			return u
		}
		p := ResolveInRepo(src.Path, u)
		if p == "" {
			return u
		}
		if image || imagePath.MatchString(p) {
			return "/api/workflows/item/asset?w=" + platform.EncodeURIComponent(entry) + "&path=" + platform.EncodeURIComponent(p)
		}
		return BlobURL(src, p)
	}
	prose := func(chunk string) string {
		chunk = replaceGroups(mdLinkURL, chunk, func(m []string) string { return m[1] + m[2] + fix(m[3], m[1] == "!") + m[4] })
		chunk = replaceGroups(mdRefDef, chunk, func(m []string) string { return m[1] + fix(m[2], false) + m[3] })
		chunk = replaceGroups(htmlImg, chunk, func(m []string) string { return m[1] + fix(m[2], true) + m[3] })
		return replaceGroups(htmlA, chunk, func(m []string) string { return m[1] + fix(m[2], false) + m[3] })
	}
	var b strings.Builder
	last := 0
	for _, loc := range codeChunk.FindAllStringIndex(markdown, -1) {
		b.WriteString(prose(markdown[last:loc[0]]))
		b.WriteString(markdown[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(prose(markdown[last:]))
	return b.String()
}

func (r *Routes) platformGet(ctx context.Context, path, key string) *platform.Response {
	return r.p.APIFetch(ctx, path, key, 20*time.Second)
}

// ReadmeSourceOf is readmeSource: where an entry's README lives, or nil
// when it has none; an error when the platform could not say.
func (r *Routes) ReadmeSourceOf(ctx context.Context, e *Entry, key string) (*ReadmeSource, error) {
	switch e.Kind {
	case KindGitHub:
		return GitHubReadmeSource(e), nil
	case KindAccount:
		res := r.platformGet(ctx, "/api/workflows/"+platform.EncodeURIComponent(e.Name), key)
		if res == nil || !res.OK {
			st := "no answer"
			if res != nil {
				st = strconv.Itoa(res.Status)
			}
			return nil, errors.New("workflow record unavailable (" + st + ")")
		}
		return SourceFromRemote(at(res.Body, "remote")), nil
	}
	doc, err := r.cliJSON(ctx, []string{"marketplace", "get", e.Slug, "-o", "json"}, key, 60*time.Second)
	if err != nil {
		return nil, err
	}
	versions := coalesce(at(doc, "versions"), jsonv.NewObject())
	var order []any
	if a, ok := at(doc, "versionOrder").([]any); ok {
		order = a
	} else if o := jsonv.Obj(versions); o != nil {
		for _, k := range o.Keys() {
			order = append(order, k)
		}
	}
	v := at(versions, jsStr(at(doc, "versionTags", "latest")))
	if nullish(v) && len(order) > 0 {
		v = at(versions, jsStr(order[len(order)-1]))
	}
	return SourceFromRemote(v), nil
}

// ReadmeText is readmeText: the README through the platform, GitHub's raw
// host failing that; ok false when neither had it.
func (r *Routes) ReadmeText(ctx context.Context, src *ReadmeSource, key string) (string, bool) {
	q := searchParams("repo", src.Repo, "ref", src.Ref, "path", src.Path)
	if res := r.platformGet(ctx, "/api/repositories/readme?"+q, key); res != nil && res.OK {
		if md, ok := at(res.Body, "markdown").(string); ok {
			return sliceJS(md, readmeMax), true
		}
	}
	if src.GitHub {
		if res, err := gh.Raw(ctx, "/"+src.Repo+"/"+src.Ref+"/"+src.Path, 15*time.Second); err == nil && res.OK {
			return sliceJS(string(res.Raw), readmeMax), true
		}
	}
	return "", false
}

type readmeValue struct {
	at   time.Time
	body *jsonv.Object
}

const readmeTTL = 5 * time.Minute

func (r *Routes) entryAndKey(req *http.Request) (*Entry, string, error) {
	e, err := entryOf(req)
	if err != nil {
		return nil, "", err
	}
	v, err := r.keyOf(req)
	if err != nil {
		return nil, "", err
	}
	if v.key == "" {
		return nil, "", platform.KbError(409, noCredential)
	}
	return e, v.key, nil
}

func (r *Routes) readme(w http.ResponseWriter, req *http.Request) (any, error) {
	ctx := req.Context()
	e, key, err := r.entryAndKey(req)
	if err != nil {
		return nil, err
	}
	ck := keyTail(key) + "|" + e.Entry
	r.mu.Lock()
	hit, ok := r.readmes[ck]
	r.mu.Unlock()
	if ok && time.Since(hit.at) < readmeTTL {
		return hit.body, nil
	}
	src, err := r.ReadmeSourceOf(ctx, e, key)
	if err != nil {
		return jsonv.NewObject("markdown", nil, "web", nil), nil
	}
	var text string
	have := false
	if src != nil {
		text, have = r.ReadmeText(ctx, src, key)
	}
	var md, web any
	if have && src != nil {
		md = RewriteReadme(text, src, e.Entry)
		if text != "" {
			web = BlobURL(src, src.Path)
		}
	}
	value := jsonv.NewObject("markdown", md, "web", web)
	// Only a definite answer is kept: a README, or a workflow with none.
	if src == nil || have {
		r.mu.Lock()
		r.readmes[ck] = readmeValue{time.Now(), value}
		r.mu.Unlock()
	}
	return value, nil
}

func (r *Routes) asset(w http.ResponseWriter, req *http.Request) (any, error) {
	ctx := req.Context()
	e, key, err := r.entryAndKey(req)
	if err != nil {
		return nil, err
	}
	p := leadSlashes.ReplaceAllString(platform.Q(req, "path"), "")
	bad := p == "" || !imagePath.MatchString(p)
	for _, s := range strings.Split(p, "/") {
		if s == ".." {
			bad = true
		}
	}
	if bad {
		return nil, platform.KbError(400, "not an image path in the repository")
	}
	ck := keyTail(key) + "|" + e.Entry + "|" + p
	r.mu.Lock()
	hit, ok := r.assets[ck]
	r.mu.Unlock()
	if ok && time.Since(hit.at) < time.Hour {
		sendImage(w, hit.typ, hit.body)
		return nil, nil
	}
	src, _ := r.ReadmeSourceOf(ctx, e, key)
	if src == nil {
		return nil, platform.KbError(404, "this workflow has no repository")
	}
	q := searchParams("repo", src.Repo, "ref", src.Ref, "path", p)
	res := r.platformGet(ctx, "/api/repositories/thumbnail?"+q, key)
	if (res == nil || !res.OK) && src.GitHub {
		res, _ = gh.Raw(ctx, "/"+src.Repo+"/"+src.Ref+"/"+p, 15*time.Second)
	}
	typ := ""
	if res != nil {
		typ = res.Header.Get("Content-Type")
	}
	if res == nil || !res.OK || !imageType.MatchString(typ) {
		return nil, platform.KbError(404, "image unavailable")
	}
	r.mu.Lock()
	r.assets[ck] = blob{time.Now(), typ, res.Raw}
	r.mu.Unlock()
	sendImage(w, typ, res.Raw)
	return nil, nil
}
