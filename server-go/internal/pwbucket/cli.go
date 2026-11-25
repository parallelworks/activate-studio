// Package pwbucket reads ACTIVATE buckets (docs/LIBRARIES.md, Buckets) with
// one person's own pw sign-in.
//
// `pw buckets get-token` gives a short-term credential for the bucket's
// store, and the Studio lists and reads the store itself with it: a flat
// listing, a page of up to 1,000 keys a request, and ranged GETs (s3.go,
// gcs.go). Where a store is not one this package reads, or the credential
// cannot be had or is refused, the same calls go through `pw buckets ls`
// and `pw buckets cat` (walk.go).
//
// The person's sign-in is given to each pw run in its environment alone,
// with a HOME of its own, in memory where the system has such a folder,
// removed when the run ends: pw keeps the bucket's credential under HOME,
// and neither it nor the sign-in is to be left on disk. Nothing here logs a
// credential or puts one in an error.
package pwbucket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Scheme starts an ACTIVATE bucket's URI.
const Scheme = "pw://"

// The stores an ACTIVATE bucket can be in, as the platform names them.
const (
	AWS    = "aws"
	Google = "google"
	Azure  = "azure"
	Oracle = "oracle"
)

// ErrNoSignIn is a read for a person with no pw sign-in of their own here.
var ErrNoSignIn = errors.New("sign in with ACTIVATE under Settings, Accounts: your buckets are read with your own sign-in")

// Bucket is one of the person's ACTIVATE buckets.
type Bucket struct {
	// URI is pw://<namespace>/<name>.
	URI       string `json:"uri"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Label     string `json:"label"`
	// CSP is the store it is in (AWS, Google, Azure, Oracle), Region where
	// the store keeps it, and Store the bucket's own name there.
	CSP    string `json:"csp"`
	Region string `json:"region"`
	Store  string `json:"store"`
	// Account is the storage account of an Azure container.
	Account string `json:"account,omitempty"`
	// Ready is whether it is provisioned.
	Ready bool `json:"ready"`
	// Why is why it cannot be read here; "" when it can be tried.
	Why string `json:"why,omitempty"`
}

var partRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@+-]*$`)

// ParseURI is an ACTIVATE bucket's URI, checked: pw://<namespace>/<name>,
// with nothing after it.
func ParseURI(s string) (namespace, name string, err error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), Scheme)
	if ok {
		namespace, name, ok = strings.Cut(strings.TrimSuffix(rest, "/"), "/")
	}
	if !ok || !partRe.MatchString(namespace) || !partRe.MatchString(name) {
		return "", "", fmt.Errorf("a bucket's URI is %s<namespace>/<bucket>", Scheme)
	}
	return namespace, name, nil
}

// Unsupported says why a store's buckets cannot be read here, "" when they
// can be.
func Unsupported(csp string) string {
	if csp == Oracle {
		return "In Oracle Cloud, which the pw CLI gives no token for (unsupported CSP)"
	}
	return ""
}

// CLI runs `pw buckets` as one person.
type CLI struct {
	// Bin is the pw program.
	Bin string
	// Key is the person's own platform credential, fresh each time it is
	// asked; "" when they have none.
	Key func(ctx context.Context) (string, error)
	// Environ is the environment a run gets, with set put over it.
	Environ func(set map[string]string) []string
	// Host is the platform's host, and Context the pw context to name; the
	// credential in the environment is what signs the run in.
	Host, Context string
	// Endpoint is another address to ask S3 at (Store.Base), which pw takes
	// from AWS_ENDPOINT_URL.
	Endpoint string
	// Runs counts the pw processes started.
	Runs atomic.Int64

	mu   sync.Mutex
	home string
	uses int
}

// tmpBase is where a run's HOME is made: a folder kept in memory where
// there is one (Linux's /dev/shm), else the system's own for temporary
// files ("").
var tmpBase = func() string {
	if runtime.GOOS == "linux" {
		if st, err := os.Stat("/dev/shm"); err == nil && st.IsDir() {
			return "/dev/shm"
		}
	}
	return ""
}

// privateHome makes a HOME for pw, open to this account alone.
func privateHome() (string, error) {
	for _, base := range []string{tmpBase(), ""} {
		if dir, err := os.MkdirTemp(base, "studio-pw-"); err == nil {
			return dir, nil
		} else if base == "" {
			return "", err
		}
	}
	return "", errors.New("no folder for temporary files")
}

