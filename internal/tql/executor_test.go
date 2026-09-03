package tql

import (
	"context"
	"testing"
	"time"

	"github.com/atvirokodosprendimai/temporaldbv1/internal/event"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/graph"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/storagetest"
)

func newExecutor(t *testing.T, search Searcher) *Executor {
	t.Helper()
	db := storagetest.DB(t)
	t.Cleanup(func() { storagetest.Reset(t, db) })
	es := event.NewStore(db)
	return NewExecutor(db, es, graph.NewStore(es, db), search)
}

func mustExec(t *testing.T, ex *Executor, tql string) Result {
	t.Helper()
	stmt, err := Parse(tql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", tql, err)
	}
	res, err := ex.Exec(context.Background(), stmt)
	if err != nil {
		t.Fatalf("Exec(%q): %v", tql, err)
	}
	return res
}

func TestExecPutThenGet(t *testing.T) {
	ex := newExecutor(t, nil)
	mustExec(t, ex, `PUT users/1 {"name":"Ada"}`)

	res := mustExec(t, ex, `GET users/1`)
	if len(res.Rows) != 1 || string(res.Rows[0].Value) != `{"name":"Ada"}` {
		t.Fatalf("GET result = %+v", res)
	}
}

func TestExecGetMissing(t *testing.T) {
	ex := newExecutor(t, nil)
	res := mustExec(t, ex, `GET users/nope`)
	if len(res.Rows) != 0 {
		t.Errorf("GET(missing) = %+v, want empty Rows", res)
	}
}

func TestExecDelete(t *testing.T) {
	ex := newExecutor(t, nil)
	mustExec(t, ex, `PUT users/1 {"a":1}`)
	mustExec(t, ex, `DELETE users/1`)

	res := mustExec(t, ex, `GET users/1`)
	if len(res.Rows) != 0 {
		t.Errorf("GET after DELETE = %+v, want empty", res)
	}
}

func TestExecFindWhereCompilesToSQL(t *testing.T) {
	ex := newExecutor(t, nil)
	mustExec(t, ex, `PUT users/1 {"age":30,"city":"NYC"}`)
	mustExec(t, ex, `PUT users/2 {"age":20,"city":"NYC"}`)
	mustExec(t, ex, `PUT users/3 {"age":40,"city":"LA"}`)

	res := mustExec(t, ex, `FIND users WHERE age > 25 AND city = "NYC"`)
	if len(res.Rows) != 1 || res.Rows[0].Key != "1" {
		t.Fatalf("FIND result = %+v, want just users/1", res.Rows)
	}
}

func TestExecFindOrderByLimit(t *testing.T) {
	ex := newExecutor(t, nil)
	mustExec(t, ex, `PUT users/1 {"age":30}`)
	mustExec(t, ex, `PUT users/2 {"age":20}`)
	mustExec(t, ex, `PUT users/3 {"age":40}`)

	res := mustExec(t, ex, `FIND users ORDER BY age DESC LIMIT 2`)
	if len(res.Rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(res.Rows))
	}
	if res.Rows[0].Key != "3" || res.Rows[1].Key != "1" {
		t.Errorf("order = [%s %s], want [3 1] (age desc)", res.Rows[0].Key, res.Rows[1].Key)
	}
}

func TestExecFindInAndContains(t *testing.T) {
	ex := newExecutor(t, nil)
	mustExec(t, ex, `PUT docs/1 {"tag":"a","labels":["x","y"]}`)
	mustExec(t, ex, `PUT docs/2 {"tag":"b","labels":["z"]}`)

	res := mustExec(t, ex, `FIND docs WHERE tag IN ["a","c"]`)
	if len(res.Rows) != 1 || res.Rows[0].Key != "1" {
		t.Fatalf("IN result = %+v, want just docs/1", res.Rows)
	}

	res = mustExec(t, ex, `FIND docs WHERE labels CONTAINS "z"`)
	if len(res.Rows) != 1 || res.Rows[0].Key != "2" {
		t.Fatalf("CONTAINS result = %+v, want just docs/2", res.Rows)
	}
}

func TestExecFindDeletedExcluded(t *testing.T) {
	ex := newExecutor(t, nil)
	mustExec(t, ex, `PUT users/1 {"a":1}`)
	mustExec(t, ex, `DELETE users/1`)

	res := mustExec(t, ex, `FIND users`)
	if len(res.Rows) != 0 {
		t.Errorf("FIND after delete = %+v, want empty", res.Rows)
	}
}

