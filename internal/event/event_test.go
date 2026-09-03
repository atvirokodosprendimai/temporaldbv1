package event

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/atvirokodosprendimai/temporaldbv1/internal/storagetest"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/temporal"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	db := storagetest.DB(t)
	t.Cleanup(func() { storagetest.Reset(t, db) })
	return NewStore(db)
}

func TestAppendAndGet(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	_, err := s.Append(ctx, "users", "1", temporal.OpPut, json.RawMessage(`{"name":"Ada"}`), nil, time.Time{})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := s.Get(ctx, "users", "1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil, want the put value")
	}
	if string(got.Value) != `{"name":"Ada"}` {
		t.Errorf("Value = %s, want {\"name\":\"Ada\"}", got.Value)
	}
}

func TestAppendDeleteThenGet(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if _, err := s.Append(ctx, "users", "1", temporal.OpPut, json.RawMessage(`{}`), nil, time.Time{}); err != nil {
		t.Fatalf("Append put: %v", err)
	}
	if _, err := s.Append(ctx, "users", "1", temporal.OpDelete, nil, nil, time.Time{}); err != nil {
		t.Fatalf("Append delete: %v", err)
	}

	got, err := s.Get(ctx, "users", "1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Errorf("Get after delete = %+v, want nil", got)
	}
}

func TestGetPreservesMeta(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	_, err := s.Append(ctx, "users", "1", temporal.OpPut, json.RawMessage(`{}`), json.RawMessage(`{"from":"a","type":"knows","to":"b"}`), time.Time{})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := s.Get(ctx, "users", "1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if string(got.Meta) != `{"from":"a","type":"knows","to":"b"}` {
		t.Errorf("Meta = %s, want the meta passed to Append (live must carry meta, not just events)", got.Meta)
	}
}

func TestGetMissingKey(t *testing.T) {
	s := newStore(t)
	got, err := s.Get(context.Background(), "users", "does-not-exist")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Errorf("Get(missing) = %+v, want nil", got)
	}
}

func TestAppendRejectsInvalidOp(t *testing.T) {
	s := newStore(t)
	_, err := s.Append(context.Background(), "users", "1", temporal.Op("bogus"), nil, nil, time.Time{})
	if err == nil {
		t.Error("Append(invalid op) = nil error, want an error")
	}
}

func TestAppendRequiresCollectionAndKey(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.Append(ctx, "", "1", temporal.OpPut, nil, nil, time.Time{}); err == nil {
		t.Error("Append(empty collection) = nil error, want an error")
	}
	if _, err := s.Append(ctx, "users", "", temporal.OpPut, nil, nil, time.Time{}); err == nil {
		t.Error("Append(empty key) = nil error, want an error")
	}
}

func TestHistoryOrdersBySeq(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		v := fmt.Sprintf(`{"n":%d}`, i)
		if _, err := s.Append(ctx, "users", "1", temporal.OpPut, json.RawMessage(v), nil, time.Time{}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	hist, err := s.History(ctx, "users", "1")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != 3 {
		t.Fatalf("History returned %d events, want 3", len(hist))
	}
	for i, e := range hist {
		want := fmt.Sprintf(`{"n":%d}`, i)
		if string(e.Value) != want {
			t.Errorf("hist[%d].Value = %s, want %s", i, e.Value, want)
		}
		if e.Seq <= 0 {
			t.Errorf("hist[%d].Seq = %d, want > 0", i, e.Seq)
		}
	}
}

func TestAsOfReplaysCorrectly(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	if _, err := s.Append(ctx, "users", "1", temporal.OpPut, json.RawMessage(`{"v":1}`), nil, t1); err != nil {
		t.Fatalf("Append v1: %v", err)
	}
	if _, err := s.Append(ctx, "users", "1", temporal.OpPut, json.RawMessage(`{"v":2}`), nil, t2); err != nil {
		t.Fatalf("Append v2: %v", err)
	}

	got, err := s.AsOf(ctx, "users", "1", time.Time{}, t1)
	if err != nil {
		t.Fatalf("AsOf(t1): %v", err)
	}
	if got == nil || string(got.Value) != `{"v":1}` {
		t.Errorf("AsOf(t1) = %v, want {\"v\":1}", got)
	}

	got, err = s.AsOf(ctx, "users", "1", time.Time{}, t2)
	if err != nil {
		t.Fatalf("AsOf(t2): %v", err)
	}
	if got == nil || string(got.Value) != `{"v":2}` {
		t.Errorf("AsOf(t2) = %v, want {\"v\":2}", got)
	}

	got, err = s.AsOf(ctx, "users", "1", time.Time{}, t1.Add(-time.Hour))
	if err != nil {
		t.Fatalf("AsOf(before t1): %v", err)
	}
	if got != nil {
		t.Errorf("AsOf(before t1) = %v, want nil", got)
	}
}

// TestAppendConcurrentDistinctKeys exercises the single-writer mutex under
// real concurrency: many goroutines appending to distinct keys must all
// succeed, get unique sequence numbers, and leave every key readable.
// Run with -race to verify there is no data race on the shared *sql.DB.
func TestAppendConcurrentDistinctKeys(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	const n = 20

	var wg sync.WaitGroup
	seqs := make([]int64, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("k%d", i)
			e, err := s.Append(ctx, "users", key, temporal.OpPut, json.RawMessage(`{}`), nil, time.Time{})
			seqs[i], errs[i] = e.Seq, err
		}(i)
	}
	wg.Wait()

	seen := make(map[int64]bool, n)
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("Append(k%d): %v", i, errs[i])
		}
		if seen[seqs[i]] {
			t.Fatalf("duplicate seq %d", seqs[i])
		}
		seen[seqs[i]] = true

		got, err := s.Get(ctx, "users", fmt.Sprintf("k%d", i))
		if err != nil || got == nil {
			t.Fatalf("Get(k%d) = %v, %v", i, got, err)
		}
	}
}

func TestReplayAsOfExcludesDeletedAndPicksVisibleVersion(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	if _, err := s.Append(ctx, "users", "1", temporal.OpPut, json.RawMessage(`{"v":1}`), nil, t1); err != nil {
		t.Fatalf("Append users/1 v1: %v", err)
	}
	if _, err := s.Append(ctx, "users", "1", temporal.OpPut, json.RawMessage(`{"v":2}`), nil, t2); err != nil {
		t.Fatalf("Append users/1 v2: %v", err)
	}
	if _, err := s.Append(ctx, "users", "2", temporal.OpPut, json.RawMessage(`{"v":1}`), nil, t1); err != nil {
		t.Fatalf("Append users/2: %v", err)
	}
	if _, err := s.Append(ctx, "users", "2", temporal.OpDelete, nil, nil, t2); err != nil {
		t.Fatalf("Append users/2 delete: %v", err)
	}

	got, err := s.ReplayAsOf(ctx, "users", time.Time{}, t2)
	if err != nil {
		t.Fatalf("ReplayAsOf: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ReplayAsOf returned %d events, want 1 (users/2 deleted, users/1 visible)", len(got))
	}
	if got[0].Key != "1" || string(got[0].Value) != `{"v":2}` {
		t.Errorf("got %+v, want key=1 value={\"v\":2}", got[0])
	}

	got, err = s.ReplayAsOf(ctx, "users", time.Time{}, t1)
	if err != nil {
		t.Fatalf("ReplayAsOf(t1): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ReplayAsOf(t1) returned %d events, want 2 (both keys existed then)", len(got))
	}
}
