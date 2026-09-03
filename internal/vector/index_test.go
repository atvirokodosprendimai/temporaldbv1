package vector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIndexUpsertThenSearch(t *testing.T) {
	var storedVector []float32
	var storedPayload map[string]any

	qdrant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound) // force EnsureCollection to create
		case r.Method == http.MethodPut && r.URL.Path == "/collections/docs":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut:
			var body struct {
				Points []struct {
					ID      string         `json:"id"`
					Vector  []float32      `json:"vector"`
					Payload map[string]any `json:"payload"`
				} `json:"points"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			storedVector = body.Points[0].Vector
			storedPayload = body.Points[0].Payload
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": []map[string]any{
					{"id": PointID("1"), "score": 0.87, "payload": storedPayload},
				},
			})
		}
	}))
	defer qdrant.Close()

	tei := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([][]float32{{0.5, 0.6}})
	}))
	defer tei.Close()

	idx := NewIndex(NewTEIClient(tei.URL, ""), NewQdrantClient(qdrant.URL, ""))

	if err := idx.Upsert(context.Background(), "docs", "1", "hello world"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if len(storedVector) != 2 {
		t.Fatalf("stored vector = %v, want length 2", storedVector)
	}
	if storedPayload["key"] != "1" {
		t.Fatalf("stored payload = %v, want key=1", storedPayload)
	}

	keys, err := idx.Search(context.Background(), "docs", "hello", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(keys) != 1 || keys[0] != "1" {
		t.Fatalf("Search = %v, want [1]", keys)
	}
}

func TestIndexSearchSkipsPointsWithoutKeyPayload(t *testing.T) {
	qdrant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": []map[string]any{
					{"id": "some-id", "score": 0.5, "payload": map[string]any{"not_key": "x"}},
				},
			})
		}
	}))
	defer qdrant.Close()
	tei := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([][]float32{{0.1}})
	}))
	defer tei.Close()

	idx := NewIndex(NewTEIClient(tei.URL, ""), NewQdrantClient(qdrant.URL, ""))
	keys, err := idx.Search(context.Background(), "docs", "q", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("Search = %v, want empty (point had no key payload)", keys)
	}
}

func TestPointIDIsDeterministic(t *testing.T) {
	a := PointID("users/1")
	b := PointID("users/1")
	c := PointID("users/2")
	if a != b {
		t.Errorf("PointID not deterministic: %s != %s", a, b)
	}
	if a == c {
		t.Errorf("PointID collision: users/1 and users/2 both got %s", a)
	}
}
