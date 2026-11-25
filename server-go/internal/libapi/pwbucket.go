package libapi

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/extractfiles"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwbucket"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
	"github.com/parallelworks/activate-studio/server-go/internal/search"
	"github.com/parallelworks/activate-studio/server-go/internal/sessions"
	"github.com/parallelworks/activate-studio/server-go/internal/transfers"
)

// ACTIVATE buckets (docs/LIBRARIES.md, Buckets). A bucket library of one is
// listed, opened and read with its owner's own pw sign-in (package
// pwbucket): on a Studio of one the Studio's, on a Studio where people sign
// in only that person's, never the deployment's and never another's. Such a
// library is in its owner's own folder there (libraries/owned.go).

// bucketStore is where a bucket library's objects are.
type bucketStore interface {
	Walk(ctx context.Context, rel string, fn func(transfers.BucketEntry) error) error
	Stat(ctx context.Context, key string) (transfers.BucketEntry, error)
	Read(ctx context.Context, key string, n int64) ([]byte, error)
	// Endpoint names the store, the same on every computer; Provider whose
	// prices apply.
	Endpoint(ctx context.Context) string
	Provider(ctx context.Context) string
	// textSource is how index-extract reads the objects of the library over
	// bc: its arguments, and what to call when it ended.
	textSource(ctx context.Context, bc *libraries.BucketOf) (args []string, done func(), err error)
}

// rcloneStore is an rclone remote.
type rcloneStore struct{ *transfers.Bucket }

func (b rcloneStore) textSource(_ context.Context, bc *libraries.BucketOf) ([]string, func(), error) {
	return []string{"--rclone", b.Spec(bc.Path), "--rclone-bin", b.Bin()}, func() {}, nil
}

// pwStore is an ACTIVATE bucket.
type pwStore struct{ s *pwbucket.Store }

func (b pwStore) Walk(ctx context.Context, rel string, fn func(transfers.BucketEntry) error) error {
	return b.s.Walk(ctx, rel, func(e pwbucket.Entry) error {
		return fn(transfers.BucketEntry{Path: e.Path, Size: e.Size, ModTime: e.ModTime, IsDir: e.IsDir})
	})
}

func (b pwStore) Stat(ctx context.Context, key string) (transfers.BucketEntry, error) {
	e, err := b.s.Stat(ctx, key)
	return transfers.BucketEntry{Path: e.Path, Size: e.Size, ModTime: e.ModTime}, pwBucketErr(err)
}

func (b pwStore) Read(ctx context.Context, key string, n int64) ([]byte, error) {
	data, err := b.s.Read(ctx, key, n)
	return data, pwBucketErr(err)
}

func (b pwStore) Endpoint(context.Context) string { return b.s.Endpoint() }

func (b pwStore) Provider(context.Context) string {
	switch b.s.B.CSP {
	case pwbucket.AWS:
		return "AWS"
	case pwbucket.Google:
		return "Google Cloud"
	case pwbucket.Azure:
		return "Azure"
	}
	return b.s.B.CSP
}

// textSource gives index-extract the objects over a socket in a folder of
// this account's alone, so the bucket's credential stays in this process,
// which renews it as it ends.
func (b pwStore) textSource(ctx context.Context, bc *libraries.BucketOf) ([]string, func(), error) {
	dir, err := os.MkdirTemp("", "studio-bkt-")
	if err != nil {
		return nil, nil, err
	}
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		os.RemoveAll(dir)
		return nil, nil, err
	}
	srv := &http.Server{ReadHeaderTimeout: 30 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel, err := search.CleanRel(r.URL.Query().Get(extractfiles.SocketParam))
		if err != nil || rel == "" {
			http.Error(w, "name an object", http.StatusBadRequest)
			return
		}
		body, err := b.s.Open(r.Context(), pwbucket.Join(bc.Path, rel))
		if err != nil {
			status := http.StatusBadGateway
			var nf *pwbucket.NotFound
			if errors.As(err, &nf) {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		defer body.Close()
		if _, err := io.Copy(w, body); err != nil {
			// The reader must not take a part of the object for all of it.
			panic(http.ErrAbortHandler)
		}
	})}
	go srv.Serve(ln)
	return []string{"--socket", sock}, func() { srv.Close(); os.RemoveAll(dir) }, nil
}

