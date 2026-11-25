package pwbucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Store is one ACTIVATE bucket, read as one person.
type Store struct {
	CLI *CLI
	B   Bucket
	// Base is another address to ask than the store's own: a VPC endpoint
	// or a gateway the person named (CheckEndpoint).
	Base string
	// Requests counts the requests made of the store itself. What the pw
	// CLI asks on its own is not in it (CLI.Runs counts those runs).
	Requests atomic.Int64

	mu     sync.Mutex
	cred   *Cred
	client client
	// why is why the store is not read directly, once that is known.
	why error
	// answered is whether the store ever answered.
	answered atomic.Bool
}

// The ways a bucket is read.
const (
	// Direct is the store itself, with the bucket's short-term credential.
	Direct = "direct"
	// ThroughCLI is `pw buckets ls` and `pw buckets cat`.
	ThroughCLI = "pw"
)

// Route is how the bucket was last read, and why not directly.
func (s *Store) Route() (route string, why error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil && s.why == nil {
		return Direct, nil
	}
	return ThroughCLI, s.why
}

// Endpoint names the store, the same on every computer, for the name a
// bucket's text is kept under; "" when it is not known.
func (s *Store) Endpoint() string {
	b := s.B
	switch b.CSP {
	case AWS:
		if b.Region == "" {
			return "s3/" + b.Store
		}
		return newS3(nil, b.Store, b.Region, "").endpoint() + "/" + b.Store
	case Google:
		return gcsHost + "/" + b.Store
	case Azure:
		host := b.Account
		if host == "" {
			host = "azure"
		}
		return host + ".blob.core.windows.net/" + b.Store
	}
	return b.URI
}

// direct is the store's client and the credential to ask it with, or
// nil when the bucket is read through the CLI.
func (s *Store) direct(ctx context.Context) (client, Cred, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.why != nil {
		return nil, Cred{}, nil
	}
	if s.client == nil {
		switch {
		case s.B.Store == "":
			s.why = errors.New("the platform does not say the bucket's name in its store")
		case s.B.CSP == AWS:
			base := s.Base
			if base == "" {
				base = siteEndpoint()
			}
			s.client = newS3(sharedHTTP, s.B.Store, s.B.Region, base)
		case s.B.CSP == Google:
			s.client = &gcsClient{hc: sharedHTTP, bucket: s.B.Store, base: s.Base}
		default:
			s.why = fmt.Errorf("Studio does not read %s buckets directly yet", s.B.CSP)
		}
		if s.why != nil {
			return nil, Cred{}, nil
		}
	}
	if s.cred == nil {
		cr, err := s.CLI.Token(ctx, s.B.URI)
		if err != nil {
			if ctx.Err() != nil {
				return nil, Cred{}, ctx.Err()
			}
			if errors.Is(err, ErrNoSignIn) {
				return nil, Cred{}, err
			}
			s.why = err
			return nil, Cred{}, nil
		}
		s.cred = &cr
	}
	// The region is the platform's word, or the credential's: without
	// either the partition is not known, and no host is asked on a guess.
	if c, ok := s.client.(*s3Client); ok && c.region() == "" {
		if s.cred.Region == "" {
			s.why = errors.New("the platform does not say which AWS region the bucket is in")
			return nil, Cred{}, nil
		}
		c.move(s.cred.Region)
	}
	return s.client, *s.cred, nil
}

// renew takes a new credential in place of old; another caller may have
// done so already.
func (s *Store) renew(ctx context.Context, old Cred) (Cred, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cred != nil && *s.cred != old {
		return *s.cred, nil
	}
	cr, err := s.CLI.Token(ctx, s.B.URI)
	if err != nil {
		return Cred{}, fmt.Errorf("the bucket's credential ended and a new one could not be had: %w", err)
	}
	s.cred = &cr
	return cr, nil
}

// fallBack reads the bucket through the CLI from here on.
func (s *Store) fallBack(why error) {
	s.mu.Lock()
	s.why = why
	s.mu.Unlock()
}

