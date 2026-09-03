// Package graph implements relations (the knowledge graph) as a
// distinguished collection versioned through the same event log as any
// other data, so edges are temporal too (ADR-001 D5). There is no second
// storage subsystem: RELATE/UNRELATE are PUT/DELETE against a synthetic
// key, and RELATED is a scan filtered by the routing triple stored in
// each edge event's meta.
package graph

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/atvirokodosprendimai/temporaldbv1/internal/event"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/temporal"
)

// EdgeCollection is the distinguished collection edges live in.
const EdgeCollection = "__edges__"

// Edge is one relation between two documents, identified as
// "<collection>/<key>" strings.
type Edge struct {
	From      string          `json:"from"`
	Type      string          `json:"type"`
	To        string          `json:"to"`
	Props     json.RawMessage `json:"props,omitempty"`
	Seq       int64           `json:"seq,omitempty"`
	ValidFrom time.Time       `json:"valid_from,omitzero"`
	TxTime    time.Time       `json:"tx_time,omitzero"`
}

type edgeMeta struct {
	From string `json:"from"`
	Type string `json:"type"`
	To   string `json:"to"`
}

func edgeKey(from, typ, to string) string {
	return from + "|" + typ + "|" + to
}

// Store manages edges. Writes go through events (the single writer);
// Related's current-state fast path reads live directly via json_extract,
// mirroring how internal/tql compiles FIND (ADR-001 D4).
type Store struct {
	events *event.Store
	db     *sql.DB
}

// NewStore wraps the event store (for writes and AS-OF replay) and the
// underlying database (for the current-state json_extract scan).
func NewStore(events *event.Store, db *sql.DB) *Store {
	return &Store{events: events, db: db}
}

// Relate creates or updates one edge and returns what was committed —
// mirroring event.Store.Append, which is what actually assigns Seq and
// the timestamps.
func (s *Store) Relate(ctx context.Context, fromColl, fromKey, edgeType, toColl, toKey string, props json.RawMessage) (Edge, error) {
	from, to := path(fromColl, fromKey), path(toColl, toKey)
	meta, err := json.Marshal(edgeMeta{From: from, Type: edgeType, To: to})
	if err != nil {
		return Edge{}, fmt.Errorf("graph: encode edge meta: %w", err)
	}
	if len(props) == 0 {
		props = json.RawMessage("{}")
	}
	e, err := s.events.Append(ctx, EdgeCollection, edgeKey(from, edgeType, to), temporal.OpPut, props, meta, time.Time{})
	if err != nil {
		return Edge{}, fmt.Errorf("graph: relate %s -%s-> %s: %w", from, edgeType, to, err)
	}
	return Edge{From: from, Type: edgeType, To: to, Props: e.Value, Seq: e.Seq, ValidFrom: e.ValidFrom, TxTime: e.TxTime}, nil
}

// Unrelate removes one edge (a tombstone, like any other delete — the
// edge's full history remains queryable via RelatedAsOf).
func (s *Store) Unrelate(ctx context.Context, fromColl, fromKey, edgeType, toColl, toKey string) error {
	from, to := path(fromColl, fromKey), path(toColl, toKey)
	_, err := s.events.Append(ctx, EdgeCollection, edgeKey(from, edgeType, to), temporal.OpDelete, nil, nil, time.Time{})
	if err != nil {
		return fmt.Errorf("graph: unrelate %s -%s-> %s: %w", from, edgeType, to, err)
	}
	return nil
}

// EdgeTypes returns every distinct edge type currently in use across the
// whole graph, sorted.
func (s *Store) EdgeTypes(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT json_extract(meta, '$.type') FROM live
		WHERE collection = ? AND deleted = 0 ORDER BY 1`, EdgeCollection)
	if err != nil {
		return nil, fmt.Errorf("graph: edge types: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("graph: edge types: scan: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("graph: edge types: %w", err)
	}
	return out, nil
}

// Related returns current edges from (fromColl/fromKey), optionally
// filtered to one edge type. The fast path: a direct json_extract scan of
// live, no replay.
func (s *Store) Related(ctx context.Context, fromColl, fromKey, edgeType string, limit int) ([]Edge, error) {
	from := path(fromColl, fromKey)
	query := `
		SELECT value, meta, valid_from, tx_time, seq FROM live
		WHERE collection = ? AND deleted = 0 AND json_extract(meta, '$.from') = ?`
	args := []any{EdgeCollection, from}
	if edgeType != "" {
		query += ` AND json_extract(meta, '$.type') = ?`
		args = append(args, edgeType)
	}
	query += ` ORDER BY seq ASC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("graph: related %s: %w", from, err)
	}
	defer rows.Close()

	var out []Edge
	for rows.Next() {
		var props, metaB []byte
		var validFromS, txTimeS string
		var seq int64
		if err := rows.Scan(&props, &metaB, &validFromS, &txTimeS, &seq); err != nil {
			return nil, fmt.Errorf("graph: scan related %s: %w", from, err)
		}
		e, err := toEdge(props, metaB, validFromS, txTimeS, seq)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("graph: related %s: %w", from, err)
	}
	return out, nil
}

// RelatedAsOf returns the edges from (fromColl/fromKey) visible at asOf,
// by replaying the whole edge collection (event.Store.ReplayAsOf) and
// filtering in Go. More expensive than Related; see docs/adr/BACKLOG.md
// §2/§3 for the scaling trigger this and FIND ... AS OF share.
//
// validAt is a valid-time (business time) point, matching TQL's AS OF
// (internal/tql's execRelated) — the tx_time cutoff passed to ReplayAsOf
// stays unbounded, so this never excludes an edge solely because it was
// committed "too recently" relative to validAt.
func (s *Store) RelatedAsOf(ctx context.Context, fromColl, fromKey, edgeType string, validAt time.Time, limit int) ([]Edge, error) {
	from := path(fromColl, fromKey)
	events, err := s.events.ReplayAsOf(ctx, EdgeCollection, time.Time{}, validAt)
	if err != nil {
		return nil, fmt.Errorf("graph: related as of %s: %w", from, err)
	}

	var out []Edge
	for _, ev := range events {
		var m edgeMeta
		if err := json.Unmarshal(ev.Meta, &m); err != nil {
			return nil, fmt.Errorf("graph: decode edge meta for %s: %w", ev.Key, err)
		}
		if m.From != from || (edgeType != "" && m.Type != edgeType) {
			continue
		}
		out = append(out, Edge{
			From: m.From, Type: m.Type, To: m.To, Props: ev.Value,
			Seq: ev.Seq, ValidFrom: ev.ValidFrom, TxTime: ev.TxTime,
		})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func toEdge(props, metaB []byte, validFromS, txTimeS string, seq int64) (Edge, error) {
	var m edgeMeta
	if err := json.Unmarshal(metaB, &m); err != nil {
		return Edge{}, fmt.Errorf("graph: decode edge meta: %w", err)
	}
	validFrom, err := temporal.Parse(validFromS)
	if err != nil {
		return Edge{}, err
	}
	txTime, err := temporal.Parse(txTimeS)
	if err != nil {
		return Edge{}, err
	}
	return Edge{
		From: m.From, Type: m.Type, To: m.To, Props: props,
		Seq: seq, ValidFrom: validFrom, TxTime: txTime,
	}, nil
}

func path(collection, key string) string {
	return collection + "/" + key
}