// pwBucketErr is a bucket's failure with the status a page shows it by.
func pwBucketErr(err error) error {
	var nf *pwbucket.NotFound
	var ae *apierr.Error
	switch {
	case err == nil || errors.As(err, &ae) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, pwbucket.ErrNoSignIn):
		return apierr.New(409, "%s", sentence(err.Error()))
	case errors.As(err, &nf):
		return apierr.New(404, "%s", nf.What)
	}
	return apierr.New(502, "%s", err.Error())
}

func sentence(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:] + "."
}

// ---- whose sign-in ----

// visitFor is how long a person's credential, seen on a request of theirs,
// is used for their own bucket libraries when the Studio keeps no sign-in
// of theirs (a key kept only in their browser).
const visitFor = 15 * time.Minute

// openFor is how long a bucket's short-term credential serves one person's
// openings of its files. A listing or a text pass takes its own.
const openFor = 5 * time.Minute

type visit struct {
	key string
	at  time.Time
}

// listFor is how long the list of a person's buckets serves the steps of
// adding one.
const listFor = 2 * time.Minute

type listed struct {
	buckets []pwbucket.Bucket
	key     string
	at      time.Time
}

type opened struct {
	store *pwbucket.Store
	key   string
	at    time.Time
}

// pwBuckets is what is kept in memory for ACTIVATE buckets: nothing of it is
// ever written.
type pwBuckets struct {
	mu     sync.Mutex
	visits map[string]visit
	opens  map[string]opened
	lists  map[string]listed
	// base is another place to ask than the store's own, in a test.
	base string
}

// seen notes the credential a person's request carried.
func (c *pwBuckets) seen(owner, key string) {
	if owner == "" || key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.visits == nil {
		c.visits = map[string]visit{}
	}
	c.visits[owner] = visit{key, time.Now()}
}

func (c *pwBuckets) visiting(owner string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.visits[owner]
	if !ok || time.Since(v.at) > visitFor {
		delete(c.visits, owner)
		return ""
	}
	return v.key
}

// forget drops what is kept for a person, whose sign-in or key changed.
func (c *pwBuckets) forget(owner string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.visits, owner)
	delete(c.lists, owner)
	for k := range c.opens {
		if strings.HasPrefix(k, owner+"\x00") {
			delete(c.opens, k)
		}
	}
}

// storedKey is the person's own credential as the Studio keeps it, renewed:
// their pw sign-in, else their ACTIVATE key; "" for none. owner "" is the
// one person of a Studio with sign-in off.
func (s *Service) storedKey(owner string) string {
	if owner == "" {
		if s.a.Cfg.AuthEnabled() {
			return ""
		}
		p := platform.Of(s.a)
		p.EnsureFreshAccess(10 * time.Second)
		if k := p.GatewayKey(); k != "" {
			return k
		}
		if f := s.a.Providers.PersonalCred; f != nil && !p.Stopped() {
			if key, base, ok := f(platform.SingleOwner, ""); ok && base == "" {
				return key
			}
		}
		return ""
	}
	if !s.a.Cfg.AuthEnabled() {
		return ""
	}
	return search.OwnerKeys{A: s.a}.KeyOf(owner)
}

// onePersonsKey is whether storedKey would have one for the one person of a
// Studio with sign-in off, without renewing anything.
func (s *Service) onePersonsKey() bool {
	p := platform.Of(s.a)
	if p.HasActivate() {
		return true
	}
	f := s.a.Providers.PersonalCred
	if f == nil || p.Stopped() {
		return false
	}
	key, base, ok := f(platform.SingleOwner, "")
	return ok && base == "" && key != ""
}

// bucketKey is the credential a person's bucket library is read with now.
func (s *Service) bucketKey(owner string) string {
	if k := s.storedKey(owner); k != "" || owner == "" {
		return k
	}
	return s.builds.pwb.visiting(owner)
}

// pwCLI runs pw as owner.
func (s *Service) pwCLI(owner string) *pwbucket.CLI {
	p := platform.Of(s.a)
	host := p.Host()
	c := &pwbucket.CLI{Bin: p.CLI(), Host: host, Context: "studio",
		Key:     func(context.Context) (string, error) { return s.bucketKey(owner), nil },
		Environ: func(set map[string]string) []string { return p.EnvironFor(set, false) }}
	if s.a.Cfg.AuthEnabled() {
		c.Report = func(what string) { s.a.Log.Warn("ACTIVATE buckets: a connection failed", "said", what) }
	}
	return c
}

