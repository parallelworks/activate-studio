package kb

import (
	"context"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
)

// Indexer re-indexes the main library after its files change and says
// what derived state is stale. ProviderIndexer is the one in use: it reads
// the app's providers, which the index track fills (internal/index) and
// which until then reach Node.
type Indexer interface {
	// IndexDir is indexing.ts's incrementalIndexDir: one top-level subtree
	// (rel is not empty), after the jobs ahead of it.
	IndexDir(ctx context.Context, rel string) (ms int64, err error)
	// IndexRootDB is indexRootDb: the files directly under the root.
	IndexRootDB(ctx context.Context) (ms int64, err error)
	// Changed: "dbList", "context", "tagMaps" (app.Providers.Changed).
	Changed(ctx context.Context, what ...string)
}

// ProviderIndexer is an Indexer over a.Providers, read at each call so a
// provider set later is the one used.
type ProviderIndexer struct{ A *app.App }

func (p ProviderIndexer) IndexDir(ctx context.Context, rel string) (int64, error) {
	return p.A.Providers.IndexDirMS(ctx, rel)
}
func (p ProviderIndexer) IndexRootDB(ctx context.Context) (int64, error) {
	return p.A.Providers.IndexRootDB(ctx)
}
func (p ProviderIndexer) Changed(ctx context.Context, what ...string) { p.A.Providers.Changed(ctx, what...) }

// NoIndexer re-indexes nothing (tests; the sweep finds the change).
type NoIndexer struct{}

func (NoIndexer) IndexDir(context.Context, string) (int64, error) { return 0, nil }
func (NoIndexer) IndexRootDB(context.Context) (int64, error)      { return 0, nil }
func (NoIndexer) Changed(context.Context, ...string)              {}

// ReindexForFile re-indexes whatever covers a file: its top-level subtree,
// or the root's db for a file directly under the root.
func ReindexForFile(ctx context.Context, ix Indexer, rel string) (int64, error) {
	c := trimSlashes(rel)
	if i := strings.IndexByte(c, '/'); i >= 0 {
		return ix.IndexDir(ctx, c[:i])
	}
	return ix.IndexRootDB(ctx)
}

// ReindexForDir re-indexes whatever covers a directory.
func ReindexForDir(ctx context.Context, ix Indexer, rel string) (int64, error) {
	c := trimSlashes(rel)
	if c == "" {
		return ix.IndexRootDB(ctx)
	}
	return ix.IndexDir(ctx, strings.SplitN(c, "/", 2)[0])
}

// ReindexRoots is move.ts's reindexRoots: each touched subtree once, then
// the root's db.
func ReindexRoots(ctx context.Context, ix Indexer, roots []string, rootDB bool) (int64, error) {
	var ms int64
	for _, r := range roots {
		n, err := ix.IndexDir(ctx, r)
		if err != nil {
			return ms, err
		}
		ms += n
	}
	if rootDB {
		n, err := ix.IndexRootDB(ctx)
		if err != nil {
			return ms, err
		}
		ms += n
	}
	return ms, nil
}
