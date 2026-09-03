package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/atvirokodosprendimai/temporaldbv1/internal/event"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/storage"
)

var snapshotNameRe = regexp.MustCompile(`^snapshot-(\d{20})-(\w+)\.sqlite$`)

type snapshotInfo struct {
	path string
	seq  int64
	at   time.Time
}

// listSnapshots returns every snapshot in sinkDir's snapshots/
// subdirectory, oldest first. A filename this package didn't write is
// skipped, not an error — a snapshots/ directory only ever holds what
// Snapshotter put there.
func listSnapshots(sinkDir string) ([]snapshotInfo, error) {
	entries, err := os.ReadDir(filepath.Join(sinkDir, "snapshots"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("backup: restore: list snapshots: %w", err)
	}

	var out []snapshotInfo
	for _, e := range entries {
		m := snapshotNameRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		seq, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			continue
		}
		at, err := time.Parse(snapshotTimeLayout, m[2])
		if err != nil {
			continue
		}
		out = append(out, snapshotInfo{
			path: filepath.Join(sinkDir, "snapshots", e.Name()),
			seq:  seq,
			at:   at,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].seq < out[j].seq })
	return out, nil
}

// latestSnapshotBefore returns the snapshot with the highest seq among
// those taken at or before target, or the overall latest if target is
// zero. ok is false when none qualifies (including an empty snapshots/
// directory), meaning restore proceeds from an empty database and replays
// the whole shipped event log.
func latestSnapshotBefore(sinkDir string, target time.Time) (snapshotInfo, bool, error) {
	all, err := listSnapshots(sinkDir)
	if err != nil {
		return snapshotInfo{}, false, err
	}
	var best snapshotInfo
	found := false
	for _, s := range all {
		if !target.IsZero() && s.at.After(target) {
			continue
		}
		if !found || s.seq > best.seq {
			best, found = s, true
		}
	}
	return best, found, nil
}

// readShippedEvents reads events.ndjson from sinkDir, returning those with
// Seq > afterSeq and (if target is non-zero) TxTime <= target, in file
// order — which is commit order, since FileSink only ever appends.
func readShippedEvents(sinkDir string, afterSeq int64, target time.Time) ([]shippedEvent, error) {
	f, err := os.Open(filepath.Join(sinkDir, "events.ndjson"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("backup: restore: open event stream: %w", err)
	}
	defer f.Close()

	var out []shippedEvent
	dec := json.NewDecoder(f)
	for {
		var e shippedEvent
		if err := dec.Decode(&e); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("backup: restore: decode event stream: %w", err)
		}
		if e.Seq <= afterSeq {
			continue
		}
		if !target.IsZero() && e.TxTime.After(target) {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// Restore rebuilds a fresh, independent database under destDataDir from
// the backups in sinkDir (a FileSink's directory): the latest snapshot at
// or before target (the overall latest if target is zero; an empty
// database if there are no snapshots at all), then every shipped event
// after that snapshot's seq up to target, replayed with
// event.Store.RestoreEvent so their original commit times are preserved
// exactly — an AS-OF query against the restored database must answer the
// same way it would have against the original. destDataDir must not
// already contain a database.
//
// Implemented against FileSink's on-disk format specifically, not the
// Sink interface — reading a backup back requires knowing its storage
// format, which a write-only interface deliberately doesn't expose.
func Restore(ctx context.Context, sinkDir, destDataDir string, target time.Time) error {
	if _, err := os.Stat(filepath.Join(destDataDir, "temporaldb.sqlite")); err == nil {
		return fmt.Errorf("backup: restore: %s already contains a database", destDataDir)
	}

	snap, hasSnapshot, err := latestSnapshotBefore(sinkDir, target)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(destDataDir, 0o755); err != nil {
		return fmt.Errorf("backup: restore: create dest dir: %w", err)
	}
	var afterSeq int64
	if hasSnapshot {
		if err := copyFile(snap.path, filepath.Join(destDataDir, "temporaldb.sqlite")); err != nil {
			return fmt.Errorf("backup: restore: copy snapshot: %w", err)
		}
		afterSeq = snap.seq
	}

	// storage.Open applies migrations; a no-op for tables the copied
	// snapshot already has, and how an empty database gets its schema
	// when there was no snapshot to copy.
	db, err := storage.Open(destDataDir)
	if err != nil {
		return fmt.Errorf("backup: restore: open restored database: %w", err)
	}
	defer db.Close()

	shipped, err := readShippedEvents(sinkDir, afterSeq, target)
	if err != nil {
		return err
	}

	es := event.NewStore(db)
	for _, se := range shipped {
		if err := es.RestoreEvent(ctx, se.toEvent()); err != nil {
			return fmt.Errorf("backup: restore: replay seq %d: %w", se.Seq, err)
		}
	}
	return nil
}

// LastShippedSeq returns the highest Seq recorded in sinkDir's shipped
// event stream, or 0 if nothing has been shipped yet. A Shipper resuming
// after a restart must start from this, not from 0 — re-shipping already
// recorded events would duplicate them in the stream.
func LastShippedSeq(sinkDir string) (int64, error) {
	f, err := os.Open(filepath.Join(sinkDir, "events.ndjson"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("backup: last shipped seq: %w", err)
	}
	defer f.Close()

	var last int64
	dec := json.NewDecoder(f)
	for {
		var e shippedEvent
		if err := dec.Decode(&e); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return 0, fmt.Errorf("backup: last shipped seq: decode: %w", err)
		}
		if e.Seq > last {
			last = e.Seq
		}
	}
	return last, nil
}