// hold gives the HOME runs share while any of them is at work, and what to
// call when done with it: the last one out removes it.
func (c *CLI) hold() (string, func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.home == "" {
		h, err := privateHome()
		if err != nil {
			return "", nil, err
		}
		c.home = h
	}
	c.uses++
	h := c.home
	var once sync.Once
	return h, func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.uses--; c.uses == 0 {
				os.RemoveAll(c.home)
				c.home = ""
			}
		})
	}, nil
}

// start is a pw run, not yet started. done must be called after it ends.
func (c *CLI) command(ctx context.Context, home string, args []string) (cmd *exec.Cmd, secret string, err error) {
	key, err := c.Key(ctx)
	if err != nil {
		return nil, "", err
	}
	if key == "" {
		return nil, "", ErrNoSignIn
	}
	if c.Bin == "" {
		return nil, "", errors.New("the pw CLI was not found on the Studio's computer")
	}
	set := map[string]string{
		"HOME": home, "PW_API_KEY": key,
		"PW_CREDENTIALS_DIR": "", "PW_CONTEXT": "", "PW_PLATFORM_HOST": c.Host,
		"XDG_CONFIG_HOME": "", "XDG_CACHE_HOME": "", "XDG_DATA_HOME": "",
		"DBUS_SESSION_BUS_ADDRESS": "",
	}
	if c.Endpoint != "" {
		set["AWS_ENDPOINT_URL"] = c.Endpoint
	}
	var global []string
	if c.Context != "" {
		global = append(global, "--context", c.Context)
	}
	if c.Host != "" {
		global = append(global, "--platform-host", c.Host)
	}
	cmd = exec.CommandContext(ctx, c.Bin, append(append(global, "buckets"), args...)...)
	cmd.Env = c.Environ(set)
	cmd.Dir = home
	cmd.Cancel = func() error {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 5 * time.Second
	c.Runs.Add(1)
	return cmd, key, nil
}

// listMax bounds what one `pw buckets ls` answer may hold: about two
// million objects of one folder.
const listMax = 512 << 20

func (c *CLI) run(ctx context.Context, timeout time.Duration, max int64, args ...string) ([]byte, error) {
	home, release, err := c.hold()
	if err != nil {
		return nil, err
	}
	defer release()
	return c.runIn(ctx, home, timeout, max, args...)
}

func (c *CLI) runIn(ctx context.Context, home string, timeout time.Duration, max int64, args ...string) ([]byte, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd, key, err := c.command(ctx, home, args)
	if err != nil {
		return nil, err
	}
	var errb tail
	cmd.Stderr = &errb
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	n, _ := io.Copy(&out, io.LimitReader(stdout, max+1))
	if n > max {
		cmd.Cancel()
	}
	err = cmd.Wait()
	if n > max {
		return nil, fmt.Errorf("pw %s: it answered more than %d bytes", args[0], max)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &RunError{Command: "pw buckets " + args[0], Message: plain(errb.String(), err, key)}
	}
	return out.Bytes(), nil
}

// RunError is a pw run that failed, with what it said.
type RunError struct{ Command, Message string }

func (e *RunError) Error() string { return e.Message }

// tail keeps the end of what a program writes.
type tail struct{ b []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if n := len(t.b); n > 8192 {
		t.b = append([]byte{}, t.b[n-4096:]...)
	}
	return len(p), nil
}
func (t *tail) String() string { return string(t.b) }

var (
	ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")
	// levelRe is how pw starts a line of its log.
	levelRe = regexp.MustCompile(`^\S+ \[(?:ERROR|WARN|WARNING|FATAL)\] `)
	// secretRe is what would be a credential in a message.
	secretRe = regexp.MustCompile(`(?i)\b(AKIA|ASIA)[A-Z0-9]{8,}\b|ya29\.[A-Za-z0-9._-]+|\bpwt_[A-Za-z0-9._-]+|\beyJ[A-Za-z0-9._-]{20,}`)
)

// plain is what a pw run said it failed of, in one line, with nothing in it
// that could be a credential.
func plain(stderr string, err error, secrets ...string) string {
	msg := ""
	for _, l := range strings.Split(ansiRe.ReplaceAllString(stderr, ""), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			msg = levelRe.ReplaceAllString(l, "")
		}
	}
	if msg == "" && err != nil {
		msg = err.Error()
	}
	return redact(msg, secrets...)
}

