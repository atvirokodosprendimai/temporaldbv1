package tql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/atvirokodosprendimai/temporaldbv1/internal/event"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/graph"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/temporal"
)

// Searcher is implemented by internal/vector's index. It is declared here,
// not imported, so this package never depends on internal/vector and
// SEARCH fails with a clear error (rather than the package failing to
// build) when vector search is not wired in — the normal state per
// ADR-001 D6 when QDRANT_URL/TEI_URL are unset.
type Searcher interface {
	Search(ctx context.Context, collection, queryText string, limit int) (keys []string, err error)
}

// ResultValue is one document in a Result.
type ResultValue struct {
	Collection string          `json:"collection"`
	Key        string          `json:"key"`
	Value      json.RawMessage `json:"value,omitempty"`
	ValidFrom  time.Time       `json:"valid_from"`
	TxTime     time.Time       `json:"tx_time"`
}

// Result is what executing one statement produces. Which field is
// populated depends on the statement kind: GET/FIND/PUT/HISTORY/SEARCH
// use Rows, RELATE/UNRELATE/RELATED use Edges, PURGE uses Purged. DELETE
// and UNRELATE leave everything empty — success is the absence of an
// error.
type Result struct {
	Rows   []ResultValue `json:"rows,omitempty"`
	Edges  []graph.Edge  `json:"edges,omitempty"`
	Purged int64         `json:"purged,omitempty"`
}

// Executor runs parsed TQL statements. Events and Graph share the
// database Executor itself holds a read-only handle to, per ADR-001 D13:
// the write path is serialized inside event.Store; compiled read queries
// here go through SQLite's WAL directly and are never blocked by it.
type Executor struct {
	db     *sql.DB
	Events *event.Store
	Graph  *graph.Store
	Search Searcher // nil disables SEARCH (ADR-001 D6)
}

// NewExecutor builds an Executor. search may be nil.
func NewExecutor(db *sql.DB, events *event.Store, g *graph.Store, search Searcher) *Executor {
	return &Executor{db: db, Events: events, Graph: g, Search: search}
}

// Exec runs one parsed statement.
func (ex *Executor) Exec(ctx context.Context, stmt Stmt) (Result, error) {
	switch s := stmt.(type) {
	case *GetStmt:
		return ex.execGet(ctx, s)
	case *FindStmt:
		return ex.execFind(ctx, s)
	case *PutStmt:
		return ex.execPut(ctx, s)
	case *DeleteStmt:
		return ex.execDelete(ctx, s)
	case *HistoryStmt:
		return ex.execHistory(ctx, s)
	case *RelateStmt:
		return ex.execRelate(ctx, s)
	case *UnrelateStmt:
		return ex.execUnrelate(ctx, s)
	case *RelatedStmt:
		return ex.execRelated(ctx, s)
	case *SearchStmt:
		return ex.execSearch(ctx, s)
	case *PurgeStmt:
		return ex.execPurge(ctx, s)
	default:
		return Result{}, fmt.Errorf("tql: exec: unhandled statement type %T", stmt)
	}
}

func toResultValue(e temporal.Event) ResultValue {
	return ResultValue{
		Collection: e.Collection, Key: e.Key, Value: e.Value,
		ValidFrom: e.ValidFrom, TxTime: e.TxTime,
	}
}

func (ex *Executor) execGet(ctx context.Context, s *GetStmt) (Result, error) {
	var e *temporal.Event
	var err error
	if s.AsOf != nil {
		e, err = ex.Events.AsOf(ctx, s.Collection, s.Key, *s.AsOf, *s.AsOf)
	} else {
		e, err = ex.Events.Get(ctx, s.Collection, s.Key)
	}
	if err != nil {
		return Result{}, err
	}
	if e == nil {
		return Result{}, nil
	}
	return Result{Rows: []ResultValue{toResultValue(*e)}}, nil
}

