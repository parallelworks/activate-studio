package transfers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// A bucket library (docs/LIBRARIES.md, Buckets) indexes an rclone remote's
// folder from its listing alone and reads one object when it is opened.
// Bucket is that remote as the library reads it: the whole listing in one
// fast list, and an object, or its first bytes, on demand. Every read
// uses the Studio account's own rclone configuration, so a bucket library
// is offered only with sign-in off, as copy libraries are.

// Bucket is an rclone remote read for a bucket library.
type Bucket struct {
	fs *RcloneFS
	// Location is the Transfers location id; Label its name.
	Location string
	Label    string
}

// BucketEntry is one entry of a bucket's listing. A folder of an object
// store is a prefix: rclone gives it with no size and the listing's time.
type BucketEntry struct {
	Path    string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// Bucket is the rclone remote of location id, for a bucket library.
func (s *Service) Bucket(ctx context.Context, v Viewer, id string) (*Bucket, error) {
	kind, _, _ := strings.Cut(id, ":")
	if kind != KindRclone {
		return nil, apierr.New(400, "location: an rclone remote from Transfers")
	}
	l, err := s.resolve(ctx, v, id)
	if err != nil {
		return nil, err
	}
	f, ok := asRclone(l.fs)
	if !ok {
		why := l.Why
		if why == "" {
			why = id + " is not an rclone remote"
		}
		return nil, apierr.New(400, "%s", why)
	}
	return &Bucket{fs: f, Location: id, Label: l.Label}, nil
}

// NewBucket is a Bucket over rclone program bin and remote, for tests and
// for a caller that already resolved the location.
func NewBucket(bin, remote, typ string) *Bucket {
	return &Bucket{fs: &RcloneFS{Bin: bin, Remote: remote, Type: typ}, Location: KindRclone + ":" + remote, Label: remote}
}

// Remote is the remote's name.
func (b *Bucket) Remote() string { return b.fs.Remote }

// Spec is a path of the remote as rclone names it, "remote:path".
func (b *Bucket) Spec(rel string) string { return b.fs.spec(rel) }

// Bin is the rclone program.
func (b *Bucket) Bin() string { return b.fs.Bin }

// Endpoint names the store the remote reaches, the same on every computer
// whatever the remote is called: its endpoint's host, for AWS
// s3.<region>.amazonaws.com, or else its backend type. From rclone's
// configuration with its secrets redacted; "" when rclone does not say.
func (b *Bucket) Endpoint(ctx context.Context) string {
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := runRclone(c, b.fs.Bin, nil, "config", "redacted", b.fs.Remote)
	if err != nil {
		return ""
	}
	if ep := redactedValue(out, "endpoint"); ep != "" {
		if u, err := url.Parse(ep); err == nil && u.Host != "" {
			return u.Host
		}
		return strings.TrimRight(ep, "/")
	}
	if redactedValue(out, "provider") == "AWS" {
		region := redactedValue(out, "region")
		if region == "" {
			region = "us-east-1"
		}
		return "s3." + region + ".amazonaws.com"
	}
	return redactedValue(out, "type")
}

// Type is the remote's backend type (s3, azureblob, ...), "" when not known.
func (b *Bucket) Type() string { return b.fs.Type }

// Walk lists everything under rel with one fast list (`rclone lsjson -R
// --fast-list`): an object store answers a page of up to 1,000 keys per
// request, with no request per folder and none per object. A remote that
// cannot list that way is listed folder by folder, as rclone does.
func (b *Bucket) Walk(ctx context.Context, rel string, fn func(BucketEntry) error) error {
	return b.fs.walkDirArgs(ctx, rel, []string{"-R", "--fast-list"}, func(e lsEntry) error {
		return fn(BucketEntry{Path: e.Path, Size: e.Size, ModTime: e.ModTime, IsDir: e.IsDir})
	})
}

// Stat is one object, from a single request for it (`lsjson --stat`).
// An object store answers for a key that is not there as for a prefix,
// so a missing object comes back as a folder; a caller opening a file
// takes IsDir as not found.
func (b *Bucket) Stat(ctx context.Context, rel string) (BucketEntry, error) {
	if rel == "" {
		return BucketEntry{IsDir: true}, nil
	}
	out, err := b.fs.run(ctx, nil, append(lsArgs("--stat"), "--", b.fs.spec(rel))...)
	if err != nil {
		return BucketEntry{}, err
	}
	var e lsEntry
	if err := json.Unmarshal(out, &e); err != nil {
		return BucketEntry{}, apierr.New(502, "%s: rclone's answer could not be read", b.fs.Remote)
	}
	return BucketEntry{Path: rel, Size: e.Size, ModTime: e.ModTime, IsDir: e.IsDir}, nil
}

// Read is an object's first n bytes, or all of it with n < 0. rclone
// asks the store for that range alone (`cat --count`), so a preview of a
// large object reads only what it shows.
func (b *Bucket) Read(ctx context.Context, rel string, n int64) ([]byte, error) {
	if rel == "" {
		return nil, apierr.New(400, "name a file")
	}
	args := []string{"cat"}
	if n >= 0 {
		args = append(args, "--count", strconv.FormatInt(n, 10))
	}
	out, err := b.fs.run(ctx, nil, append(args, "--", b.fs.spec(rel))...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Open streams an object whole.
func (b *Bucket) Open(ctx context.Context, rel string) (io.ReadCloser, error) {
	return b.fs.Read(ctx, rel)
}

// Provider is the s3 provider the remote names (AWS, Minio, Ceph, Other
// ...), from rclone's configuration with its secrets redacted (`rclone
// config redacted`), so a request estimate can say whose prices apply.
// "" for another backend, or when rclone does not say.
func (b *Bucket) Provider(ctx context.Context) string {
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := runRclone(c, b.fs.Bin, nil, "config", "redacted", b.fs.Remote)
	if err != nil {
		return ""
	}
	return redactedValue(out, "provider")
}

// redactedValue is key's value in the first section of an rclone
// configuration.
func redactedValue(out []byte, key string) string {
	sc := bufio.NewScanner(bytes.NewReader(out))
	sections := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			if sections++; sections > 1 {
				break
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
