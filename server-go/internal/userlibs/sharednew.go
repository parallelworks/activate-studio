package userlibs

import (
	"context"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// SharedNew is a folder someone shared with a person, or with a group they
// are in, which they have not made a library of: a notice (internal/notices).
// It holds nothing Add a library's list of folders shared with them does not.
type SharedNew struct {
	ID string
	// From is the sharer's account, Label and Folder the folder, Site the
	// system it is on and System its name.
	From, Label, Folder, Site, System string
	// Group is the group it is shared with, "" where it is the person.
	Group  string
	Access string
	At     string
}

// How long what a person's session said is kept: while something is shared
// with them that they have not added, and otherwise, for a group they join.
// One that failed is tried again after lookAgain, and what the one before it
// said is kept for lookKeep. A check is bounded by lookTimeout, at most lookMax
// folders at a time.
const (
	lookShown = 10 * time.Minute
	lookGroup = 30 * time.Minute
	lookAgain = 5 * time.Minute
	lookKeep  = time.Hour
	lookMax   = 200
)

var lookTimeout = 60 * time.Second

// shareLook is what a person's session on a system said: the account and
// groups it reaches, and which shares' folders open to it, by the share.
type shareLook struct {
	id    clusterIdent
	open  map[string]bool
	at    time.Time // of the last answer
	tried time.Time
	busy  bool
}

// lookSh prints the account and its groups, and which of the folders given
// it can list, by their place among them.
const lookSh = `echo "uid=$(id -u)"; echo "user=$(id -un)"; echo "groups=$(id -Gn)"
i=0; o=; for f; do [ -d "$f" ] && [ -r "$f" ] && [ -x "$f" ] && o="$o $i"; i=$((i+1)); done; echo "open=$o"; echo done=1`

// SharedNew is what is shared with the person r is from, that they have not
// added. Whom a share reaches is for their own session on its system to say
// (its account, its groups, and whether the folder opens), so it is known
// for someone who signs in for the first time, for a group's members as the
// system has them, and no longer once the share is taken back. Nothing is
// waited for: looking is whether a session is being asked now, for a reader
// that comes again. A share with every account is not told.
func (m *Module) SharedNew(r *http.Request) (list []SharedNew, looking bool) {
	w, err := m.whoOf(r)
	if err != nil {
		return nil, false
	}
	owner := w.owner()
	if owner == "" {
		return nil, false
	}
	bySite := map[string][]Share{}
	var sites []string
	for _, s := range m.loadShares() {
		if s.Kind == "everyone" || s.Owner == owner || slices.Contains(s.Left, owner) ||
			slices.ContainsFunc(s.Adopters, func(a Adopter) bool { return a.Owner == owner }) {
			continue
		}
		if bySite[s.Site] == nil {
			sites = append(sites, s.Site)
		}
		bySite[s.Site] = append(bySite[s.Site], s)
	}
	if len(sites) == 0 {
		return nil, false
	}
	// Their own credential, or nothing is known of them there.
	key := m.keyOf(w, owner)
	if key == "" {
		return nil, false
	}
	for _, site := range sites {
		shares := bySite[site]
		if len(shares) > lookMax {
			slices.SortStableFunc(shares, func(a, b Share) int { return strings.Compare(b.CreatedAt, a.CreatedAt) })
			shares = shares[:lookMax]
		}
		id, open, known, busy := m.looked(owner, site, key, shares)
		looking = looking || busy
		if !known {
			continue
		}
		for _, s := range shares {
			if !s.tells(id) || !open[s.ID] {
				continue
			}
			n := SharedNew{ID: s.ID, From: s.OwnerAccount, Label: s.SpaceLabel, Folder: s.Folder, Site: s.Site, System: remote.ShortName(s.Site), Access: s.Access, At: s.CreatedAt}
			if s.Rel != "" {
				n.Label = path.Base(s.Rel)
			}
			if s.Kind == "group" {
				n.Group = s.Name
			}
			list = append(list, n)
		}
	}
	return list, looking
}

// tells: s reaches the account id, which is not the one that shared it.
func (s Share) tells(id clusterIdent) bool { return id.account != s.OwnerAccount && s.reaches(id) }

// looked is what owner's session on site said of shares, when it did, and
// whether it is being asked now: it is when it has not said, when a share
// that reaches them is new to it, and when what it said is old.
func (m *Module) looked(owner, site, key string, shares []Share) (id clusterIdent, open map[string]bool, known, busy bool) {
	k := owner + "|" + keyID(key) + "|" + site
	now := time.Now()
	m.sharesMu.Lock()
	defer m.sharesMu.Unlock()
	if m.looks == nil {
		m.looks = map[string]*shareLook{}
	}
	l := m.looks[k]
	if l == nil {
		l = &shareLook{}
		m.looks[k] = l
	}
	known = !l.at.IsZero() && now.Sub(l.at) < lookKeep
	ask := l.at.IsZero()
	for _, s := range shares {
		if l.at.IsZero() {
			break
		}
		_, seen := l.open[s.ID]
		switch {
		case s.tells(l.id):
			ask = ask || !seen || now.Sub(l.at) > lookShown
		case s.Kind == "group":
			ask = ask || now.Sub(l.at) > lookGroup
		}
	}
	if ask && !l.busy && (!l.tried.After(l.at) || now.Sub(l.tried) > lookAgain) {
		l.busy = true
		go m.look(l, owner, site, key, shares)
	}
	return l.id, l.open, known, l.busy
}

// look asks owner's session on site, and keeps what it says in l.
func (m *Module) look(l *shareLook, owner, site, key string, shares []Share) {
	ctx, cancel := context.WithTimeout(m.Background(), lookTimeout)
	defer cancel()
	id, open, err := m.lookThere(ctx, owner, site, key, shares)
	m.sharesMu.Lock()
	defer m.sharesMu.Unlock()
	l.busy, l.tried = false, time.Now()
	if err != nil {
		if m.Log != nil {
			m.Log.Debug("shared folders were not checked", "site", site, "err", err.Error())
		}
		return
	}
	l.id, l.open, l.at = id, open, l.tried
}

func (m *Module) lookThere(ctx context.Context, owner, site, key string, shares []Share) (clusterIdent, map[string]bool, error) {
	var id clusterIdent
	if err := m.theirs(ctx, owner, site, key); err != nil {
		return id, nil, err
	}
	argv := []string{"sh", "-c", lookSh, "_"}
	for _, s := range shares {
		argv = append(argv, s.Folder)
	}
	r, err := m.Pool.Run(ctx, site, key, argv, lookTimeout)
	if err != nil {
		return id, nil, err
	}
	kv := keyValues(r.Stdout)
	uid, err := strconv.Atoi(kv["uid"])
	if err != nil || kv["user"] == "" || kv["done"] != "1" {
		return id, nil, apierr.New(502, "%s did not say which account the session reaches.", remote.ShortName(site))
	}
	open := make(map[string]bool, len(shares))
	folders := strings.Fields(kv["open"])
	for i, s := range shares {
		open[s.ID] = slices.Contains(folders, strconv.Itoa(i))
	}
	return clusterIdent{account: kv["user"], uid: uid, groups: strings.Fields(kv["groups"])}, open, nil
}

// theirs refuses a system owner did not choose: a share names its own, and
// the Studio opens no session as someone on another person's machine
// because a folder was shared there. The Studio's cluster, one they have a
// library on, and a running one of their own on the platform are theirs.
func (m *Module) theirs(ctx context.Context, owner, site, key string) error {
	if site == HomeCluster() {
		return nil
	}
	for _, d := range m.load(owner) {
		if d.Site == site {
			return nil
		}
	}
	if strings.HasPrefix(site, remote.SSHScheme) {
		return apierr.New(409, "%s is reached over ssh.", remote.ShortName(site))
	}
	name := remote.ShortName(site)
	at, err := m.resourceOf(ctx, name, key)
	if err != nil {
		return err
	}
	if site != name && site != at {
		return apierr.New(409, "%s is not one of their clusters.", site)
	}
	return nil
}

// forgetLooks drops what a person's sessions said: they signed out or
// changed their key.
func (m *Module) forgetLooks(owner string) {
	m.sharesMu.Lock()
	defer m.sharesMu.Unlock()
	for k := range m.looks {
		if strings.HasPrefix(k, owner+"|") {
			delete(m.looks, k)
		}
	}
}
