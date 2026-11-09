package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	maxUploadBytes = 200 * 1024 * 1024
	maxUploadFiles = 200
	maxURLBytes    = 50 * 1024 * 1024
)

var multiSpace = regexp.MustCompile(` +`)

// SanitizeName is uploads.ts's sanitizeName: the base name with every
// character outside [A-Za-z0-9_.\- ()] made "_" (one per UTF-16 unit, as
// the JavaScript regular expression replaces), runs of spaces made one.
func SanitizeName(name string) (string, error) {
	var b strings.Builder
	for _, r := range jsBasename(name) {
		switch {
		case r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.- ()", r)):
			b.WriteRune(r)
		default:
			b.WriteString(strings.Repeat("_", utf16.RuneLen(r)))
		}
	}
	base := JSTrim(multiSpace.ReplaceAllString(b.String(), " "))
	if base == "" || strings.HasPrefix(base, ".") {
		return "", Error(400, "unusable filename: %s", name)
	}
	return base, nil
}

// SanitizeRelPath sanitizes each segment of an upload's relative path.
func SanitizeRelPath(name string) (string, error) {
	var segs []string
	for _, s := range strings.Split(name, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	if len(segs) > 32 {
		return "", Error(400, "path too deep")
	}
	for i, s := range segs {
		n, err := SanitizeName(s)
		if err != nil {
			return "", err
		}
		segs[i] = n
	}
	return strings.Join(segs, "/"), nil
}

// TargetDir resolves and creates an upload's directory, refusing machinery
// and hidden trees.
func (f *Files) TargetDir(rel string) (abs, cleaned string, err error) {
	cleaned = trimSlashes(rel)
	if abs, err = f.abs(cleaned); err != nil {
		return "", "", err
	}
	for _, part := range strings.Split(cleaned, "/") {
		if ExcludeDirs[part] || strings.HasPrefix(part, ".") {
			return "", "", Error(400, "cannot upload into %s", part)
		}
	}
	return abs, cleaned, os.MkdirAll(abs, 0o777)
}

// errInternal is an error the route answers with a 500 and a reference,
// as Node does for a failure that is not a KbError.
var errInternal = errors.New("internal")

// Upload saves a multipart request's files under dir: names from the
// "paths" field ahead of them where given, else each file's own name.
func (f *Files) Upload(r *http.Request, dir string) (saved []string, cleaned string, err error) {
	abs, cleaned, err := f.TargetDir(dir)
	if err != nil {
		return nil, "", err
	}
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mt, "multipart/") || params["boundary"] == "" {
		return nil, "", fmt.Errorf("%w: the request is not multipart", errInternal)
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	var listed []string
	n := 0
	saved = []string{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return saved, cleaned, err
		}
		if part.FileName() == "" {
			if part.FormName() == "paths" {
				b, _ := io.ReadAll(io.LimitReader(part, 1<<20))
				listed = nil
				var list []any
				if json.Unmarshal(b, &list) == nil {
					for _, x := range list {
						listed = append(listed, jsString(x))
					}
				}
			}
			part.Close()
			continue
		}
		if len(saved) >= maxUploadFiles {
			return saved, cleaned, fmt.Errorf("%w: too many files", errInternal)
		}
		name := part.FileName()
		if n < len(listed) {
			name = listed[n]
		}
		n++
		rel, err := SanitizeRelPath(name)
		if err != nil {
			return saved, cleaned, err
		}
		dest := filepath.Join(abs, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0o777); err != nil {
			return saved, cleaned, err
		}
		if err := saveLimited(dest, part, maxUploadBytes); err != nil {
			return saved, cleaned, err
		}
		if cleaned != "" {
			saved = append(saved, cleaned+"/"+rel)
		} else {
			saved = append(saved, rel)
		}
	}
	return saved, cleaned, nil
}

// saveLimited writes r to dest whole or not at all: a file past max is
// refused, as Node's buffer of the part is, and nothing is left.
func saveLimited(dest string, r io.Reader, max int64) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".upload-*")
	if err != nil {
		return err
	}
	n, err := io.Copy(tmp, io.LimitReader(r, max+1))
	cerr := tmp.Close()
	if err == nil {
		err = cerr
	}
	if err == nil && n > max {
		err = fmt.Errorf("%w: file too large", errInternal)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	os.Chmod(tmp.Name(), 0o666&^umask())
	return os.Rename(tmp.Name(), dest)
}

