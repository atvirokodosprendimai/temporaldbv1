package vector

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Index combines a TEIClient and QdrantClient into TemporalDB's vector
// search backend (ADR-001 D6). One Qdrant collection per TemporalDB
// collection, same name: a Qdrant search is already scoped by picking the
// right Qdrant collection, so only the TemporalDB key — not the full
// "collection/key" path — needs to travel in the payload.
type Index struct {
	TEI    *TEIClient
	Qdrant *QdrantClient
}

// NewIndex builds an Index from already-constructed clients.
func NewIndex(tei *TEIClient, qdrant *QdrantClient) *Index {
	return &Index{TEI: tei, Qdrant: qdrant}
}

// PointID derives a stable Qdrant point ID (a UUID) from a TemporalDB
// key. Qdrant requires a UUID or unsigned integer ID, which an arbitrary
// key string is neither — and the mapping must be deterministic (the
// same key always produces the same point ID), or re-indexing a PUT would
// create a duplicate point instead of updating the existing one.
func PointID(key string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(key)).String()
}

// Upsert embeds text and stores it in collection under key, creating the
// Qdrant collection first if needed. TemporalDB remains the source of
// truth; Qdrant is a derived index, rebuildable by re-calling this for
// every document. Automatic invocation on every PUT is not wired up in
// this pass — see docs/adr/BACKLOG.md.
func (idx *Index) Upsert(ctx context.Context, collection, key, text string) error {
	vecs, err := idx.TEI.Embed(ctx, []string{text})
	if err != nil {
		return fmt.Errorf("vector: index: embed %s/%s: %w", collection, key, err)
	}
	if err := idx.Qdrant.EnsureCollection(ctx, collection, len(vecs[0])); err != nil {
		return err
	}
	point := Point{ID: PointID(key), Vector: vecs[0], Payload: map[string]any{"key": key}}
	if err := idx.Qdrant.Upsert(ctx, collection, []Point{point}); err != nil {
		return fmt.Errorf("vector: index: upsert %s/%s: %w", collection, key, err)
	}
	return nil
}

// Search embeds queryText, searches collection, and returns the matched
// keys in relevance order. Its signature matches tql.Searcher, satisfied
// structurally — this package does not import internal/tql (that would
// invert the dependency direction the executor already has on Searcher),
// so cmd/temporaldb-server wires *Index in wherever a tql.Searcher is
// expected.
func (idx *Index) Search(ctx context.Context, collection, queryText string, limit int) ([]string, error) {
	vecs, err := idx.TEI.Embed(ctx, []string{queryText})
	if err != nil {
		return nil, fmt.Errorf("vector: index: embed query: %w", err)
	}
	if limit <= 0 {
		limit = 10
	}
	results, err := idx.Qdrant.Search(ctx, collection, vecs[0], limit)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(results))
	for _, r := range results {
		key, ok := r.Payload["key"].(string)
		if !ok {
			continue // a point this package didn't write; skip rather than fail the whole search
		}
		keys = append(keys, key)
	}
	return keys, nil
}
