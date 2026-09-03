package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/atvirokodosprendimai/temporaldbv1/internal/event"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/graph"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/storagetest"
	"github.com/atvirokodosprendimai/temporaldbv1/internal/tql"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	db := storagetest.DB(t)
	t.Cleanup(func() { storagetest.Reset(t, db) })
	es := event.NewStore(db)
	ex := tql.NewExecutor(db, es, graph.NewStore(es, db), nil, nil)
	return New(ex)
}

func doQuery(t *testing.T, s *Server, body string) (*http.Response, queryResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/query", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	resp := rec.Result()

	var qr queryResponse
	if err := json.NewDecoder(resp.Body).Decode(&qr); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp, qr
}

func TestHandleHealthz(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status field = %q, want ok", body["status"])
	}
}

func TestHandleQuerySingleStatement(t *testing.T) {
	s := newTestServer(t)
	resp, qr := doQuery(t, s, `PUT users/1 {"name":"Ada"}`)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if qr.Error != "" {
		t.Fatalf("Error = %q, want empty", qr.Error)
	}
	if len(qr.Results) != 1 || len(qr.Results[0].Rows) != 1 {
		t.Fatalf("Results = %+v", qr.Results)
	}
	if string(qr.Results[0].Rows[0].Value) != `{"name":"Ada"}` {
		t.Errorf("Value = %s", qr.Results[0].Rows[0].Value)
	}
}

func TestHandleQueryBatch(t *testing.T) {
	s := newTestServer(t)
	body := "PUT users/1 {\"a\":1}\nPUT users/2 {\"a\":2}\nGET users/1"
	resp, qr := doQuery(t, s, body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if qr.Error != "" {
		t.Fatalf("Error = %q, want empty", qr.Error)
	}
	if len(qr.Results) != 3 {
		t.Fatalf("got %d results, want 3", len(qr.Results))
	}
	if string(qr.Results[2].Rows[0].Value) != `{"a":1}` {
		t.Errorf("third result = %+v, want GET users/1's value", qr.Results[2])
	}
}

func TestHandleQueryBatchStopsOnExecutionError(t *testing.T) {
	s := newTestServer(t)
	// newTestServer wires no Searcher, so SEARCH fails at execution time
	// (not parse time) - the batch should stop there.
	body := "PUT a/1 {}\nSEARCH x NEAR \"y\"\nPUT a/2 {}"
	resp, qr := doQuery(t, s, body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (execution errors are reported in the body, not the status)", resp.StatusCode)
	}
	if qr.Error == "" {
		t.Fatal("Error = empty, want the SEARCH failure")
	}
	if len(qr.Results) != 1 {
		t.Fatalf("got %d results, want 1 (only the first PUT ran)", len(qr.Results))
	}

	// Confirm a/2 was genuinely never written.
	_, qr2 := doQuery(t, s, `GET a/2`)
	if len(qr2.Results) != 1 || len(qr2.Results[0].Rows) != 0 {
		t.Errorf("GET a/2 = %+v, want not found (batch stopped before it ran)", qr2.Results)
	}
}

func TestHandleQueryEmptyBody(t *testing.T) {
	s := newTestServer(t)
	resp, qr := doQuery(t, s, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if qr.Error == "" {
		t.Error("Error = empty, want a message about the empty body")
	}
}

func TestHandleQueryParseError(t *testing.T) {
	s := newTestServer(t)
	resp, qr := doQuery(t, s, "BOGUS users/1")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if qr.Error == "" {
		t.Error("Error = empty, want a parse error message")
	}
}