// pwStoreOf is an ACTIVATE bucket library's bucket, read as its owner. A run
// (fresh) has a credential of its own; openings share one for a while.
func (s *Service) pwStoreOf(owner string, bc *libraries.BucketOf, fresh bool) (pwStore, error) {
	// A library of an ACTIVATE bucket among the Studio's own, from when its
	// sign-in was off, has no person whose sign-in could read it.
	if owner == "" && s.a.Cfg.AuthEnabled() {
		return pwStore{}, apierr.New(403, "%s", ownerlessBucket)
	}
	key := s.bucketKey(owner)
	if key == "" {
		return pwStore{}, pwBucketErr(pwbucket.ErrNoSignIn)
	}
	// An endpoint is asked only while it is one that may be.
	if _, err := pwbucket.CheckEndpoint(bc.Endpoint, bc.CSP, s.a.Cfg.AuthEnabled()); err != nil {
		return pwStore{}, apierr.New(400, "Endpoint: %s", err.Error())
	}
	cli := s.pwCLI(owner)
	cli.Endpoint = bc.Endpoint
	if cli.Bin == "" {
		return pwStore{}, apierr.New(409, "The pw CLI was not found on the Studio's computer, and ACTIVATE buckets are read with it.")
	}
	make := func() *pwbucket.Store {
		base := bc.Endpoint
		if s.builds.pwb.base != "" {
			base = s.builds.pwb.base
		}
		return &pwbucket.Store{CLI: cli, Base: base, B: pwbucket.Bucket{URI: bc.Location, CSP: bc.CSP, Region: bc.Region, Store: bc.Store, Account: bc.Account}}
	}
	if fresh {
		return pwStore{make()}, nil
	}
	s.builds.pwb.mu.Lock()
	defer s.builds.pwb.mu.Unlock()
	at := owner + "\x00" + bc.Location + "\x00" + bc.Endpoint
	for k, o := range s.builds.pwb.opens {
		if time.Since(o.at) > openFor {
			delete(s.builds.pwb.opens, k)
		}
	}
	o, ok := s.builds.pwb.opens[at]
	if !ok || o.key != key {
		o = opened{store: make(), key: key, at: time.Now()}
		if s.builds.pwb.opens == nil {
			s.builds.pwb.opens = map[string]opened{}
		}
		s.builds.pwb.opens[at] = o
	}
	return pwStore{o.store}, nil
}

const ownerlessBucket = "This library of an ACTIVATE bucket was made when this Studio had sign-in off. Add the bucket again as your own, to read it with your own sign-in."

