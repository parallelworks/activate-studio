package userlibs

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/folders"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// What Add a library chooses from on other systems: the hosts in the
// person's ssh config, a folder's folders, and what a folder holds. Each is
// read as the person, over the session a library there is read with.

// sshHosts is the hosts the ssh config of the account the Studio runs as
// names, on a Studio one person uses, whose account that is: their names
// and nothing else of the file.
func (m *Module) sshHosts(w who) (any, error) {
	if !w.sshAllowed() {
		return nil, errSSHSingle()
	}
	home := m.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	names := []any{}
	if home != "" {
		for _, n := range remote.ConfigHosts(filepath.Join(home, ".ssh", "config")) {
			names = append(names, n)
		}
	}
	return jsonv.NewObject("hosts", names), nil
}

// remoteFolders lists the folders in one directory of the system a request
// names (GET ?cluster= or ?destination=&sshOptions=, and path and filter),
// as the person.
func (m *Module) remoteFolders(r *http.Request, w who) (any, error) {
	q := r.URL.Query()
	b := jsonv.NewObject()
	for _, k := range []string{"cluster", "destination", "sshOptions"} {
		if v := q.Get(k); v != "" {
			b.Set(k, v)
		}
	}
	t, err := m.spaceSession(w, b)
	if err != nil {
		return nil, err
	}
	dir := q.Get("path")
	if controlChars.MatchString(dir) || units(dir) > 1024 {
		return nil, apierr.New(400, "path: a folder on that host")
	}
	res, err := m.Pool.Run(r.Context(), t.site, t.key, []string{"sh", "-c", folders.Script, "studio-folders", dir}, 30*time.Second)
	if err != nil {
		return nil, m.reachErr(r.Context(), t, err)
	}
	l := folders.Parse(res.Stdout)
	if l == nil {
		return nil, apierr.New(400, "%s is not a folder your account there can read.", dir)
	}
	return l.Filter(q.Get("filter")), nil
}

// countSpace is what the folder a request names holds, counted as the
// person for a few seconds at most; null where it cannot be read.
func (m *Module) countSpace(ctx context.Context, w who, b *jsonv.Object) (any, error) {
	t, err := m.spaceSession(w, b)
	if err != nil {
		return nil, err
	}
	folder, err := spaceFolder(b)
	if err != nil {
		return nil, err
	}
	secs := jsonv.FormatNumber(folders.CountFor.Seconds())
	res, err := m.Pool.Run(ctx, t.site, t.key, []string{"sh", "-c", folders.CountScript, "studio-count", folder, secs}, folders.CountFor+25*time.Second)
	if err != nil {
		return nil, m.reachErr(ctx, t, err)
	}
	var count any
	if c := folders.ParseCount(res.Stdout); c != nil {
		count = c
	}
	return struct {
		Count any `json:"count"`
	}{count}, nil
}

// reachErr is why a session to t did not open, with what to do about it.
func (m *Module) reachErr(ctx context.Context, t *spaceTarget, err error) error {
	if t.transport == "ssh" && ctx.Err() == nil {
		return apierr.New(502, "%s", t.host.Advice(err.Error()))
	}
	return m.sessionErr(t.name, t.key, err)
}
