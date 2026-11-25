package userlibs

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"path"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/transfers"
)

// Text files made and edited in the Library, in a library written as its
// owner (kb.TextFS): on the cluster, as the person, with the checks an
// upload's write makes.

// OpenText is a file's bytes, at most max of them.
func (s spaceFiles) OpenText(r *http.Request, lib kb.Library, rel string, max int) ([]byte, error) {
	rel, err := inSpace(rel)
	if err != nil {
		return nil, err
	}
	fs, _, _, _, err := s.target(r, lib)
	if err != nil {
		return nil, err
	}
	return fs.Text(r.Context(), rel, max)
}

// CreateText makes a new file, never over something already there.
func (s spaceFiles) CreateText(r *http.Request, lib kb.Library, rel string, data []byte) error {
	rel, err := inSpace(rel)
	if err != nil {
		return err
	}
	if rel == "" {
		return apierr.New(400, "a file needs a name")
	}
	fs, _, _, d, err := s.target(r, lib)
	if err != nil {
		return err
	}
	defer s.wrote(fs, d.Site, rel)
	ctx := r.Context()
	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	staged := stagingName(dir)
	_, err = fs.Write(ctx, staged, bytes.NewReader(data), 0, 0o644)
	landing := err == nil
	if landing {
		_, err = fs.Land(ctx, []transfers.LandPair{{Staged: staged, Final: rel}})
	}
	if err == nil {
		return nil
	}
	fs.Remove(context.WithoutCancel(ctx), []string{staged}, nil)
	var ae *apierr.Error
	if errors.As(err, &ae) {
		// The name is taken. Before it lands, a 409 is the folder's own
		// refusal (transfers.RemoteFS.Guard).
		if landing && ae.Status == 409 {
			return &kb.TextConflict{State: kb.TextExists}
		}
		// Named as the person named it, not as it was written first.
		return apierr.New(ae.Status, "%s", strings.ReplaceAll(ae.Message, staged, rel))
	}
	return err
}

// SaveText replaces a file's bytes while its version is still base.
func (s spaceFiles) SaveText(r *http.Request, lib kb.Library, rel string, data []byte, base string) error {
	rel, err := inSpace(rel)
	if err != nil {
		return err
	}
	if rel == "" {
		return apierr.New(400, "a file needs a name")
	}
	fs, _, _, d, err := s.target(r, lib)
	if err != nil {
		return err
	}
	defer s.wrote(fs, d.Site, rel)
	st, err := fs.Swap(r.Context(), rel, data, base)
	switch {
	case err != nil:
		return err
	case st.Missing:
		return &kb.TextConflict{State: kb.TextMissing}
	case !st.Done:
		return &kb.TextConflict{State: kb.TextChanged, Version: st.Version}
	}
	return nil
}