// bucketsOf is the person's buckets; with fresh, asked of the platform now.
func (s *Service) bucketsOf(ctx context.Context, owner string, fresh bool) ([]pwbucket.Bucket, error) {
	key, c := s.bucketKey(owner), &s.builds.pwb
	c.mu.Lock()
	l, ok := c.lists[owner]
	c.mu.Unlock()
	if ok && !fresh && l.key == key && time.Since(l.at) < listFor {
		return l.buckets, nil
	}
	bs, err := s.pwCLI(owner).Buckets(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.lists == nil {
		c.lists = map[string]listed{}
	}
	c.lists[owner] = listed{bs, key, time.Now()}
	c.mu.Unlock()
	return bs, nil
}

// aBucket is one of the person's buckets.
func (s *Service) aBucket(ctx context.Context, owner, uri string) (pwbucket.Bucket, error) {
	bs, err := s.bucketsOf(ctx, owner, false)
	if err != nil {
		return pwbucket.Bucket{}, pwBucketErr(err)
	}
	for _, b := range bs {
		if b.URI == uri {
			if b.Why != "" {
				return b, apierr.New(400, "%s", b.Why)
			}
			return b, nil
		}
	}
	return pwbucket.Bucket{}, apierr.New(404, "%s is not one of your buckets", uri)
}

// pwViewer is the person a request about ACTIVATE buckets is from: their
// libraries' owner ("" with sign-in off), and whether they have a credential
// of their own. With keep, one the request carries serves their bucket
// libraries for a while (visitFor): a listing outlasts its request.
func (s *Service) pwViewer(r *http.Request, keep bool) (owner string, ok bool, err error) {
	v, err := platform.Of(s.a).ViewerOf(r)
	if err != nil {
		return "", false, apierr.New(401, "authentication required")
	}
	if !s.authEnabled(r) {
		return "", s.onePersonsKey(), nil
	}
	owner = v.UserID()
	if owner == "" {
		return "", false, apierr.New(401, "authentication required")
	}
	key, own := platform.Of(s.a).KeyFor(v)
	if !own || key == "" {
		return owner, false, nil
	}
	if keep {
		s.builds.pwb.seen(owner, key)
	}
	return owner, true, nil
}

// pwStart is what is done for ACTIVATE buckets as the server starts: what a
// run that was cut off left is removed, and an address the environment gives
// for S3 that is none is said.
func (s *Service) pwStart(context.Context) {
	if n := pwbucket.Sweep(); n > 0 {
		s.a.Log.Info("ACTIVATE buckets: removed what runs that were cut off left", "folders", n)
	}
	if err := pwbucket.CheckSite(); err != nil {
		s.a.Log.Error("ACTIVATE buckets: " + err.Error())
	}
}

// pwHere is whether this Studio can read ACTIVATE buckets: its gateway is a
// platform's, the pw CLI is here, and GUFI can build an index from a listing.
func (s *Service) pwHere() bool {
	p := platform.Of(s.a)
	return s.a.NodeFree() && p.PlatformGateway() && p.CLISource() != platform.SourceNone && exists(filepath.Join(s.a.Cfg.GufiBin, "gufi_trace2index"))
}

// pwOffered is whether ACTIVATE buckets can be read for a request's person:
// they can here, and the person has a credential of their own.
func (s *Service) pwOffered(r *http.Request) bool {
	if !s.pwHere() {
		return false
	}
	_, ok, err := s.pwViewer(r, false)
	return err == nil && ok
}

// pwShown is whether Add a library speaks of ACTIVATE buckets: where they
// are offered, and to a signed-in person who has only to sign in to ACTIVATE.
func (s *Service) pwShown(r *http.Request) bool {
	if s.pwOffered(r) {
		return true
	}
	owner, _, err := s.pwViewer(r, false)
	return err == nil && owner != "" && s.pwHere()
}

// bucketEndpoints is what a person chooses a bucket's endpoint from, where
// people sign in and the administrator lists any.
func (s *Service) bucketEndpoints(r *http.Request) []string {
	if !s.authEnabled(r) || !s.pwShown(r) {
		return nil
	}
	var out []string
	for _, e := range pwbucket.Endpoints() {
		if at, err := pwbucket.CheckEndpoint(e, "", true); err == nil {
			out = append(out, at)
		}
	}
	return out
}

// otherSite is whether a page of another site sent a request, a GET's too:
// what is read with a person's own sign-in is asked for by the Studio's
// pages only, with sign-in off as with it on. A browser says so itself
// (Sec-Fetch-Site) where it trusts the address; where it does not, over plain
// http, it names the page's origin on anything but a GET.
func otherSite(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return true
	case "same-origin":
		return false
	}
	if r.Header.Get("Origin") != "" {
		return !sessions.SameOrigin(r)
	}
	if u, err := url.Parse(r.Header.Get("Referer")); err == nil && u.Host != "" {
		at := r.Clone(r.Context())
		at.Header.Set("Origin", u.Scheme+"://"+u.Host)
		return !sessions.SameOrigin(at)
	}
	return false
}

// otherSiteRefused answers what a page of another site asked for.
func otherSiteRefused() error {
	return apierr.New(403, "A page of another site cannot use or change your bucket libraries.")
}

// ownBucket is the bucket library of the route's id that the request's
// person may change: with sign-in off any, else their own alone. 404 says
// the same of another person's as of none.
func (s *Service) ownBucket(r *http.Request) (*libraries.Def, error) {
	id := r.PathValue("id")
	if otherSite(r) {
		return nil, otherSiteRefused()
	}
	if s.localBuildOffered(r) {
		if d := s.Reg.AddedDef(id); d != nil && d.Bucket != nil {
			return d, nil
		}
		return nil, apierr.New(404, "%s is not a bucket library", id)
	}
	owner, _, err := s.pwViewer(r, true)
	if err != nil {
		return nil, err
	}
	if d := s.Reg.OwnedDef(owner, id); d != nil {
		return d, nil
	}
	return nil, apierr.New(404, "%s is not a bucket library of yours", id)
}

// ---- routes ----