func (ex *Executor) execFind(ctx context.Context, s *FindStmt) (Result, error) {
	if s.AsOf != nil {
		return ex.execFindAsOf(ctx, s)
	}

	where, args, err := compileExpr(s.Where)
	if err != nil {
		return Result{}, err
	}
	query := `SELECT collection, key, value, valid_from, tx_time FROM live WHERE collection = ? AND deleted = 0`
	qargs := []any{s.Collection}
	if where != "" {
		query += " AND " + where
		qargs = append(qargs, args...)
	}
	if s.OrderBy != "" {
		query += " ORDER BY json_extract(value, ?)"
		qargs = append(qargs, "$."+s.OrderBy)
		if s.Desc {
			query += " DESC"
		}
	}
	if s.Limit > 0 {
		query += " LIMIT ?"
		qargs = append(qargs, s.Limit)
	}

	rows, err := ex.db.QueryContext(ctx, query, qargs...)
	if err != nil {
		return Result{}, fmt.Errorf("tql: find: %w", err)
	}
	defer rows.Close()

	var out []ResultValue
	for rows.Next() {
		var rv ResultValue
		var value []byte
		var validFromS, txTimeS string
		if err := rows.Scan(&rv.Collection, &rv.Key, &value, &validFromS, &txTimeS); err != nil {
			return Result{}, fmt.Errorf("tql: find: scan: %w", err)
		}
		rv.Value = value
		if rv.ValidFrom, err = temporal.Parse(validFromS); err != nil {
			return Result{}, err
		}
		if rv.TxTime, err = temporal.Parse(txTimeS); err != nil {
			return Result{}, err
		}
		out = append(out, rv)
	}
	if err := rows.Err(); err != nil {
		return Result{}, fmt.Errorf("tql: find: %w", err)
	}
	return Result{Rows: out}, nil
}

func (ex *Executor) execFindAsOf(ctx context.Context, s *FindStmt) (Result, error) {
	events, err := ex.Events.ReplayAsOf(ctx, s.Collection, *s.AsOf, *s.AsOf)
	if err != nil {
		return Result{}, err
	}
	var rows []ResultValue
	for _, e := range events {
		if s.Where != nil && !matchExpr(e.Value, s.Where) {
			continue
		}
		rows = append(rows, toResultValue(e))
	}
	rows = sortAndLimit(rows, s.OrderBy, s.Desc, s.Limit)
	return Result{Rows: rows}, nil
}

func (ex *Executor) execPut(ctx context.Context, s *PutStmt) (Result, error) {
	var validFrom time.Time
	if s.At != nil {
		validFrom = *s.At
	}
	e, err := ex.Events.Append(ctx, s.Collection, s.Key, temporal.OpPut, s.Value, nil, validFrom)
	if err != nil {
		return Result{}, err
	}
	return Result{Rows: []ResultValue{toResultValue(e)}}, nil
}

func (ex *Executor) execDelete(ctx context.Context, s *DeleteStmt) (Result, error) {
	_, err := ex.Events.Append(ctx, s.Collection, s.Key, temporal.OpDelete, nil, nil, time.Time{})
	return Result{}, err
}

func (ex *Executor) execHistory(ctx context.Context, s *HistoryStmt) (Result, error) {
	events, err := ex.Events.History(ctx, s.Collection, s.Key)
	if err != nil {
		return Result{}, err
	}
	if s.Between != nil {
		from, to := s.Between[0], s.Between[1]
		filtered := events[:0]
		for _, e := range events {
			if !e.ValidFrom.Before(from) && !e.ValidFrom.After(to) {
				filtered = append(filtered, e)
			}
		}
		events = filtered
	}
	rows := make([]ResultValue, len(events))
	for i, e := range events {
		rows[i] = toResultValue(e)
	}
	return Result{Rows: rows}, nil
}

func (ex *Executor) execRelate(ctx context.Context, s *RelateStmt) (Result, error) {
	e, err := ex.Graph.Relate(ctx, s.FromCollection, s.FromKey, s.EdgeType, s.ToCollection, s.ToKey, s.Props)
	if err != nil {
		return Result{}, err
	}
	return Result{Edges: []graph.Edge{e}}, nil
}

