package graph

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/atvirokodosprendimai/temporaldbv1/internal/event"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/storagetest"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	db := storagetest.DB(t)
	t.Cleanup(func() { storagetest.Reset(t, db) })
	return NewStore(event.NewStore(db), db)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func mustRelate(t *testing.T, s *Store, ctx context.Context, fromColl, fromKey, edgeType, toColl, toKey string, props json.RawMessage) Edge {
	t.Helper()
	e, err := s.Relate(ctx, fromColl, fromKey, edgeType, toColl, toKey, props)
	if err != nil {
		t.Fatalf("Relate: %v", err)
	}
	return e
}

func TestRelateAndRelated(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	mustRelate(t, s, ctx, "users", "1", "knows", "users", "2", nil)

	edges, err := s.Related(ctx, "users", "1", "", 0)
	if err != nil {
		t.Fatalf("Related: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(edges))
	}
	if edges[0].From != "users/1" || edges[0].Type != "knows" || edges[0].To != "users/2" {
		t.Errorf("edge = %+v", edges[0])
	}
}

func TestRelatedFiltersByType(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	mustRelate(t, s, ctx, "users", "1", "knows", "users", "2", nil)
	mustRelate(t, s, ctx, "users", "1", "blocks", "users", "3", nil)

	edges, err := s.Related(ctx, "users", "1", "knows", 0)
	if err != nil {
		t.Fatalf("Related: %v", err)
	}
	if len(edges) != 1 || edges[0].Type != "knows" {
		t.Fatalf("Related(knows) = %+v, want 1 knows edge", edges)
	}

	edges, err = s.Related(ctx, "users", "1", "", 0)
	if err != nil {
		t.Fatalf("Related: %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("Related(any) = %d edges, want 2", len(edges))
	}
}

func TestRelateWithProps(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	mustRelate(t, s, ctx, "users", "1", "knows", "users", "2", json.RawMessage(`{"since":2020}`))
	edges, err := s.Related(ctx, "users", "1", "", 0)
	if err != nil {
		t.Fatalf("Related: %v", err)
	}
	if string(edges[0].Props) != `{"since":2020}` {
		t.Errorf("Props = %s", edges[0].Props)
	}
}

func TestUnrelateRemovesFromRelated(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	mustRelate(t, s, ctx, "users", "1", "knows", "users", "2", nil)
	must(t, s.Unrelate(ctx, "users", "1", "knows", "users", "2"))

	edges, err := s.Related(ctx, "users", "1", "", 0)
	if err != nil {
		t.Fatalf("Related: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("Related after unrelate = %+v, want empty", edges)
	}
}

func TestRelatedAsOf(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	mustRelate(t, s, ctx, "users", "1", "knows", "users", "2", nil)
	tAfterRelate := time.Now().UTC()

	must(t, s.Unrelate(ctx, "users", "1", "knows", "users", "2"))
	tAfterUnrelate := time.Now().UTC()

	edges, err := s.RelatedAsOf(ctx, "users", "1", "", tAfterRelate, 0)
	if err != nil {
		t.Fatalf("RelatedAsOf(after relate): %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("RelatedAsOf(after relate) = %d edges, want 1", len(edges))
	}

	edges, err = s.RelatedAsOf(ctx, "users", "1", "", tAfterUnrelate, 0)
	if err != nil {
		t.Fatalf("RelatedAsOf(after unrelate): %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("RelatedAsOf(after unrelate) = %+v, want empty", edges)
	}
}

func TestEdgeTypes(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustRelate(t, s, ctx, "users", "1", "knows", "users", "2", nil)
	mustRelate(t, s, ctx, "users", "1", "blocks", "users", "3", nil)
	mustRelate(t, s, ctx, "users", "2", "knows", "users", "3", nil) // duplicate type, must not repeat

	types, err := s.EdgeTypes(ctx)
	if err != nil {
		t.Fatalf("EdgeTypes: %v", err)
	}
	if len(types) != 2 || types[0] != "blocks" || types[1] != "knows" {
		t.Fatalf("EdgeTypes = %v, want [blocks knows] (distinct, sorted)", types)
	}
}

func TestEdgeTypesExcludesUnrelated(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustRelate(t, s, ctx, "users", "1", "knows", "users", "2", nil)
	must(t, s.Unrelate(ctx, "users", "1", "knows", "users", "2"))

	types, err := s.EdgeTypes(ctx)
	if err != nil {
		t.Fatalf("EdgeTypes: %v", err)
	}
	if len(types) != 0 {
		t.Errorf("EdgeTypes after the only edge was unrelated = %v, want empty", types)
	}
}

func TestRelatedLimit(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustRelate(t, s, ctx, "users", "1", "knows", "users", "2", nil)
	mustRelate(t, s, ctx, "users", "1", "knows", "users", "3", nil)
	mustRelate(t, s, ctx, "users", "1", "knows", "users", "4", nil)

	edges, err := s.Related(ctx, "users", "1", "", 2)
	if err != nil {
		t.Fatalf("Related: %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("got %d edges, want 2 (LIMIT)", len(edges))
	}
}
