// Package storagetest gives other internal packages' tests a shared,
// already-migrated in-memory database, so a package with many test
// functions pays SQLite's migration cost once per test binary rather than
// once per test (M, 2026-09-03: "reuse database in tests, so it wont
// recreate in 100 tests").
package storagetest

import (
	"database/sql"
	"sync"
	"testing"

	"github.com/atvirokodosprendimai/temporaldbv1/internal/storage"
)

var (
	once   sync.Once
	db     *sql.DB
	openErr error
)

// DB returns the shared, migrated in-memory database for this test binary.
// It is created on first use and reused by every later call; callers must
// not close it. Pair with Reset in a t.Cleanup so each test starts from an
// empty store without re-running migrations.
func DB(tb testing.TB) *sql.DB {
	tb.Helper()
	once.Do(func() {
		db, openErr = storage.OpenMemory()
	})
	if openErr != nil {
		tb.Fatalf("storagetest: open shared db: %v", openErr)
	}
	return db
}

// Reset deletes every row from the tables tests can write to, leaving the
// schema (and the shared connection) intact.
func Reset(tb testing.TB, db *sql.DB) {
	tb.Helper()
	for _, table := range []string{"events", "live"} {
		if _, err := db.Exec("DELETE FROM " + table); err != nil {
			tb.Fatalf("storagetest: reset %s: %v", table, err)
		}
	}
}