// TestExecGetAsOfIsValidTimeNotTxTime is a regression test for a real bug
// found via manual verification (not by the rest of this suite, which
// never exercised a write whose valid_from differs from its real tx_time):
// GET ... AS OF was passing the same instant for both temporal.Visible
// parameters, so AS OF a past business date silently returned nothing
// whenever the write itself had been committed more recently (the normal
// case for any backdated PUT ... AT). AS OF must mean "valid at this
// business time, using everything committed so far" - never "and also
// pretend nothing committed after this instant."
func TestExecGetAsOfIsValidTimeNotTxTime(t *testing.T) {
	ex := newExecutor(t, nil)
	ctx := context.Background()

	// Both PUTs commit for real "now" (tx_time), but are backdated into
	// the business past via AT - so valid_from is far from tx_time,
	// exactly the case the conflated bug masked.
	jan := &PutStmt{Collection: "users", Key: "1", Value: []byte(`{"v":1}`), At: timePtr(t, "2020-01-01T00:00:00Z")}
	jun := &PutStmt{Collection: "users", Key: "1", Value: []byte(`{"v":2}`), At: timePtr(t, "2020-06-01T00:00:00Z")}
	if _, err := ex.Exec(ctx, jan); err != nil {
		t.Fatalf("put jan: %v", err)
	}
	if _, err := ex.Exec(ctx, jun); err != nil {
		t.Fatalf("put jun: %v", err)
	}

	get := &GetStmt{Collection: "users", Key: "1", AsOf: timePtr(t, "2020-03-01T00:00:00Z")}
	res, err := ex.Exec(ctx, get)
	if err != nil {
		t.Fatalf("GET AS OF: %v", err)
	}
	if len(res.Rows) != 1 || string(res.Rows[0].Value) != `{"v":1}` {
		t.Fatalf("GET AS OF 2020-03-01 = %+v, want the January version (v1) - "+
			"both writes committed today, so a tx_time-cutoff bug returns nothing", res.Rows)
	}
}

func timePtr(t *testing.T, s string) *time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return &tm
}

func TestExecFindAsOfUsesGoSidePath(t *testing.T) {
	ex := newExecutor(t, nil)
	stmt1, _ := Parse(`PUT users/1 {"v":1}`)
	if _, err := ex.Exec(context.Background(), stmt1); err != nil {
		t.Fatalf("put v1: %v", err)
	}
	tBetween := time.Now().UTC()
	stmt2, _ := Parse(`PUT users/1 {"v":2}`)
	if _, err := ex.Exec(context.Background(), stmt2); err != nil {
		t.Fatalf("put v2: %v", err)
	}

	find := &FindStmt{Collection: "users", AsOf: &tBetween}
	res, err := ex.Exec(context.Background(), find)
	if err != nil {
		t.Fatalf("FIND AS OF: %v", err)
	}
	if len(res.Rows) != 1 || string(res.Rows[0].Value) != `{"v":1}` {
		t.Fatalf("FIND AS OF result = %+v, want v1", res.Rows)
	}
}

func TestExecHistoryBetween(t *testing.T) {
	ex := newExecutor(t, nil)
	mustExec(t, ex, `PUT users/1 {"v":1} AT "2026-01-01"`)
	mustExec(t, ex, `PUT users/1 {"v":2} AT "2026-02-01"`)
	mustExec(t, ex, `PUT users/1 {"v":3} AT "2026-03-01"`)

	res := mustExec(t, ex, `HISTORY users/1 BETWEEN "2026-01-15" AND "2026-02-15"`)
	if len(res.Rows) != 1 || string(res.Rows[0].Value) != `{"v":2}` {
		t.Fatalf("HISTORY BETWEEN = %+v, want just v2", res.Rows)
	}

	res = mustExec(t, ex, `HISTORY users/1`)
	if len(res.Rows) != 3 {
		t.Fatalf("HISTORY (all) = %d rows, want 3", len(res.Rows))
	}
}