type pwBucketList struct {
	// Available is whether the person's buckets can be listed; Why why not.
	Available bool              `json:"available"`
	Why       string            `json:"why,omitempty"`
	Buckets   []pwbucket.Bucket `json:"buckets"`
}

// pwBucketsList is the person's ACTIVATE buckets: GET /api/libraries/buckets.
func (s *Service) pwBucketsList(w http.ResponseWriter, r *http.Request) error {
	if otherSite(r) {
		return otherSiteRefused()
	}
	out := pwBucketList{Buckets: []pwbucket.Bucket{}}
	owner, ok, err := s.pwViewer(r, true)
	if err != nil {
		return err
	}
	if !ok || !s.pwOffered(r) {
		if owner != "" && s.pwHere() {
			out.Why = "Sign in with ACTIVATE under Settings, Accounts to choose from your buckets"
		}
		apierr.JSON(w, 200, out)
		return nil
	}
	out.Available = true
	if out.Buckets, err = s.bucketsOf(r.Context(), owner, true); err != nil {
		out.Buckets, out.Why = []pwbucket.Bucket{}, err.Error()
	}
	apierr.JSON(w, 200, out)
	return nil
}

// browseMax is how many entries of a folder are looked at when choosing one.
const browseMax = 1000

type pwBucketFolder struct {
	Path    string   `json:"path"`
	Parent  *string  `json:"parent"`
	Dirs    []string `json:"dirs"`
	Objects int      `json:"objects"`
	// More: there is more in the folder than was looked at.
	More bool `json:"more"`
}

// pwBucketFolders is what is in a folder of one of the person's buckets, for
// choosing one: POST /api/libraries/buckets/folders { uri, path, endpoint }.
// It asks the store with the bucket's credential, at an address the request
// may name, so it is no GET: those any page can have a browser send.
func (s *Service) pwBucketFolders(w http.ResponseWriter, r *http.Request) error {
	if otherSite(r) {
		return otherSiteRefused()
	}
	body, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	owner, ok, err := s.pwViewer(r, true)
	if err != nil {
		return err
	}
	if !ok {
		return pwBucketErr(pwbucket.ErrNoSignIn)
	}
	uri, dir, err := bucketPlace(body.Str("uri"), body.Str("path"))
	if err != nil {
		return err
	}
	endpoint, err := pwbucket.CheckEndpoint(body.Str("endpoint"), "", s.a.Cfg.AuthEnabled())
	if err != nil {
		return apierr.New(400, "Endpoint: %s", err.Error())
	}
	b, err := s.aBucket(r.Context(), owner, uri)
	if err != nil {
		return err
	}
	st, err := s.pwStoreOf(owner, bucketOfPW(b, dir, endpoint, 0), false)
	if err != nil {
		return err
	}
	list, more, err := st.s.Browse(r.Context(), dir, browseMax)
	if err != nil {
		return pwBucketErr(err)
	}
	out := pwBucketFolder{Path: dir, Dirs: []string{}, More: more}
	if dir != "" {
		up := ""
		if i := strings.LastIndexByte(dir, '/'); i >= 0 {
			up = dir[:i]
		}
		out.Parent = &up
	}
	for _, e := range list {
		if e.IsDir {
			out.Dirs = append(out.Dirs, e.Path)
		} else {
			out.Objects++
		}
	}
	apierr.JSON(w, 200, out)
	return nil
}

// bucketPlace is an ACTIVATE bucket's URI and a folder in it, checked.
func bucketPlace(uri, dir string) (string, string, error) {
	ns, name, err := pwbucket.ParseURI(uri)
	if err != nil {
		return "", "", apierr.New(400, "bucket: %s", err.Error())
	}
	p, err := transfers.CleanRel(dir)
	if err != nil {
		return "", "", err
	}
	return pwbucket.Scheme + ns + "/" + name, p, nil
}

