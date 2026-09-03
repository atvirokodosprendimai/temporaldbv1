// Package storage owns the on-disk SQLite database: connection setup, WAL
// pragmas, and schema migrations. It is pure Go and needs no C toolchain
// (modernc.org/sqlite, ADR-001 D1).
package storage

import (
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens (creating if absent) the SQLite database file under dataDir,
// applies the WAL pragmas single-writer/many-reader concurrency needs
// (ADR-001 D1/D13), and applies any pending schema migrations.
func Open(dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("storage: create data dir %s: %w", dataDir, err)
	}
	return open(filepath.Join(dataDir, "temporaldb.sqlite"))
}

// OpenMemory opens a private in-memory database. Intended for tests; a
// single connection is enforced (see open), so the usual multi-connection
// :memory: pitfall (each connection seeing an empty database) does not
// apply here.
func OpenMemory() (*sql.DB, error) {
	return open(":memory:")
}

func open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)",
		path,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}
	// Single writer (ADR-001 D13): WAL already gives readers concurrency
	// without more connections, and more than one writer connection fights
	// WAL rather than helping it.
	db.SetMaxOpenConns(1)

	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	goose.SetBaseFS(migrationsFS)
	defer goose.SetBaseFS(nil)

	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("storage: set goose dialect: %w", err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		return fmt.Errorf("storage: migrate: %w", err)
	}
	return nil
}