func jsString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return "null"
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		b, _ := json.Marshal(x)
		return string(b)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

var (
	reScript   = regexp.MustCompile(`(?i)<script[\s\S]*?</script>`)
	reStyle    = regexp.MustCompile(`(?i)<style[\s\S]*?</style>`)
	reBreak    = regexp.MustCompile(`(?i)<(br|/p|/div|/h[1-6]|/li|/tr)[^>]*>`)
	reTag      = regexp.MustCompile(`<[^>]+>`)
	reHSpace   = regexp.MustCompile(`[ \t]+`)
	reTitleTag = regexp.MustCompile(`(?i)<title[^>]*>([^<]*)</title>`)
	reSlug     = regexp.MustCompile(`[^\w.-]`)
	reWebExt   = regexp.MustCompile(`(?i)\.(html?|php|aspx?)$`)
	reHasExt   = regexp.MustCompile(`(?i)\.[a-z0-9]{1,5}$`)
)

func htmlToText(html string) string {
	s := reScript.ReplaceAllString(html, " ")
	s = reStyle.ReplaceAllString(s, " ")
	s = reBreak.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, " ")
	s = strings.NewReplacer("&nbsp;", " ").Replace(s)
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#39;", "'")
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = JSTrim(reHSpace.ReplaceAllString(l, " ")); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

func slugFromURL(u *url.URL) string {
	var last string
	for _, p := range strings.Split(u.EscapedPath(), "/") {
		if p != "" {
			last = p
		}
	}
	host := strings.TrimPrefix(u.Hostname(), "www.")
	base := last
	if base == "" {
		base = host
	}
	base = SliceUTF16(reSlug.ReplaceAllString(base, "-"), 80)
	if base == "" {
		return "page"
	}
	return base
}

// FetchURL is POST /api/kb/upload-url's fetch: http(s) only, redirects
// followed, 30 seconds, at most 50 MB. A PDF is saved as it is, a page
// reduced to text with where it came from, other text with a suffix.
func (f *Files) FetchURL(ctx context.Context, raw, dir string, client *http.Client) (string, string, error) {
	raw = JSTrim(raw)
	if !regexp.MustCompile(`(?i)^https?://`).MatchString(raw) {
		return "", "", Error(400, "url must be http(s)")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", fmt.Errorf("%w: invalid URL", errInternal)
	}
	if u.Path == "" {
		u.Path = "/" // as the URL parser's href has it
	}
	abs, cleaned, err := f.TargetDir(dir)
	if err != nil {
		return "", "", err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, "GET", u.String(), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "activate-studio-kb/1.0")
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return "", "", Error(502, "fetch failed: %d", res.StatusCode)
	}
	buf, err := io.ReadAll(io.LimitReader(res.Body, maxURLBytes+1))
	if err != nil {
		return "", "", err
	}
	if len(buf) > maxURLBytes {
		return "", "", Error(413, "response too large")
	}
	ctype := strings.ToLower(res.Header.Get("Content-Type"))
	name, err := SanitizeName(slugFromURL(u))
	if err != nil {
		return "", "", err
	}
	data := buf
	switch {
	case strings.Contains(ctype, "pdf") || strings.HasSuffix(strings.ToLower(name), ".pdf"):
		if !strings.HasSuffix(strings.ToLower(name), ".pdf") {
			name += ".pdf"
		}
	case strings.Contains(ctype, "html"):
		html := DecodeUTF8(buf)
		title := u.Hostname()
		if m := reTitleTag.FindStringSubmatch(html); m != nil {
			title = JSTrim(m[1])
		}
		data = []byte(fmt.Sprintf("# %s\n\nSource: %s\nFetched: %s\n\n%s", title, u.String(), time.Now().UTC().Format("2006-01-02"), htmlToText(html)))
		name = reWebExt.ReplaceAllString(name, "")
		if !strings.HasSuffix(name, ".md") {
			name += ".md"
		}
	case strings.HasPrefix(ctype, "text/") || strings.Contains(ctype, "json") || strings.Contains(ctype, "xml"):
		if !reHasExt.MatchString(name) {
			name += ".txt"
		}
	}
	if err := os.WriteFile(filepath.Join(abs, name), data, 0o666); err != nil {
		return "", "", err
	}
	saved := name
	if cleaned != "" {
		saved = cleaned + "/" + name
	}
	return saved, cleaned, nil
}