// pwBucketDef is a bucket library of an ACTIVATE bucket as a request asks
// for it: the bucket is one of the person's, and the folder can be listed
// as them, before anything is kept.
func (s *Service) pwBucketDef(ctx context.Context, owner, uri, dir, endpoint string, every int) (*libraries.BucketOf, error) {
	uri, dir, err := bucketPlace(uri, dir)
	if err != nil {
		return nil, err
	}
	if endpoint, err = pwbucket.CheckEndpoint(endpoint, "", s.a.Cfg.AuthEnabled()); err != nil {
		return nil, apierr.New(400, "Endpoint: %s", err.Error())
	}
	have := s.Reg.Owned(owner)
	if owner == "" {
		have = s.Reg.AddedDefs()
	}
	for _, d := range have {
		if d.Bucket.PW() && d.Bucket.Location == uri && d.Bucket.Path == dir {
			return nil, apierr.New(409, "That is already a library of yours, %s.", d.Label)
		}
	}
	b, err := s.aBucket(ctx, owner, uri)
	if err != nil {
		return nil, err
	}
	bc := bucketOfPW(b, dir, endpoint, every)
	st, err := s.pwStoreOf(owner, bc, false)
	if err != nil {
		return nil, err
	}
	// A store has no folders: one nothing is in looks as one misspelt does.
	if list, more, err := st.s.Browse(ctx, dir, 2); err != nil {
		return nil, pwBucketErr(err)
	} else if dir != "" && len(list) == 0 && !more {
		return nil, apierr.New(404, "There is nothing in %s/%s.", uri, dir)
	}
	return bc, nil
}

func bucketOfPW(b pwbucket.Bucket, dir, endpoint string, every int) *libraries.BucketOf {
	return &libraries.BucketOf{Location: b.URI, Path: dir, Label: b.Label, Every: every, CSP: b.CSP, Region: b.Region, Store: b.Store, Account: b.Account, Endpoint: endpoint}
}

// ---- when the owner is here ----

// bucketVisit starts the listings of a person's bucket libraries that wait
// for them, where the Studio keeps no sign-in of theirs.
func (s *Service) bucketVisit(r *http.Request) {
	if !s.authEnabled(r) || otherSite(r) {
		return
	}
	owner, ok, err := s.pwViewer(r, false)
	if err != nil || !ok {
		return
	}
	now := time.Now()
	for i, d := range s.Reg.Owned(owner) {
		if i == 0 {
			s.pwViewer(r, true)
		}
		s.builds.startDue(d, now)
	}
}

// openVisit refuses another site's request for a file of an ACTIVATE bucket,
// and keeps the credential the owner's own carries (pwViewer).
func (s *Service) openVisit(r *http.Request, l libraries.Library) error {
	if l.Bucket.PW() && otherSite(r) {
		return otherSiteRefused()
	}
	if l.Owner != "" {
		s.pwViewer(r, true)
	}
	return nil
}

// startDue lists a bucket library that is due, or waited, when its owner's
// sign-in can be had; without it the library waits, and says so.
func (b *builds) startDue(d libraries.Def, now time.Time) {
	if d.Bucket == nil || d.Build == nil || b.jobOf(d.ID) != nil {
		return
	}
	st := b.s.Reg.BucketState(d.ID)
	if !st.Waits && (d.Build.State != "ready" || !bucketDue(d.Bucket, st, now)) {
		return
	}
	if d.Owner != "" && b.s.bucketKey(d.Owner) == "" {
		if !st.Waits {
			st.Waits = true
			b.s.Reg.WriteBucketState(d.ID, st)
		}
		return
	}
	if _, err := b.start(d.ID); err != nil {
		b.libLog(d.ID, "listing it again did not start", err)
	}
}

// libLog logs what happened to a library. The index log is read by everyone
// signed in, so of a person's own library it holds nothing: the server's
// log says what happened, and the library's row, which is theirs, why.
func (b *builds) libLog(id, what string, why error) {
	d := b.s.Reg.BuiltDef(id)
	if d != nil && d.Owner != "" {
		b.s.a.Log.Info("library " + id + ": " + what)
		return
	}
	msg := "library " + id + ": " + what
	if why != nil {
		msg += ": " + why.Error()
	}
	b.log(msg)
}

// jobAllowed is whether a request may see an index job: a job of a person's
// own library is theirs alone.
func (s *Service) jobAllowed(r *http.Request, dir string) bool {
	id, ok := strings.CutPrefix(dir, "library:")
	if !ok {
		return true
	}
	if s.Reg.AddedDef(id) != nil {
		return true
	}
	if d := s.Reg.BuiltDef(id); d == nil {
		// Removed since, or never one: only a shared library's job was
		// anyone's to see.
		return !strings.HasPrefix(id, "pwb-")
	}
	v, err := s.viewer(r)
	if err != nil || v == nil {
		return false
	}
	return s.Reg.OwnedDef(s.Reg.Users().OwnerOf(*v), id) != nil
}
