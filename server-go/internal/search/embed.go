package search

import (
	"context"

	"github.com/parallelworks/activate-studio/server-go/internal/embed"
)

// Embedder embeds search queries: 384 little-endian float32 values per
// text (embedWorker.ts embedQueries).
//
// QueryEmbedder, over the index track's embed.Queries, is the one the
// server uses.
type Embedder interface {
	Present() bool
	Embed(ctx context.Context, texts []string) ([][]byte, error)
}

// QueryEmbedder is embed.Queries (the embed-query child) for the model
// in Dir.
type QueryEmbedder struct {
	Dir string
	Q   *embed.Queries
}

// Present is onnxModelPresent.
func (q *QueryEmbedder) Present() bool { return embed.ModelPresent(q.Dir) }

// Embed is embedQueries.
func (q *QueryEmbedder) Embed(ctx context.Context, texts []string) ([][]byte, error) {
	type ans struct {
		v   [][]byte
		err error
	}
	ch := make(chan ans, 1)
	go func() { v, err := q.Q.Embed(texts); ch <- ans{v, err} }()
	select {
	case a := <-ch:
		return a.v, a.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
