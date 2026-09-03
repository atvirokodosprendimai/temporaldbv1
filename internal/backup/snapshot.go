package backup

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// snapshotTimeLayout is used only in filenames (informational — restore.go
// parses it back out to pick "the latest snapshot at or before a target
// time"). Colon-free because it must be a valid filename component.
const snapshotTimeLayout = "20060102T150405Z"

// Snapshotter takes periodic consistent snapshots via SQLite's VACUUM
// INTO — plain SQL, so it works through modernc.org/sqlite with no
// special backup API (ADR-001 D7).
type Snapshotter struct {
	db     *sql.DB
	tmpDir string
	sink   Sink
}

// NewSnapshotter builds a Snapshotter. tmpDir is where VACUUM INTO writes
// the snapshot file before it is handed to sink (which may copy it
// somewhere else entirely, e.g. FileSink's snapshots/ subdirectory) —
// kept separate from sink's own storage so a partially-written VACUUM
// target is never mistaken for a finished backup.
func NewSnapshotter(db *sql.DB, tmpDir string, sink Sink) *Snapshotter {
	return &Snapshotter{db: db, tmpDir: tmpDir, sink: sink}
}

// Snapshot takes one consistent snapshot, named by its high-water seq and
// the wall-clock time it was taken, hands it to the sink, and returns the
// seq it was taken at.
func (s *Snapshotter) Snapshot(ctx context.Context) (seq int64, err error) {
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("backup: snapshot: read high-water seq: %w", err)
	}

	if err := os.MkdirAll(s.tmpDir, 0o755); err != nil {
		return 0, fmt.Errorf("backup: snapshot: create tmp dir: %w", err)
	}
	name := fmt.Sprintf("snapshot-%020d-%s.sqlite", seq, time.Now().UTC().Format(snapshotTimeLayout))
	path := filepath.Join(s.tmpDir, name)
	_ = os.Remove(path) // VACUUM INTO refuses to overwrite an existing file

	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return 0, fmt.Errorf("backup: snapshot: vacuum into %s: %w", path, err)
	}
	defer os.Remove(path)

	if err := s.sink.WriteSnapshot(ctx, seq, path); err != nil {
		return 0, fmt.Errorf("backup: snapshot: write to sink: %w", err)
	}
	return seq, nil
}