func TestExecRelateRelatedUnrelate(t *testing.T) {
	ex := newExecutor(t, nil)
	mustExec(t, ex, `RELATE users/1 -knows-> users/2 {"since":2020}`)

	res := mustExec(t, ex, `RELATED users/1`)
	if len(res.Edges) != 1 || res.Edges[0].To != "users/2" {
		t.Fatalf("RELATED = %+v", res.Edges)
	}

	mustExec(t, ex, `UNRELATE users/1 -knows-> users/2`)
	res = mustExec(t, ex, `RELATED users/1`)
	if len(res.Edges) != 0 {
		t.Fatalf("RELATED after UNRELATE = %+v, want empty", res.Edges)
	}
}

func TestExecPurgeLeavesLiveIntact(t *testing.T) {
	ex := newExecutor(t, nil)
	mustExec(t, ex, `PUT users/1 {"v":1}`)
	mustExec(t, ex, `PUT users/1 {"v":2}`)
	cutoff := time.Now().UTC().Add(time.Second)

	stmt := &PurgeStmt{Collection: "users", Before: cutoff}
	res, err := ex.Exec(context.Background(), stmt)
	if err != nil {
		t.Fatalf("PURGE: %v", err)
	}
	if res.Purged != 2 {
		t.Errorf("Purged = %d, want 2", res.Purged)
	}

	hist := mustExec(t, ex, `HISTORY users/1`)
	if len(hist.Rows) != 0 {
		t.Errorf("HISTORY after purge = %+v, want empty", hist.Rows)
	}

	get := mustExec(t, ex, `GET users/1`)
	if len(get.Rows) != 1 || string(get.Rows[0].Value) != `{"v":2}` {
		t.Errorf("GET after purge = %+v, want live value untouched", get.Rows)
	}
}

func TestExecSearchNotConfigured(t *testing.T) {
	ex := newExecutor(t, nil)
	_, err := Parse(`SEARCH docs NEAR "hello"`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	stmt, _ := Parse(`SEARCH docs NEAR "hello"`)
	if _, err := ex.Exec(context.Background(), stmt); err == nil {
		t.Error("SEARCH with no Searcher configured: want error, got nil")
	}
}

// fakeSearcher returns a fixed, ordered key list regardless of the query,
// standing in for internal/vector's index (not built yet — T10).
type fakeSearcher struct {
	keys []string
}

func (f fakeSearcher) Search(_ context.Context, _ string, _ string, limit int) ([]string, error) {
	keys := f.keys
	if limit > 0 && limit < len(keys) {
		keys = keys[:limit]
	}
	return keys, nil
}

func TestExecSearchHydratesAndPreservesOrder(t *testing.T) {
	ex := newExecutor(t, fakeSearcher{keys: []string{"3", "1", "2"}})
	mustExec(t, ex, `PUT docs/1 {"v":1}`)
	mustExec(t, ex, `PUT docs/2 {"v":2}`)
	mustExec(t, ex, `PUT docs/3 {"v":3}`)

	res := mustExec(t, ex, `SEARCH docs NEAR "anything"`)
	if len(res.Rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(res.Rows))
	}
	gotOrder := []string{res.Rows[0].Key, res.Rows[1].Key, res.Rows[2].Key}
	want := []string{"3", "1", "2"}
	for i := range want {
		if gotOrder[i] != want[i] {
			t.Errorf("order = %v, want %v (the searcher's relevance order)", gotOrder, want)
		}
	}
}

func TestExecSearchSkipsStaleKeysAndAppliesWhere(t *testing.T) {
	ex := newExecutor(t, fakeSearcher{keys: []string{"1", "2", "gone"}})
	mustExec(t, ex, `PUT docs/1 {"lang":"en"}`)
	mustExec(t, ex, `PUT docs/2 {"lang":"lt"}`)
	// docs/gone is never written: simulates a stale vector-index entry.

	res := mustExec(t, ex, `SEARCH docs NEAR "x" WHERE lang = "en"`)
	if len(res.Rows) != 1 || res.Rows[0].Key != "1" {
		t.Fatalf("SEARCH with WHERE = %+v, want just docs/1", res.Rows)
	}
}

func TestExecUnknownStatementType(t *testing.T) {
	ex := newExecutor(t, nil)
	var stmt Stmt // nil interface
	if _, err := ex.Exec(context.Background(), stmt); err == nil {
		t.Error("Exec(nil Stmt): want error, got nil")
	}
}
