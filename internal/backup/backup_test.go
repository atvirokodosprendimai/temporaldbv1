package backup

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/atvirokodosprendimai/temporaldbv1/internal/event"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/storage"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/storagetest"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/temporal"
)

func TestFileSinkWriteEventsAndPurge(t *testing.T) {
	dir := t.TempDir()
	sink := NewFileSink(dir)
	ctx := context.Background()

	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	events := []temporal.Event{
		{Seq: 1, Collection: "a", Key: "1", Op: temporal.OpPut, Value: json.RawMessage(`{}`), ValidFrom: old, TxTime: old},
		{Seq: 2, Collection: "a", Key: "2", Op: temporal.OpPut, Value: json.RawMessage(`{}`), ValidFrom: recent, TxTime: recent},
	}
	if err := sink.WriteEvents(ctx, events); err != nil {
		t.Fatalf("WriteEvents: %v", err)
	}

	got, err := readShippedEvents(dir, 0, time.Time{})
	if err != nil {
		t.Fatalf("readShippedEvents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}

	if err := sink.Purge(ctx, recent); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	got, err = readShippedEvents(dir, 0, time.Time{})
	if err != nil {
		t.Fatalf("readShippedEvents after purge: %v", err)
	}
	if len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("after purge = %+v, want just seq 2", got)
	}
}

func TestFileSinkPurgeOnMissingStreamIsNoop(t *testing.T) {
	sink := NewFileSink(t.TempDir())
	if err := sink.Purge(context.Background(), time.Now()); err != nil {
		t.Errorf("Purge on a sink with nothing shipped yet: %v, want nil", err)
	}
}

func TestSnapshotWritesToSink(t *testing.T) {
	db := storagetest.DB(t)
	t.Cleanup(func() { storagetest.Reset(t, db) })
	es := event.NewStore(db)
	if _, err := es.Append(context.Background(), "a", "1", temporal.OpPut, json.RawMessage(`{}`), nil, time.Time{}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	sinkDir := t.TempDir()
	sink := NewFileSink(sinkDir)
	snap := NewSnapshotter(db, t.TempDir(), sink)

	seq, err := snap.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if seq != 1 {
		t.Errorf("Snapshot seq = %d, want 1", seq)
	}

	snaps, err := listSnapshots(sinkDir)
	if err != nil {
		t.Fatalf("listSnapshots: %v", err)
	}
	if len(snaps) != 1 || snaps[0].seq != 1 {
		t.Fatalf("listSnapshots = %+v, want one snapshot at seq 1", snaps)
	}
}

func TestShipperShipsNewEventsOnlyOnce(t *testing.T) {
	db := storagetest.DB(t)
	t.Cleanup(func() { storagetest.Reset(t, db) })
	es := event.NewStore(db)
	ctx := context.Background()

	e1, err := es.Append(ctx, "a", "1", temporal.OpPut, json.RawMessage(`{}`), nil, time.Time{})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	sinkDir := t.TempDir()
	shipper := NewShipper(es, NewFileSink(sinkDir), time.Hour, 0)

	if err := shipper.shipOnce(ctx); err != nil {
		t.Fatalf("shipOnce: %v", err)
	}
	if shipper.LastSeq() != e1.Seq {
		t.Errorf("LastSeq = %d, want %d", shipper.LastSeq(), e1.Seq)
	}

	got, err := readShippedEvents(sinkDir, 0, time.Time{})
	if err != nil {
		t.Fatalf("readShippedEvents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}

	// A second ship with no new commits must be a no-op, not a duplicate.
	if err := shipper.shipOnce(ctx); err != nil {
		t.Fatalf("shipOnce (no new events): %v", err)
	}
	got, err = readShippedEvents(sinkDir, 0, time.Time{})
	if err != nil {
		t.Fatalf("readShippedEvents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("after a no-op ship, got %d events, want still 1", len(got))
	}
}

// TestRestoreRoundTripPreservesTemporalHistory is the test that actually
// matters for this package: a restored database must answer AS-OF queries
// identically to the original, which only holds if RestoreEvent (not
// Append) was used to replay history — Append always stamps "now".
func TestRestoreRoundTripPreservesTemporalHistory(t *testing.T) {
	// A real on-disk database, not storagetest's shared in-memory one:
	// Snapshotter's VACUUM INTO and Restore's file copy both need real
	// files on disk.
	srcDir := filepath.Join(t.TempDir(), "src")
	db, err := storage.Open(srcDir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()
	es := event.NewStore(db)
	ctx := context.Background()

	sinkDir := t.TempDir()
	sink := NewFileSink(sinkDir)
	shipper := NewShipper(es, sink, time.Hour, 0)

	// t1 sets ValidFrom (business time) only — Append always stamps
	// TxTime as real wall-clock "now" by design (ADR-001 D3), so the
	// TxTime-preservation assertion below compares against origV1.TxTime
	// (whatever that real value actually was), never against t1.
	t1 := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	origV1, err := es.Append(ctx, "users", "1", temporal.OpPut, json.RawMessage(`{"v":1}`), nil, t1)
	if err != nil {
		t.Fatalf("Append v1: %v", err)
	}
	if err := shipper.shipOnce(ctx); err != nil {
		t.Fatalf("ship 1: %v", err)
	}

	snap := NewSnapshotter(db, t.TempDir(), sink)
	if _, err := snap.Snapshot(ctx); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	t2 := time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)
	if _, err := es.Append(ctx, "users", "1", temporal.OpPut, json.RawMessage(`{"v":2}`), nil, t2); err != nil {
		t.Fatalf("Append v2: %v", err)
	}
	if _, err := es.Append(ctx, "users", "2", temporal.OpPut, json.RawMessage(`{"other":true}`), nil, time.Time{}); err != nil {
		t.Fatalf("Append users/2: %v", err)
	}
	if err := shipper.shipOnce(ctx); err != nil {
		t.Fatalf("ship 2: %v", err)
	}

	destDir := filepath.Join(t.TempDir(), "restored")
	if err := Restore(ctx, sinkDir, destDir, time.Time{}); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	restoredDB, err := storage.Open(destDir)
	if err != nil {
		t.Fatalf("open restored db: %v", err)
	}
	defer restoredDB.Close()
	restoredEvents := event.NewStore(restoredDB)

	got, err := restoredEvents.Get(ctx, "users", "1")
	if err != nil {
		t.Fatalf("Get users/1 on restored db: %v", err)
	}
	if got == nil || string(got.Value) != `{"v":2}` {
		t.Fatalf("restored users/1 = %v, want v2 (current state)", got)
	}

	asOf, err := restoredEvents.AsOf(ctx, "users", "1", time.Time{}, t1)
	if err != nil {
		t.Fatalf("AsOf(t1) on restored db: %v", err)
	}
	if asOf == nil || string(asOf.Value) != `{"v":1}` {
		t.Fatalf("restored AsOf(t1) = %v, want v1 - history must survive restore", asOf)
	}

	hist, err := restoredEvents.History(ctx, "users", "1")
	if err != nil {
		t.Fatalf("History on restored db: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("restored History = %d entries, want 2 (full history, not just current)", len(hist))
	}
	if !hist[0].TxTime.Equal(origV1.TxTime) {
		t.Errorf("restored hist[0].TxTime = %v, want %v exactly (RestoreEvent must preserve the original commit time)", hist[0].TxTime, origV1.TxTime)
	}

	// A normal Append on the restored database must continue past every
	// restored seq without colliding.
	next, err := restoredEvents.Append(ctx, "users", "3", temporal.OpPut, json.RawMessage(`{}`), nil, time.Time{})
	if err != nil {
		t.Fatalf("Append on restored db: %v", err)
	}
	if next.Seq <= hist[len(hist)-1].Seq {
		t.Errorf("post-restore Append seq %d did not continue past restored history (last restored seq %d)",
			next.Seq, hist[len(hist)-1].Seq)
	}
}

func TestRestoreRefusesExistingDatabase(t *testing.T) {
	destDir := t.TempDir()
	db, err := storage.Open(destDir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	db.Close()

	if err := Restore(context.Background(), t.TempDir(), destDir, time.Time{}); err == nil {
		t.Error("Restore into an existing database: want error, got nil")
	}
}

func TestRestoreWithNoBackupsYieldsEmptyMigratedDatabase(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), "restored")
	if err := Restore(context.Background(), t.TempDir(), destDir, time.Time{}); err != nil {
		t.Fatalf("Restore with no backups: %v", err)
	}

	db, err := storage.Open(destDir)
	if err != nil {
		t.Fatalf("open restored db: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 0 {
		t.Errorf("events count = %d, want 0", count)
	}
}
