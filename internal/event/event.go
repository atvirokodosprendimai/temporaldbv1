// Package event implements the single write path for TemporalDB: appending
// immutable rows to the event log and updating the live projection in the
// same transaction (ADR-001 D2/D13). Nothing else writes to the events or
// live tables.
package event

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/atvirokodosprendimai/temporaldbv1/internal/temporal"
)

// Store is the single writer for the event log and its live projection.
type Store struct {
	db *sql.DB
	mu sync.Mutex // ADR-001 D13: serializes the append path
}

// NewStore wraps an already-migrated database.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Append writes one immutable event and updates the live projection to
// match, in one transaction. validFrom may be the zero Time, meaning
// "default to this commit's tx_time" (ADR-001 D3). meta may be nil,
// meaning "{}". value is forced to nil when op is temporal.OpDelete.
func (s *Store) Append(ctx context.Context, collection, key string, op temporal.Op, value, meta json.RawMessage, validFrom time.Time) (temporal.Event, error) {
	if collection == "" || key == "" {
		return temporal.Event{}, fmt.Errorf("event: collection and key are required")
	}
	if op != temporal.OpPut && op != temporal.OpDelete {
		return temporal.Event{}, fmt.Errorf("event: invalid op %q", op)
	}
	if op == temporal.OpDelete {
		value = nil
	}
	if len(meta) == 0 {
		meta = json.RawMessage("{}")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	txTime := time.Now().UTC()
	if validFrom.IsZero() {
		validFrom = txTime
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return temporal.Event{}, fmt.Errorf("event: begin: %w", err)
	}
	defer tx.Rollback() // no-op once Commit has succeeded

	res, err := tx.ExecContext(ctx, `
		INSERT INTO events (collection, key, op, value, valid_from, tx_time, meta)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		collection, key, string(op), nullBytes(value),
		temporal.Encode(validFrom), temporal.Encode(txTime), string(meta),
	)
	if err != nil {
		return temporal.Event{}, fmt.Errorf("event: insert events: %w", err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return temporal.Event{}, fmt.Errorf("event: last insert id: %w", err)
	}

	deleted := 0
	if op == temporal.OpDelete {
		deleted = 1
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO live (collection, key, value, valid_from, tx_time, deleted, seq, meta)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(collection, key) DO UPDATE SET
			value = excluded.value,
			valid_from = excluded.valid_from,
			tx_time = excluded.tx_time,
			deleted = excluded.deleted,
			seq = excluded.seq,
			meta = excluded.meta`,
		collection, key, nullBytes(value),
		temporal.Encode(validFrom), temporal.Encode(txTime), deleted, seq, string(meta),
	); err != nil {
		return temporal.Event{}, fmt.Errorf("event: upsert live: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return temporal.Event{}, fmt.Errorf("event: commit: %w", err)
	}

	return temporal.Event{
		Seq: seq, Collection: collection, Key: key, Op: op,
		Value: value, ValidFrom: validFrom, TxTime: txTime, Meta: meta,
	}, nil
}

// Get returns the current live value for a key, or nil if the key has
// never been written or its most recent version is a delete.
func (s *Store) Get(ctx context.Context, collection, key string) (*temporal.Event, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT value, valid_from, tx_time, deleted, seq, meta FROM live
		WHERE collection = ? AND key = ?`, collection, key)

	var value, meta []byte
	var validFromS, txTimeS string
	var deleted int
	var seq int64
	if err := row.Scan(&value, &validFromS, &txTimeS, &deleted, &seq, &meta); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("event: get %s/%s: %w", collection, key, err)
	}
	if deleted == 1 {
		return nil, nil
	}

	validFrom, err := temporal.Parse(validFromS)
	if err != nil {
		return nil, err
	}
	txTime, err := temporal.Parse(txTimeS)
	if err != nil {
		return nil, err
	}
	return &temporal.Event{
		Seq: seq, Collection: collection, Key: key, Op: temporal.OpPut,
		Value: value, ValidFrom: validFrom, TxTime: txTime, Meta: meta,
	}, nil
}

// History returns every version of one key, oldest first.
func (s *Store) History(ctx context.Context, collection, key string) ([]temporal.Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT seq, op, value, valid_from, tx_time, meta FROM events
		WHERE collection = ? AND key = ? ORDER BY seq ASC`, collection, key)
	if err != nil {
		return nil, fmt.Errorf("event: history %s/%s: %w", collection, key, err)
	}
	defer rows.Close()

	var out []temporal.Event
	for rows.Next() {
		var e temporal.Event
		var opS, validFromS, txTimeS string
		var value, meta []byte
		if err := rows.Scan(&e.Seq, &opS, &value, &validFromS, &txTimeS, &meta); err != nil {
			return nil, fmt.Errorf("event: scan history %s/%s: %w", collection, key, err)
		}
		e.Collection, e.Key, e.Op = collection, key, temporal.Op(opS)
		e.Value, e.Meta = value, meta
		if e.ValidFrom, err = temporal.Parse(validFromS); err != nil {
			return nil, err
		}
		if e.TxTime, err = temporal.Parse(txTimeS); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("event: history %s/%s: %w", collection, key, err)
	}
	return out, nil
}

// AsOf replays a key's full history and returns the version visible at the
// given times (ADR-001 D3; temporal.Visible has the exact semantics). It
// returns (nil, nil) when no version is visible, or the visible version is
// a delete.
func (s *Store) AsOf(ctx context.Context, collection, key string, asOf, validAt time.Time) (*temporal.Event, error) {
	events, err := s.History(ctx, collection, key)
	if err != nil {
		return nil, err
	}
	e, ok := temporal.Visible(events, asOf, validAt)
	if !ok || e.Op == temporal.OpDelete {
		return nil, nil
	}
	return &e, nil
}

// ReplayAsOf returns, for every key ever written in collection, the
// version visible at the given times (temporal.Visible), excluding keys
// with no visible version and keys whose visible version is a delete. It
// is the primitive behind AS-OF queries across a whole collection (FIND
// ... AS OF, graph.Store.RelatedAsOf) where the live projection's
// current-only view cannot answer.
func (s *Store) ReplayAsOf(ctx context.Context, collection string, asOf, validAt time.Time) ([]temporal.Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT key, seq, op, value, valid_from, tx_time, meta FROM events
		WHERE collection = ? ORDER BY key ASC, seq ASC`, collection)
	if err != nil {
		return nil, fmt.Errorf("event: replay %s: %w", collection, err)
	}
	defer rows.Close()

	byKey := make(map[string][]temporal.Event)
	var order []string
	for rows.Next() {
		var e temporal.Event
		var key, opS, validFromS, txTimeS string
		var value, meta []byte
		if err := rows.Scan(&key, &e.Seq, &opS, &value, &validFromS, &txTimeS, &meta); err != nil {
			return nil, fmt.Errorf("event: scan replay %s: %w", collection, err)
		}
		e.Collection, e.Key, e.Op = collection, key, temporal.Op(opS)
		e.Value, e.Meta = value, meta
		if e.ValidFrom, err = temporal.Parse(validFromS); err != nil {
			return nil, err
		}
		if e.TxTime, err = temporal.Parse(txTimeS); err != nil {
			return nil, err
		}
		if _, seen := byKey[key]; !seen {
			order = append(order, key)
		}
		byKey[key] = append(byKey[key], e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("event: replay %s: %w", collection, err)
	}

	out := make([]temporal.Event, 0, len(order))
	for _, key := range order {
		e, ok := temporal.Visible(byKey[key], asOf, validAt)
		if !ok || e.Op == temporal.OpDelete {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func nullBytes(b []byte) interface{} {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}
