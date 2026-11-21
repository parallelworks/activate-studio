package userlibs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/kb"
)

// The Library's listings of a person's space (or a folder shared with
// them), kept for a short while so a folder seen a moment ago, or one its
// parent's listing already read, opens at once. An entry is the folder as
// the viewer's own session read it, under the viewer, their key and the
// folder: one person never gets another's, and a new key, a sign-out or a
// share taken back drops what was kept. A listing is only answered from
// here after the request passed the same checks as one read from the
// cluster (spaceFiles.target). The Studio's own writes there drop the
// listings of that folder for everyone, so the next one is read again.

var (
	// listFresh is how long a kept listing is answered as it is.
	listFresh = 5 * time.Second
	// listStale is how long a kept listing is still answered at once
	// while it is read again behind it; older, it is read first.
	listStale = 2 * time.Minute
	// listMax is the most listings kept, the oldest dropped first.
	listMax = 4000
)

type listKey struct {
	owner, key, site, root, rel string
}

type keptList struct {
	at         time.Time
	entries    []kb.Entry
	refreshing bool
}

type listCache struct {
	mu sync.Mutex
	m  map[listKey]*keptList
	// keys is the key each owner's listings were read with.
	keys map[string]string
}

func keyHash(key string) string {
	s := sha256.Sum256([]byte(key))
	return hex.EncodeToString(s[:])[:24]
}

func copyEntries(es []kb.Entry) []kb.Entry {
	return append(make([]kb.Entry, 0, len(es)), es...)
}

// get is a kept listing and whether it should be read again behind it.
func (c *listCache) get(k listKey) ([]kb.Entry, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.keys[k.owner] != k.key {
		// Another key than the listings were read with: none of them.
		c.dropOwnerLocked(k.owner)
		if c.keys == nil {
			c.keys = map[string]string{}
		}
		c.keys[k.owner] = k.key
		return nil, false, false
	}
	e := c.m[k]
	if e == nil {
		return nil, false, false
	}
	age := time.Since(e.at)
	if age > listStale {
		delete(c.m, k)
		return nil, false, false
	}
	again := age > listFresh && !e.refreshing
	if again {
		e.refreshing = true
	}
	return copyEntries(e.entries), again, true
}

func (c *listCache) put(k listKey, entries []kb.Entry, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.keys[k.owner] != k.key {
		return
	}
	if c.m == nil {
		c.m = map[listKey]*keptList{}
	}
	if old := c.m[k]; old != nil && old.at.After(at) {
		return
	}
	c.m[k] = &keptList{at: at, entries: copyEntries(entries)}
	if len(c.m) > listMax {
		var oldest listKey
		var t time.Time
		for key, e := range c.m {
			if t.IsZero() || e.at.Before(t) {
				oldest, t = key, e.at
			}
		}
		delete(c.m, oldest)
	}
}

// failed drops a listing whose reading again failed.
func (c *listCache) failed(k listKey) {
	c.mu.Lock()
	delete(c.m, k)
	c.mu.Unlock()
}

func (c *listCache) dropOwnerLocked(owner string) {
	for k := range c.m {
		if k.owner == owner {
			delete(c.m, k)
		}
	}
	delete(c.keys, owner)
}

// forgetOwner drops everything kept for a person: they signed out or
// changed their key.
func (c *listCache) forgetOwner(owner string) {
	c.mu.Lock()
	c.dropOwnerLocked(owner)
	c.mu.Unlock()
}

// changed drops, for every viewer, the listings of folder (an absolute
// path on site) and of every folder in it or above it, so a write there
// shows to the writer and to anyone the folder is shared with.
func (c *listCache) changed(site, folder string) {
	folder = path.Clean(folder)
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if k.site != site {
			continue
		}
		at := path.Join(k.root, k.rel)
		if within(at, folder) || within(folder, at) {
			delete(c.m, k)
		}
	}
}

func within(p, dir string) bool {
	return p == dir || dir == "/" || strings.HasPrefix(p, dir+"/")
}

// spaceList is a folder of a space as the viewer's session reads it now,
// with the folders in it kept for the next click.
func (s spaceFiles) read(ctx context.Context, k listKey, fs listTwo, rel string) ([]kb.Entry, error) {
	at := time.Now()
	two, err := fs.ListTwo(ctx, rel, 2000)
	if err != nil {
		s.m.lists.failed(k)
		return nil, err
	}
	out := entriesOf(rel, two.Entries)
	s.m.lists.put(k, out, at)
	for d, es := range two.Children {
		ck := k
		ck.rel = d
		s.m.lists.put(ck, entriesOf(d, es), at)
	}
	return out, nil
}

// readBehind reads a kept listing again without the viewer waiting.
func (s spaceFiles) readBehind(k listKey, fs listTwo, rel string) {
	go func() {
		ctx, cancel := context.WithTimeout(s.m.Background(), time.Minute)
		defer cancel()
		s.read(ctx, k, fs, rel)
	}()
}
