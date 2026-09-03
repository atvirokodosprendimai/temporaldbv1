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

	edges, err := s.Related(ctx, "users", "1", nil, DirOut, 0, 0)
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

	edges, err := s.Related(ctx, "users", "1", []string{"knows"}, DirOut, 0, 0)
	if err != nil {
		t.Fatalf("Related: %v", err)
	}
	if len(edges) != 1 || edges[0].Type != "knows" {
		t.Fatalf("Related(knows) = %+v, want 1 knows edge", edges)
	}

	edges, err = s.Related(ctx, "users", "1", nil, DirOut, 0, 0)
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
	edges, err := s.Related(ctx, "users", "1", nil, DirOut, 0, 0)
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

	edges, err := s.Related(ctx, "users", "1", nil, DirOut, 0, 0)
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

	edges, err := s.RelatedAsOf(ctx, "users", "1", nil, DirOut, tAfterRelate, 0, 0)
	if err != nil {
		t.Fatalf("RelatedAsOf(after relate): %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("RelatedAsOf(after relate) = %d edges, want 1", len(edges))
	}

	edges, err = s.RelatedAsOf(ctx, "users", "1", nil, DirOut, tAfterUnrelate, 0, 0)
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

	edges, err := s.Related(ctx, "users", "1", nil, DirOut, 2, 0)
	if err != nil {
		t.Fatalf("Related: %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("got %d edges, want 2 (LIMIT)", len(edges))
	}
}

func TestRelatedDirectionIn(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustRelate(t, s, ctx, "users", "1", "knows", "users", "2", nil)

	out, err := s.Related(ctx, "users", "2", nil, DirOut, 0, 0)
	if err != nil {
		t.Fatalf("Related(out): %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("Related(users/2, DirOut) = %+v, want empty (edge points INTO users/2)", out)
	}

	in, err := s.Related(ctx, "users", "2", nil, DirIn, 0, 0)
	if err != nil {
		t.Fatalf("Related(in): %v", err)
	}
	if len(in) != 1 || in[0].From != "users/1" {
		t.Fatalf("Related(users/2, DirIn) = %+v, want the users/1->users/2 edge", in)
	}
}

func TestRelatedDirectionBoth(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustRelate(t, s, ctx, "users", "1", "knows", "users", "2", nil) // 1 -> 2
	mustRelate(t, s, ctx, "users", "3", "knows", "users", "1", nil) // 3 -> 1

	both, err := s.Related(ctx, "users", "1", nil, DirBoth, 0, 0)
	if err != nil {
		t.Fatalf("Related(both): %v", err)
	}
	if len(both) != 2 {
		t.Fatalf("Related(users/1, DirBoth) = %d edges, want 2 (one out, one in)", len(both))
	}
}

func TestRelatedMultiType(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustRelate(t, s, ctx, "users", "1", "knows", "users", "2", nil)
	mustRelate(t, s, ctx, "users", "1", "blocks", "users", "3", nil)
	mustRelate(t, s, ctx, "users", "1", "parents", "users", "4", nil)

	edges, err := s.Related(ctx, "users", "1", []string{"knows", "parents"}, DirOut, 0, 0)
	if err != nil {
		t.Fatalf("Related: %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("Related(knows,parents) = %d edges, want 2", len(edges))
	}
	for _, e := range edges {
		if e.Type != "knows" && e.Type != "parents" {
			t.Errorf("unexpected edge type %q, blocks should have been excluded", e.Type)
		}
	}
}

func TestRelatedOffset(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustRelate(t, s, ctx, "users", "1", "knows", "users", "2", nil)
	mustRelate(t, s, ctx, "users", "1", "knows", "users", "3", nil)
	mustRelate(t, s, ctx, "users", "1", "knows", "users", "4", nil)

	edges, err := s.Related(ctx, "users", "1", nil, DirOut, 0, 1)
	if err != nil {
		t.Fatalf("Related: %v", err)
	}
	if len(edges) != 2 || edges[0].To != "users/3" || edges[1].To != "users/4" {
		t.Fatalf("Related(offset=1) = %+v, want [users/3 users/4] (seq order, first skipped)", edges)
	}
}

func TestRelatedLimitAndOffset(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustRelate(t, s, ctx, "users", "1", "knows", "users", "2", nil)
	mustRelate(t, s, ctx, "users", "1", "knows", "users", "3", nil)
	mustRelate(t, s, ctx, "users", "1", "knows", "users", "4", nil)

	edges, err := s.Related(ctx, "users", "1", nil, DirOut, 1, 1)
	if err != nil {
		t.Fatalf("Related: %v", err)
	}
	if len(edges) != 1 || edges[0].To != "users/3" {
		t.Fatalf("Related(limit=1,offset=1) = %+v, want [users/3]", edges)
	}
}

func TestRelatedAsOfDirectionTypeOffset(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustRelate(t, s, ctx, "users", "1", "knows", "users", "2", nil)
	mustRelate(t, s, ctx, "users", "3", "blocks", "users", "1", nil)
	tAfter := time.Now().UTC()

	in, err := s.RelatedAsOf(ctx, "users", "1", nil, DirIn, tAfter, 0, 0)
	if err != nil {
		t.Fatalf("RelatedAsOf(in): %v", err)
	}
	if len(in) != 1 || in[0].From != "users/3" {
		t.Fatalf("RelatedAsOf(users/1, DirIn) = %+v, want the users/3->users/1 edge", in)
	}

	typed, err := s.RelatedAsOf(ctx, "users", "1", []string{"knows"}, DirBoth, tAfter, 0, 0)
	if err != nil {
		t.Fatalf("RelatedAsOf(typed): %v", err)
	}
	if len(typed) != 1 || typed[0].Type != "knows" {
		t.Fatalf("RelatedAsOf(knows, DirBoth) = %+v, want just the knows edge", typed)
	}
}