func (ex *Executor) execUnrelate(ctx context.Context, s *UnrelateStmt) (Result, error) {
	err := ex.Graph.Unrelate(ctx, s.FromCollection, s.FromKey, s.EdgeType, s.ToCollection, s.ToKey)
	return Result{}, err
}

func (ex *Executor) execRelated(ctx context.Context, s *RelatedStmt) (Result, error) {
	var edges []graph.Edge
	var err error
	if s.AsOf != nil {
		edges, err = ex.Graph.RelatedAsOf(ctx, s.Collection, s.Key, s.EdgeType, *s.AsOf, s.Limit)
	} else {
		edges, err = ex.Graph.Related(ctx, s.Collection, s.Key, s.EdgeType, s.Limit)
	}
	if err != nil {
		return Result{}, err
	}
	return Result{Edges: edges}, nil
}

func (ex *Executor) execSearch(ctx context.Context, s *SearchStmt) (Result, error) {
	if ex.Search == nil {
		return Result{}, fmt.Errorf("tql: SEARCH requires vector search to be configured (set QDRANT_URL and TEI_URL)")
	}
	keys, err := ex.Search.Search(ctx, s.Collection, s.Query, s.Limit)
	if err != nil {
		return Result{}, fmt.Errorf("tql: search: %w", err)
	}
	if len(keys) == 0 {
		return Result{}, nil
	}

	// Hydrate from live and push the WHERE filter into the same query,
	// scoped to the candidate keys the vector index returned — the
	// "compilation, not interpretation" path (ADR-001 D4) applies here
	// too, just against a key list instead of a full collection scan.
	where, wargs, err := compileExpr(s.Where)
	if err != nil {
		return Result{}, err
	}
	placeholders := make([]string, len(keys))
	args := []any{s.Collection}
	for i, k := range keys {
		placeholders[i] = "?"
		args = append(args, k)
	}
	query := fmt.Sprintf(
		`SELECT collection, key, value, valid_from, tx_time FROM live
		 WHERE collection = ? AND deleted = 0 AND key IN (%s)`,
		strings.Join(placeholders, ","),
	)
	if where != "" {
		query += " AND " + where
		args = append(args, wargs...)
	}

	rows, err := ex.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Result{}, fmt.Errorf("tql: search: hydrate: %w", err)
	}
	defer rows.Close()

	byKey := make(map[string]ResultValue, len(keys))
	for rows.Next() {
		var rv ResultValue
		var value []byte
		var validFromS, txTimeS string
		if err := rows.Scan(&rv.Collection, &rv.Key, &value, &validFromS, &txTimeS); err != nil {
			return Result{}, fmt.Errorf("tql: search: scan: %w", err)
		}
		rv.Value = value
		if rv.ValidFrom, err = temporal.Parse(validFromS); err != nil {
			return Result{}, err
		}
		if rv.TxTime, err = temporal.Parse(txTimeS); err != nil {
			return Result{}, err
		}
		byKey[rv.Key] = rv
	}
	if err := rows.Err(); err != nil {
		return Result{}, fmt.Errorf("tql: search: %w", err)
	}

	// Preserve the vector index's relevance order; skip any key it
	// returned that is no longer live (stale index entry) or that the
	// WHERE filter excluded.
	out := make([]ResultValue, 0, len(keys))
	for _, k := range keys {
		if rv, ok := byKey[k]; ok {
			out = append(out, rv)
		}
	}
	return Result{Rows: out}, nil
}

func (ex *Executor) execPurge(ctx context.Context, s *PurgeStmt) (Result, error) {
	res, err := ex.db.ExecContext(ctx, `DELETE FROM events WHERE collection = ? AND tx_time < ?`,
		s.Collection, temporal.Encode(s.Before))
	if err != nil {
		return Result{}, fmt.Errorf("tql: purge: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Result{}, fmt.Errorf("tql: purge: %w", err)
	}
	return Result{Purged: n}, nil
}