// tries is how often one request is asked while the store is busy or the
// connection fails; pause the wait before the first of them.
const tries = 5

var pause = 400 * time.Millisecond

// regionRe is a region's name: what a store says goes into the host asked.
var regionRe = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]{1,2}$`)

// ask makes one request of the store until it is answered: again after a
// wait while the store is busy, with a new credential when the one it has
// ended (renewed is told, and at most twice), and in the bucket's own
// region when that was not the one it was asked in.
func (s *Store) ask(ctx context.Context, c client, cr *Cred, do func(Cred) error, renewed func()) error {
	wait, renewals, moved := pause, 0, false
	for n := 1; ; n++ {
		s.Requests.Add(1)
		err := do(*cr)
		if err == nil {
			s.answered.Store(true)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var se *StoreError
		if errors.As(err, &se) {
			s.answered.Store(true)
		}
		if s3, ok := c.(*s3Client); ok && !moved && errors.As(err, &se) && se.region != "" && se.region != s3.region() && regionRe.MatchString(se.region) && partition(se.region) == partition(s3.region()) &&
			(se.Status/100 == 3 || se.Code == "AuthorizationHeaderMalformed") {
			moved = true
			s3.move(se.region)
			continue
		}
		if expired(err) {
			if renewals++; renewals > 2 {
				return err
			}
			next, rerr := s.renew(ctx, *cr)
			if rerr != nil {
				return rerr
			}
			*cr = next
			if renewed != nil {
				renewed()
			}
			continue
		}
		// A store that has not answered yet may not be reached from here at
		// all: that is worth one more try, not five.
		if !passing(err) || n >= tries || n >= 2 && !s.answered.Load() {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
	}
}

// refused is whether the store itself refused or could not be reached,
// where the CLI may still do: it has its own way of asking.
func refused(err error) bool {
	var se *StoreError
	if errors.As(err, &se) {
		return se.Status == http.StatusForbidden || se.Status == http.StatusUnauthorized || se.Status == http.StatusBadRequest ||
			se.Status/100 == 3 || se.Status == http.StatusNotImplemented
	}
	return passing(err)
}

// Walk lists everything under dir (a folder's key, "" for the whole
// bucket), with paths relative to it. An object that marks a folder is
// given as that folder. A credential that ends on the way is renewed and
// the listing goes on after the last key it gave.
func (s *Store) Walk(ctx context.Context, dir string, fn func(Entry) error) error {
	dir = strings.Trim(dir, "/")
	c, cr, err := s.direct(ctx)
	if err != nil {
		return err
	}
	if c != nil {
		n, err := s.walkDirect(ctx, c, cr, dir, fn)
		if err == nil || n > 0 || ctx.Err() != nil || !refused(err) {
			return err
		}
		s.fallBack(err)
		if cerr := s.walkCLI(ctx, dir, fn); cerr != nil {
			return both(err, cerr)
		}
		return nil
	}
	return s.walkCLI(ctx, dir, fn)
}

// both is why neither way did.
func both(direct, cli error) error {
	if strings.TrimSpace(direct.Error()) == strings.TrimSpace(cli.Error()) {
		return direct
	}
	return fmt.Errorf("%w; through pw buckets ls: %s", direct, cli.Error())
}

// errStop is fn's own error, kept apart from the store's.
type errStop struct{ error }

func (e errStop) Unwrap() error { return e.error }

func (s *Store) walkDirect(ctx context.Context, c client, cr Cred, dir string, fn func(Entry) error) (int, error) {
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	n, token, last, resumed := 0, "", "", false
	for {
		var p listPage
		// A renewed credential may not know the old one's page token, so
		// the listing goes on after the last key instead.
		if err := s.ask(ctx, c, &cr, func(cr Cred) (err error) {
			p, err = c.page(ctx, cr, prefix, last, token)
			return err
		}, func() { token, resumed = "", true }); err != nil {
			return n, err
		}
		for _, e := range p.entries {
			if resumed && e.Path <= last || !strings.HasPrefix(e.Path, prefix) {
				// The offset's own key, given again, or a store that
				// answered what was not asked.
				continue
			}
			last = e.Path
			rel := strings.TrimPrefix(e.Path, prefix)
			if strings.HasSuffix(rel, "/") || rel == "" {
				if rel = strings.Trim(rel, "/"); rel == "" {
					continue
				}
				e.IsDir, e.Size = true, 0
			}
			e.Path = rel
			if err := fn(e); err != nil {
				return n + 1, errStop{err}
			}
			n++
		}
		if p.next == "" {
			return n, nil
		}
		if p.next == token && len(p.entries) == 0 {
			return n, errors.New("the store's listing stopped moving")
		}
		token = p.next
	}
}

// notFound makes a store's own words for a missing object NotFound.
func notFound(err error, key string) error {
	var se *StoreError
	if errors.As(err, &se) && se.Status == http.StatusNotFound && se.Code != "NoSuchBucket" {
		return &NotFound{path.Base(key) + " is not in the bucket now"}
	}
	return err
}

// object asks for an object's first n bytes (n < 0: all of it) and says its
// whole size and time. A nil body is an empty object.
func (s *Store) object(ctx context.Context, key string, n int64) (io.ReadCloser, Entry, error) {
	e := Entry{Path: key}
	c, cr, err := s.direct(ctx)
	if err != nil {
		return nil, e, err
	}
	if c != nil {
		var res *http.Response
		err := s.ask(ctx, c, &cr, func(cr Cred) (err error) { res, err = c.get(ctx, cr, key, n); return err }, nil)
		var se *StoreError
		switch {
		case err == nil:
			e.Size = res.ContentLength
			if _, whole, ok := strings.Cut(res.Header.Get("Content-Range"), "/"); ok {
				if size, err := strconv.ParseInt(whole, 10, 64); err == nil {
					e.Size = size
				}
			}
			e.ModTime, _ = http.ParseTime(res.Header.Get("Last-Modified"))
			return res.Body, e, nil
		case errors.As(err, &se) && se.Status == http.StatusRequestedRangeNotSatisfiable:
			return nil, e, nil
		case ctx.Err() != nil || !refused(err):
			return nil, e, notFound(err, key)
		}
		s.fallBack(err)
	}
	e, err = s.statCLI(ctx, key)
	if err != nil {
		return nil, e, err
	}
	if n == 0 || n == 1 {
		// Only the size was wanted.
		return nil, e, nil
	}
	body, err := s.CLI.cat(ctx, s.B.URI, key)
	return body, e, err
}

// checkKey refuses a key that names a folder, or none.
func checkKey(key string) error {
	if key == "" || strings.HasSuffix(key, "/") {
		return &NotFound{"name a file"}
	}
	return nil
}

// Stat is one object's size and time, from a request for its first byte.
func (s *Store) Stat(ctx context.Context, key string) (Entry, error) {
	if err := checkKey(key); err != nil {
		return Entry{}, err
	}
	body, e, err := s.object(ctx, key, 1)
	if body != nil {
		body.Close()
	}
	return e, err
}

// Read is an object's first n bytes, or all of it with n < 0, up to max.
func (s *Store) Read(ctx context.Context, key string, n int64) ([]byte, error) {
	body, err := s.open(ctx, key, n)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	var r io.Reader = body
	if n >= 0 {
		r = io.LimitReader(body, n)
	}
	data, err := io.ReadAll(r)
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return data, err
}

// Open streams an object whole.
func (s *Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.open(ctx, key, -1)
}

func (s *Store) open(ctx context.Context, key string, n int64) (io.ReadCloser, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	if n >= 0 && n < 2 {
		n = 2
	}
	body, _, err := s.object(ctx, key, n)
	if err != nil {
		return nil, err
	}
	if body == nil {
		body = http.NoBody
	}
	return body, nil
}

// ---- through the CLI ----

// cliJobs is how many `pw buckets ls` a listing runs at once.
const cliJobs = 8

// walkCLI lists dir and the folders under it, each with a run of pw.
func (s *Store) walkCLI(ctx context.Context, dir string, fn func(Entry) error) error {
	home, release, err := s.CLI.hold()
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	var (
		mu    sync.Mutex
		cond  = sync.NewCond(&mu)
		queue = []string{dir}
		seen  = map[string]bool{dir: true}
		busy  int
		first error
		wg    sync.WaitGroup
	)
	for range cliJobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock()
			for {
				for len(queue) == 0 && busy > 0 && first == nil {
					cond.Wait()
				}
				if first != nil || len(queue) == 0 {
					cond.Broadcast()
					return
				}
				d := queue[len(queue)-1]
				queue = queue[:len(queue)-1]
				busy++
				mu.Unlock()
				list, err := s.CLI.ls(ctx, home, s.B.URI, d, 0)
				mu.Lock()
				busy--
				for _, e := range list {
					if err != nil || first != nil {
						break
					}
					if !strings.HasPrefix(e.Path, prefix) {
						continue
					}
					if e.IsDir && !seen[e.Path] {
						seen[e.Path] = true
						queue = append(queue, e.Path)
					}
					e.Path = strings.TrimPrefix(e.Path, prefix)
					// Under the lock: fn is one caller's, and takes one
					// entry at a time.
					err = fn(e)
				}
				if err != nil && first == nil {
					first = err
					cancel()
				}
				cond.Broadcast()
			}
		}()
	}
	wg.Wait()
	return first
}

// statCLI is one object's size and time from pw's listing of its own key.
func (s *Store) statCLI(ctx context.Context, key string) (Entry, error) {
	home, release, err := s.CLI.hold()
	if err != nil {
		return Entry{}, err
	}
	defer release()
	// The listing is of every key that starts with this one, in order, so
	// the key itself is first of them.
	list, err := s.CLI.lsAt(ctx, home, s.B.URI, s.B.URI+"/"+key, 10)
	if err != nil {
		return Entry{}, err
	}
	for _, e := range list {
		if e.Path == key && !e.IsDir {
			return e, nil
		}
	}
	return Entry{}, &NotFound{path.Base(key) + " is not in the bucket now"}
}

// Browse is what is directly in a folder of the bucket, for choosing one:
// its folders, and how many objects it has, up to max of both.
func (s *Store) Browse(ctx context.Context, dir string, max int) (list []Entry, more bool, err error) {
	dir = strings.Trim(dir, "/")
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	c, cr, err := s.direct(ctx)
	if err != nil {
		return nil, false, err
	}
	var direct error
	if c != nil {
		var p listPage
		direct = s.ask(ctx, c, &cr, func(cr Cred) (err error) { p, err = c.folder(ctx, cr, prefix, max); return err }, nil)
		if direct == nil {
			for _, e := range p.entries {
				// The folder's own marker.
				if e.Path = strings.Trim(strings.TrimPrefix(e.Path, prefix), "/"); e.Path != "" {
					list = append(list, e)
				}
			}
			return list, p.next != "", nil
		}
		if ctx.Err() != nil || !refused(direct) {
			return nil, false, direct
		}
		s.fallBack(direct)
	}
	home, release, err := s.CLI.hold()
	if err != nil {
		return nil, false, err
	}
	defer release()
	list, err = s.CLI.ls(ctx, home, s.B.URI, dir, max)
	if err != nil {
		if direct != nil && ctx.Err() == nil {
			err = both(direct, err)
		}
		return nil, false, err
	}
	for i := range list {
		list[i].Path = strings.TrimPrefix(strings.TrimPrefix(list[i].Path, dir), "/")
	}
	return list, len(list) >= max, nil
}
