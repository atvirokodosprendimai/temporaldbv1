// Package backup implements TemporalDB's litestream-like continuous
// replication (ADR-001 D7): a snapshot (SQLite's VACUUM INTO) as the
// periodic restorable anchor, plus a Shipper that tails newly committed
// events to a pluggable Sink. Nothing here parses SQLite's WAL format —
// see ADR-001's Alternatives Considered for why that was rejected for a
// from-scratch build.
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/atvirokodosprendimai/temporaldbv1/internal/temporal"
)

// Sink is a backup destination for the streaming shipper and periodic
// snapshots. It is an interface specifically so an object-storage sink is
// a later implementation of the same interface, not a redesign (ADR-001
// D7); FileSink below is the only implementation this repository ships.
type Sink interface {
	// WriteEvents appends already-committed events, in order. Called
	// repeatedly as new events commit.
	WriteEvents(ctx context.Context, events []temporal.Event) error
	// WriteSnapshot stores a full consistent database snapshot (a file
	// produced by SQLite's VACUUM INTO) taken at the given high-water seq.
	WriteSnapshot(ctx context.Context, seq int64, snapshotPath string) error
	// Purge removes shipped event records strictly before cutoff,
	// mirroring PURGE's effect on the primary events table (ADR-001 D7) —
	// this is the "purge option" on the streaming backup the ask names.
	Purge(ctx context.Context, cutoff time.Time) error
}

// shippedEvent is the on-disk (and wire, within this package) shape of one
// event in FileSink's newline-delimited-JSON stream.
type shippedEvent struct {
	Seq        int64           `json:"seq"`
	Collection string          `json:"collection"`
	Key        string          `json:"key"`
	Op         string          `json:"op"`
	Value      json.RawMessage `json:"value,omitempty"`
	ValidFrom  time.Time       `json:"valid_from"`
	TxTime     time.Time       `json:"tx_time"`
	Meta       json.RawMessage `json:"meta,omitempty"`
}

func toShipped(e temporal.Event) shippedEvent {
	return shippedEvent{
		Seq: e.Seq, Collection: e.Collection, Key: e.Key, Op: string(e.Op),
		Value: e.Value, ValidFrom: e.ValidFrom, TxTime: e.TxTime, Meta: e.Meta,
	}
}

func (s shippedEvent) toEvent() temporal.Event {
	return temporal.Event{
		Seq: s.Seq, Collection: s.Collection, Key: s.Key, Op: temporal.Op(s.Op),
		Value: s.Value, ValidFrom: s.ValidFrom, TxTime: s.TxTime, Meta: s.Meta,
	}
}

// FileSink is a Sink backed by the local filesystem: an append-only
// newline-delimited-JSON event stream (events.ndjson) plus a snapshots/
// subdirectory. Restore (restore.go) is implemented against this concrete
// type, not the Sink interface — reading a backup back requires knowing
// its storage format, which a write-only interface deliberately doesn't
// expose.
type FileSink struct {
	dir string
	mu  sync.Mutex
}

// NewFileSink wraps a directory, created on first write if it does not
// exist.
func NewFileSink(dir string) *FileSink {
	return &FileSink{dir: dir}
}

func (f *FileSink) eventsPath() string   { return filepath.Join(f.dir, "events.ndjson") }
func (f *FileSink) snapshotsDir() string { return filepath.Join(f.dir, "snapshots") }

// WriteEvents appends events to events.ndjson, one JSON object per line.
func (f *FileSink) WriteEvents(_ context.Context, events []temporal.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		return fmt.Errorf("backup: filesink: mkdir: %w", err)
	}
	file, err := os.OpenFile(f.eventsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("backup: filesink: open events stream: %w", err)
	}
	defer file.Close()

	enc := json.NewEncoder(file)
	for _, e := range events {
		if err := enc.Encode(toShipped(e)); err != nil {
			return fmt.Errorf("backup: filesink: write event seq %d: %w", e.Seq, err)
		}
	}
	return nil
}

// WriteSnapshot copies the file at snapshotPath into this sink's
// snapshots/ directory, named by its high-water seq so restore.go can
// find the latest one without opening every file.
func (f *FileSink) WriteSnapshot(_ context.Context, seq int64, snapshotPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := os.MkdirAll(f.snapshotsDir(), 0o755); err != nil {
		return fmt.Errorf("backup: filesink: mkdir snapshots: %w", err)
	}
	dest := filepath.Join(f.snapshotsDir(), filepath.Base(snapshotPath))
	if err := copyFile(snapshotPath, dest); err != nil {
		return fmt.Errorf("backup: filesink: copy snapshot: %w", err)
	}
	return nil
}

// Purge rewrites events.ndjson keeping only events at or after cutoff
// (read-filter-rewrite-then-atomic-rename, since it is an append-only
// stream with no in-place delete). Snapshot files are left alone —
// pruning them by age is a coarser, separate operator concern (they are
// few and cheap to keep compared to the event stream) and is not
// implemented here; see docs/adr/BACKLOG.md.
func (f *FileSink) Purge(_ context.Context, cutoff time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	path := f.eventsPath()
	in, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil // nothing shipped yet
	}
	if err != nil {
		return fmt.Errorf("backup: filesink: purge: open: %w", err)
	}
	defer in.Close()

	tmp, err := os.CreateTemp(f.dir, "events-*.ndjson.tmp")
	if err != nil {
		return fmt.Errorf("backup: filesink: purge: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed over the original

	dec := json.NewDecoder(in)
	enc := json.NewEncoder(tmp)
	for {
		var e shippedEvent
		if err := dec.Decode(&e); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			tmp.Close()
			return fmt.Errorf("backup: filesink: purge: decode: %w", err)
		}
		if e.TxTime.Before(cutoff) {
			continue
		}
		if err := enc.Encode(e); err != nil {
			tmp.Close()
			return fmt.Errorf("backup: filesink: purge: write: %w", err)
		}
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("backup: filesink: purge: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("backup: filesink: purge: rename: %w", err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
