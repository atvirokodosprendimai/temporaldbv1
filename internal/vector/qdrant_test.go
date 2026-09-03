package vector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQdrantEnsureCollectionCreatesWhenMissing(t *testing.T) {
	var createCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPut:
			createCalled = true
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			vectors, _ := body["vectors"].(map[string]any)
			if vectors["size"] != float64(3) {
				t.Errorf("create body vectors.size = %v, want 3", vectors["size"])
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true, "status": "ok"})
		}
	}))
	defer srv.Close()

	c := NewQdrantClient(srv.URL, "")
	if err := c.EnsureCollection(context.Background(), "docs", 3); err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}
	if !createCalled {
		t.Error("EnsureCollection did not create a missing collection")
	}
}

func TestQdrantEnsureCollectionNoopWhenExists(t *testing.T) {
	var putCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			putCalled = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewQdrantClient(srv.URL, "")
	if err := c.EnsureCollection(context.Background(), "docs", 3); err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}
	if putCalled {
		t.Error("EnsureCollection called PUT when the collection already existed (GET returned 200)")
	}
}

func TestQdrantUpsertAndSearch(t *testing.T) {
	var stored []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/collections/docs/points":
			var body struct {
				Points []map[string]any `json:"points"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			stored = body.Points
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/collections/docs/points/search":
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{
				"result": []map[string]any{
					{"id": stored[0]["id"], "score": 0.99, "payload": stored[0]["payload"]},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewQdrantClient(srv.URL, "")
	id := PointID("1")
	err := c.Upsert(context.Background(), "docs", []Point{
		{ID: id, Vector: []float32{0.1, 0.2}, Payload: map[string]any{"key": "1"}},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := c.Search(context.Background(), "docs", []float32{0.1, 0.2}, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].ID != id || results[0].Score != 0.99 {
		t.Fatalf("Search = %+v, want id=%s score=0.99", results, id)
	}
	if results[0].Payload["key"] != "1" {
		t.Errorf("Payload = %+v, want key=1", results[0].Payload)
	}
}

func TestQdrantAPIKeyHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api-key") != "secret" {
			t.Errorf("api-key header = %q, want secret", r.Header.Get("api-key"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewQdrantClient(srv.URL, "secret")
	if err := c.EnsureCollection(context.Background(), "docs", 3); err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}
}
