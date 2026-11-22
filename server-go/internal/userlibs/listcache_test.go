package userlibs

import (
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/kb"
)

func one(name string) []kb.Entry { return []kb.Entry{{Name: name, Path: name, Type: "file"}} }

// TestListCacheKeepsEachViewerApart: a listing is kept for the viewer, the
// key and the folder it was read with; another viewer, a new key or a
// sign-out finds nothing, and a write in or above a folder drops its
// listings for everyone.
func TestListCacheKeepsEachViewerApart(t *testing.T) {
	var c listCache
	ada := listKey{owner: "ada", key: keyHash("key-ada"), site: "cluster-a", root: "/home/ada/studio", rel: "papers"}
	if _, _, ok := c.get(ada); ok {
		t.Fatal("a listing before any was read")
	}
	c.put(ada, one("a.pdf"), time.Now())
	got, again, ok := c.get(ada)
	if !ok || again || len(got) != 1 || got[0].Name != "a.pdf" {
		t.Fatalf("got %v %v %v", got, again, ok)
	}
	got[0].Name = "changed by the caller"
	if again, _, _ := c.get(ada); again[0].Name != "a.pdf" {
		t.Fatal("a caller changed the kept listing")
	}
	// Another person asking for the same folder, with any key, finds nothing.
	bob := ada
	bob.owner, bob.key = "bob", keyHash("key-bob")
	if _, _, ok := c.get(bob); ok {
		t.Fatal("another person was answered from ada's listing")
	}
	bob.key = ada.key
	if _, _, ok := c.get(bob); ok {
		t.Fatal("another person with ada's key hash was answered from ada's listing")
	}
	// A new key drops every listing read with the old one.
	newKey := ada
	newKey.key = keyHash("key-ada-2")
	if _, _, ok := c.get(newKey); ok {
		t.Fatal("a listing read with the old key was answered for the new one")
	}
	if _, _, ok := c.get(ada); ok {
		t.Fatal("the old key's listings were kept after the key changed")
	}
	// Signing out drops them.
	c.put(ada, one("a.pdf"), time.Now())
	c.forgetOwner("ada")
	if _, _, ok := c.get(ada); ok {
		t.Fatal("a listing outlived the sign-out")
	}
	// A write in the folder, in one inside it, or in one above it drops
	// it, for everyone who reads that folder; a write elsewhere does not.
	c.put(ada, one("a.pdf"), time.Now())
	carl := listKey{owner: "carl", key: keyHash("key-carl"), site: "cluster-a", root: "/home/ada/studio/papers", rel: ""}
	c.get(carl)
	c.put(carl, one("a.pdf"), time.Now())
	c.changed("cluster-a", "/home/ada/studio/other")
	if _, _, ok := c.get(ada); !ok {
		t.Fatal("a write in another folder dropped the listing")
	}
	c.changed("cluster-b", "/home/ada/studio/papers")
	if _, _, ok := c.get(ada); !ok {
		t.Fatal("a write on another cluster dropped the listing")
	}
	c.changed("cluster-a", "/home/ada/studio/papers/new")
	if _, _, ok := c.get(ada); ok {
		t.Fatal("a write inside the folder left its listing")
	}
	if _, _, ok := c.get(carl); ok {
		t.Fatal("a write in a shared folder left the recipient's listing")
	}
	c.put(ada, one("a.pdf"), time.Now())
	c.changed("cluster-a", "/home/ada")
	if _, _, ok := c.get(ada); ok {
		t.Fatal("a write above the folder left its listing")
	}
}

// TestListCacheAges: fresh, a listing is answered as it is; then answered
// while it is read again behind it, once; past listStale it is read first.
// A listing read before one already kept does not replace it.
func TestListCacheAges(t *testing.T) {
	var c listCache
	k := listKey{owner: "ada", key: keyHash("k"), site: "s", root: "/r", rel: ""}
	c.get(k)
	c.put(k, one("new"), time.Now().Add(-listFresh-time.Second))
	if _, again, ok := c.get(k); !ok || !again {
		t.Fatal("an older listing was not read again")
	}
	if _, again, ok := c.get(k); !ok || again {
		t.Fatal("a listing being read again was read again twice")
	}
	c.put(k, one("older"), time.Now().Add(-time.Hour))
	if got, _, _ := c.get(k); got[0].Name != "new" {
		t.Fatal("an older read replaced a newer one")
	}
	c.put(k, one("older still"), time.Now().Add(-listStale-time.Second))
	if got, _, _ := c.get(k); got[0].Name != "new" {
		t.Fatal("a read older than the one kept replaced it")
	}
	c.failed(k)
	c.put(k, one("stale"), time.Now().Add(-listStale-time.Second))
	if _, _, ok := c.get(k); ok {
		t.Fatal("a listing past listStale was answered")
	}
	for i := 0; i < listMax+10; i++ {
		x := k
		x.rel = string(rune('a'+i%26)) + time.Duration(i).String()
		c.put(x, one("x"), time.Now())
	}
	if len(c.m) > listMax {
		t.Fatalf("%d listings kept, more than %d", len(c.m), listMax)
	}
}

// TestListCacheReadAcrossAWrite: a read that began before a write in its
// folder (or one inside or above it) is answered but not kept, since it may
// be from before the write; a write elsewhere, or one before the read
// began, does not stop it being kept.
func TestListCacheReadAcrossAWrite(t *testing.T) {
	var c listCache
	k := listKey{owner: "ada", key: keyHash("k"), site: "s", root: "/home/ada/studio", rel: "papers"}
	c.get(k)
	since := c.mark()
	c.changed("s", "/home/ada/studio/papers")
	c.putRead(k, one("before the upload"), time.Now(), since)
	if got, _, ok := c.get(k); ok {
		t.Fatalf("a read across a write was kept: %v", got)
	}
	since = c.mark()
	c.changed("s", "/home/ada/studio/other")
	c.changed("t", "/home/ada/studio/papers")
	c.putRead(k, one("a.pdf"), time.Now(), since)
	if _, _, ok := c.get(k); !ok {
		t.Fatal("a write elsewhere stopped a read being kept")
	}
	// A read that began before writes no longer remembered is not kept.
	c.failed(k)
	since = c.mark()
	for i := 0; i < listRecent+1; i++ {
		c.changed("s", "/elsewhere")
	}
	c.putRead(k, one("a.pdf"), time.Now(), since)
	if _, _, ok := c.get(k); ok {
		t.Fatal("a read older than every remembered write was kept")
	}
}
