package storage

import (
	"context"
	"testing"
	"time"
)

func TestOpenMemoryAppliesSchema(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	for _, table := range []string{"events", "live"} {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
}

func TestOpenMemoryIsSingleConnection(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1 - a bare :memory: DSN gives each connection its own private database", got)
	}
}

func TestOpenCreatesDataDir(t *testing.T) {
	dir := t.TempDir() + "/nested/data"
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()

	db1, err := Open(dir)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	db1.Close()

	// Re-opening the same data dir must not fail on already-applied
	// migrations.
	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db2.Close()
}

func TestOpenFileBackedRaisesConnectionCap(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if got := db.Stats().MaxOpenConnections; got != maxFileConns {
		t.Errorf("MaxOpenConnections = %d, want %d (file-backed: WAL supports concurrent readers, ADR-001 D13)", got, maxFileConns)
	}
}

// TestOpenFileBackedAllowsConcurrentConnections is a regression test for
// the actual bug (not just the reported pool-size stat): every path used
// to cap MaxOpenConns at 1, which serialized ALL callers - reads included
// - behind whichever one held the sole connection, defeating WAL's
// concurrent-reader design. Holding one connection open and acquiring a
// second concurrently would block (and time out) against the old cap;
// found via independent review.
func TestOpenFileBackedAllowsConcurrentConnections(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	c1, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("first Conn: %v", err)
	}
	defer c1.Close()

	ctx2, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c2, err := db.Conn(ctx2)
	if err != nil {
		t.Fatalf("second Conn while the first is held open: %v (the pool is serializing connections)", err)
	}
	defer c2.Close()
}