func redact(msg string, secrets ...string) string {
	for _, s := range secrets {
		if len(s) >= 4 {
			msg = strings.ReplaceAll(msg, s, "[removed]")
		}
	}
	msg = secretRe.ReplaceAllString(msg, "[removed]")
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300])
	}
	return msg
}

// Buckets lists the person's buckets, by namespace and name.
func (c *CLI) Buckets(ctx context.Context) ([]Bucket, error) {
	out, err := c.run(ctx, 60*time.Second, 16<<20, "ls", "-o", "json")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		URI               string `json:"uri"`
		Name              string `json:"name"`
		DisplayName       string `json:"displayName"`
		Namespace         string `json:"namespace"`
		User              string `json:"user"`
		CSP               string `json:"csp"`
		Region            string `json:"region"`
		BucketName        string `json:"bucketName"`
		BucketAccountName string `json:"bucketAccountName"`
		Status            string `json:"status"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, errors.New("pw buckets ls: its answer could not be read")
	}
	list := []Bucket{}
	for _, r := range raw {
		ns := r.Namespace
		if ns == "" {
			ns = r.User
		}
		uri := r.URI
		if uri == "" {
			uri = Scheme + ns + "/" + r.Name
		}
		n, name, err := ParseURI(uri)
		if err != nil {
			continue
		}
		b := Bucket{URI: Scheme + n + "/" + name, Name: name, Namespace: n, Label: r.DisplayName, CSP: strings.ToLower(r.CSP),
			Region: r.Region, Store: r.BucketName, Account: r.BucketAccountName, Ready: r.Status == "" || r.Status == "provisioned"}
		switch {
		case b.CSP == "gcp" || b.CSP == "gcs":
			b.CSP = Google
		case strings.HasPrefix(b.CSP, "aws"):
			b.CSP = AWS // GovCloud's too; its region tells it apart
		}
		if b.Label == "" {
			b.Label = name
		}
		b.Why = Unsupported(b.CSP)
		if b.Why == "" && !b.Ready {
			b.Why = "It is not provisioned yet."
		}
		list = append(list, b)
	}
	return list, nil
}

// NotFound is a bucket or an object that is not there for the person.
type NotFound struct{ What string }

func (e *NotFound) Error() string { return e.What }

// Entry is one entry of a bucket's listing: an object, or with IsDir a
// folder (the common start of objects' keys, or an empty object that marks
// one).
type Entry struct {
	Path    string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// ls is one `pw buckets ls` of a folder of a bucket (its key without the
// last slash, "" for the top): what is directly in it. limit 0 is all of
// it.
func (c *CLI) ls(ctx context.Context, home, uri, dir string, limit int) ([]Entry, error) {
	at := uri + "/"
	if dir != "" {
		at += strings.TrimSuffix(dir, "/") + "/"
	}
	return c.lsAt(ctx, home, uri, at, limit)
}

func (c *CLI) lsAt(ctx context.Context, home, uri, at string, limit int) ([]Entry, error) {
	args := []string{"ls", "-o", "json"}
	if limit > 0 {
		args = append(args, "--limit", fmt.Sprint(limit))
	}
	out, err := c.runIn(ctx, home, 0, listMax, append(args, "--", at)...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		URI       string `json:"uri"`
		Size      int64  `json:"size"`
		Modified  string `json:"modified"`
		Directory bool   `json:"directory"`
	}
	if len(bytes.TrimSpace(out)) > 0 {
		if err := json.Unmarshal(out, &raw); err != nil {
			return nil, errors.New("pw buckets ls: its answer could not be read")
		}
	}
	list := make([]Entry, 0, len(raw))
	for _, r := range raw {
		key, ok := strings.CutPrefix(r.URI, uri+"/")
		if !ok {
			return nil, errors.New("pw buckets ls: it answered a URI outside the bucket")
		}
		mod, _ := time.Parse(time.RFC3339Nano, r.Modified) // a folder has none
		e := Entry{Path: key, Size: r.Size, ModTime: mod, IsDir: r.Directory || strings.HasSuffix(key, "/")}
		e.Path = strings.TrimSuffix(e.Path, "/")
		// A folder's own marker is in its own listing.
		if e.Path == "" || (e.IsDir && uri+"/"+key == at) {
			continue
		}
		list = append(list, e)
	}
	return list, nil
}

// cat streams an object through `pw buckets cat`. Closing it ends pw, so
// reading only the start of an object stops its transfer there.
func (c *CLI) cat(ctx context.Context, uri, key string) (io.ReadCloser, error) {
	home, release, err := c.hold()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	cmd, secret, err := c.command(ctx, home, []string{"cat", "--", uri + "/" + key})
	if err != nil {
		cancel()
		release()
		return nil, err
	}
	p := &catReader{cmd: cmd, cancel: cancel, release: release, secret: secret, ctx: ctx}
	cmd.Stderr = &p.errb
	if p.out, err = cmd.StdoutPipe(); err == nil {
		err = cmd.Start()
	}
	if err != nil {
		cancel()
		release()
		return nil, err
	}
	return p, nil
}

type catReader struct {
	cmd     *exec.Cmd
	out     io.ReadCloser
	errb    tail
	cancel  context.CancelFunc
	release func()
	secret  string
	ctx     context.Context
	ended   bool
	err     error
}

func (p *catReader) Read(b []byte) (int, error) {
	n, err := p.out.Read(b)
	if err == io.EOF {
		if werr := p.wait(); werr != nil {
			return n, werr
		}
	}
	return n, err
}

func (p *catReader) wait() error {
	if p.ended {
		return p.err
	}
	p.ended = true
	if err := p.cmd.Wait(); err != nil {
		if p.ctx.Err() != nil {
			p.err = p.ctx.Err()
		} else if msg := plain(p.errb.String(), err, p.secret); msg == "no such object" {
			p.err = &NotFound{"there is no such object"}
		} else {
			p.err = &RunError{Command: "pw buckets cat", Message: msg}
		}
	}
	p.cancel()
	p.release()
	return p.err
}

func (p *catReader) Close() error {
	if !p.ended {
		p.cancel()
		p.out.Close()
		p.wait()
	}
	return nil
}

// Cred is a short-term credential for a bucket's store, held in memory
// alone.
type Cred struct {
	// KeyID, Secret and Session are for AWS; Bearer is Google's access
	// token.
	KeyID, Secret, Session, Bearer string
	// Region is the bucket's, where the credential says it.
	Region string
}

// secrets are what must not be in a message.
func (c Cred) secrets() []string { return []string{c.KeyID, c.Secret, c.Session, c.Bearer} }

// Token is a new credential for the bucket (`pw buckets get-token`). Each
// call has a HOME of its own, so pw never answers one it kept.
func (c *CLI) Token(ctx context.Context, uri string) (Cred, error) {
	home, err := privateHome()
	if err != nil {
		return Cred{}, err
	}
	defer os.RemoveAll(home)
	out, err := c.runIn(ctx, home, 90*time.Second, 1<<20, "get-token", "--", uri)
	if err != nil {
		return Cred{}, err
	}
	v := exports(string(out))
	cr := Cred{KeyID: v["AWS_ACCESS_KEY_ID"], Secret: v["AWS_SECRET_ACCESS_KEY"], Session: v["AWS_SESSION_TOKEN"], Bearer: v["CLOUDSDK_AUTH_ACCESS_TOKEN"]}
	for _, k := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
		if cr.Region == "" && regionRe.MatchString(v[k]) {
			cr.Region = v[k]
		}
	}
	if (cr.KeyID == "" || cr.Secret == "") && cr.Bearer == "" {
		return Cred{}, errors.New("pw buckets get-token gave no credential Studio reads")
	}
	return cr, nil
}

// exports are the variables of shell export lines.
func exports(s string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(s, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(l), "export ")
		if !ok {
			continue
		}
		if k, v, ok := strings.Cut(rest, "="); ok {
			out[strings.TrimSpace(k)] = unquote(strings.TrimSpace(v))
		}
	}
	return out
}

// unquote is a word as a shell reads it: single and double quotes, and a
// backslash.
func unquote(s string) string {
	var b strings.Builder
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case quote == '\'':
			if ch == '\'' {
				quote = 0
			} else {
				b.WriteByte(ch)
			}
		case ch == '\\' && i+1 < len(s) && (quote == 0 || strings.IndexByte(`"\$`+"`", s[i+1]) >= 0):
			i++
			b.WriteByte(s[i])
		case quote == '"':
			if ch == '"' {
				quote = 0
			} else {
				b.WriteByte(ch)
			}
		case ch == '\'' || ch == '"':
			quote = ch
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}

// Join is a key under a folder of the bucket.
func Join(dir, rel string) string {
	if dir = strings.Trim(dir, "/"); dir == "" {
		return rel
	}
	return path.Join(dir, rel)
}
