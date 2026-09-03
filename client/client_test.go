package client

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/atvirokodosprendimai/temporaldbv1/internal/event"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/graph"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/server"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/storagetest"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/tql"
)

// newTestClient wires the real server (and everything under it) and
// returns a Client pointed at an httptest server — an end-to-end test of
// the actual wire protocol, not a mock.
func newTestClient(t *testing.T) *Client {
	t.Helper()
	db := storagetest.DB(t)
	t.Cleanup(func() { storagetest.Reset(t, db) })
	es := event.NewStore(db)
	ex := tql.NewExecutor(db, es, graph.NewStore(es, db), nil)
	ts := httptest.NewServer(server.New(ex))
	t.Cleanup(ts.Close)
	return New(ts.URL)
}

// mustPut Puts and fails the test on error - a value-returning call like
// Put cannot be wrapped by a generic must(t, val, err) helper alongside a
// separate t argument (Go's multi-value-call special case only applies
// when the multi-valued call is the function's SOLE argument).
func mustPut(t *testing.T, c *Client, ctx context.Context, collection, key string, value json.RawMessage) {
	t.Helper()
	if _, err := c.Put(ctx, collection, key, value); err != nil {
		t.Fatalf("Put(%s/%s): %v", collection, key, err)
	}
}

func TestClientPutGet(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	if _, err := c.Put(ctx, "users", "1", json.RawMessage(`{"name":"Ada"}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := c.Get(ctx, "users", "1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || string(got.Value) != `{"name":"Ada"}` {
		t.Fatalf("Get = %v", got)
	}
}

func TestClientGetMissing(t *testing.T) {
	c := newTestClient(t)
	got, err := c.Get(context.Background(), "users", "nope")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Errorf("Get(missing) = %+v, want nil", got)
	}
}

func TestClientDelete(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	mustPut(t, c, ctx, "users", "1", json.RawMessage(`{}`))
	if err := c.Delete(ctx, "users", "1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := c.Get(ctx, "users", "1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Errorf("Get after Delete = %+v, want nil", got)
	}
}

func TestClientFind(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	mustPut(t, c, ctx, "users", "1", json.RawMessage(`{"age":30}`))
	mustPut(t, c, ctx, "users", "2", json.RawMessage(`{"age":20}`))

	rows, err := c.Find(ctx, "users", `WHERE age > 25`)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(rows) != 1 || rows[0].Key != "1" {
		t.Fatalf("Find = %+v", rows)
	}
}

func TestClientHistory(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	mustPut(t, c, ctx, "users", "1", json.RawMessage(`{"v":1}`))
	mustPut(t, c, ctx, "users", "1", json.RawMessage(`{"v":2}`))

	hist, err := c.History(ctx, "users", "1")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("History = %d entries, want 2", len(hist))
	}
}

// TestClientHyphenatedKeyRoundTrips exercises quoteKeyIfNeeded end to end:
// a UUID key must survive Put -> TQL text -> parse -> execute -> Get.
func TestClientHyphenatedKeyRoundTrips(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	key := "550e8400-e29b-41d4-a716-446655440000"

	if _, err := c.Put(ctx, "users", key, json.RawMessage(`{"a":1}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := c.Get(ctx, "users", key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.Key != key {
		t.Fatalf("Get = %v, want key %q", got, key)
	}
}

func TestClientQueryReportsParseError(t *testing.T) {
	c := newTestClient(t)
	qr, err := c.Query(context.Background(), "BOGUS x")
	if err != nil {
		t.Fatalf("Query (transport-level error): %v", err)
	}
	if qr.Error == "" {
		t.Error("Error = empty, want a parse error message")
	}
}

func TestClientSearchNotConfigured(t *testing.T) {
	c := newTestClient(t) // wires no Searcher
	_, err := c.Search(context.Background(), "docs", "hello", "", 0)
	if err == nil {
		t.Error("Search with no vector backend configured: want error, got nil")
	}
}

// fakeSearcher stands in for internal/vector's index (T10, not built
// yet) so Search's TQL construction (quoting, WHERE, LIMIT) can be
// verified end to end without a real Qdrant/TEI instance.
type fakeSearcher struct{ keys []string }

func (f fakeSearcher) Search(_ context.Context, _, _ string, limit int) ([]string, error) {
	keys := f.keys
	if limit > 0 && limit < len(keys) {
		keys = keys[:limit]
	}
	return keys, nil
}

func TestClientSearchWithWhereAndLimit(t *testing.T) {
	db := storagetest.DB(t)
	t.Cleanup(func() { storagetest.Reset(t, db) })
	es := event.NewStore(db)
	ex := tql.NewExecutor(db, es, graph.NewStore(es, db), fakeSearcher{keys: []string{"1", "2"}})
	ts := httptest.NewServer(server.New(ex))
	t.Cleanup(ts.Close)
	c := New(ts.URL)

	ctx := context.Background()
	mustPut(t, c, ctx, "docs", "1", json.RawMessage(`{"lang":"en"}`))
	mustPut(t, c, ctx, "docs", "2", json.RawMessage(`{"lang":"lt"}`))

	rows, err := c.Search(ctx, "docs", `a "quoted" query`, `lang = "en"`, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(rows) != 1 || rows[0].Key != "1" {
		t.Fatalf("Search = %+v, want just docs/1 (WHERE lang=en excludes docs/2)", rows)
	}
}
