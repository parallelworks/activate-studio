package index

import (
	"context"
	"fmt"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
)

// overlayTags is the files module's labels, which hold the overlay; nil
// before kb.Register runs, or in a server without it.
var overlayTags = func() *kb.Tags {
	if m := kb.Default; m != nil {
		return m.Tags
	}
	return nil
}

// reapplyOverlay is tags.ts reapplyTagOverlay: on a filesystem without
// xattrs the labels live only in the overlay, and an index rebuilt below
// prefix ("" is all of it) has lost them. It returns how many paths took
// their labels back.
func reapplyOverlay(ctx context.Context, prefix string) int {
	t := overlayTags()
	if t == nil {
		return 0
	}
	return t.ReapplyOverlay(ctx, prefix)
}

// registerOverlay matches Node's two call sites: indexing.ts afterChange,
// after every build, sweep and folder pass this process runs, and main.ts
// once at start. With Node behind the Go server Node's own start does the
// second.
func registerOverlay(a *app.App, ix *Indexer) {
	ix.OnChange(func(rel string) { reapplyOverlay(context.Background(), rel) })
	if !a.NodeFree() {
		return
	}
	a.OnStart("index: tag overlay", func(ctx context.Context) {
		if n := reapplyOverlay(ctx, ""); n > 0 {
			a.Log.Info(fmt.Sprintf("tag overlay: re-applied %d labeled paths", n))
		}
	})
}
