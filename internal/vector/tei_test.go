package vector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTEIEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embed" {
			t.Errorf("path = %s, want /embed", r.URL.Path)
		}
		var req struct {
			Inputs []string `json:"inputs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(req.Inputs) != 2 {
			t.Fatalf("got %d inputs, want 2", len(req.Inputs))
		}
		_ = json.NewEncoder(w).Encode([][]float32{{0.1, 0.2}, {0.3, 0.4}})
	}))
	defer srv.Close()

	c := NewTEIClient(srv.URL, "")
	vecs, err := c.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 || len(vecs[0]) != 2 {
		t.Fatalf("vecs = %+v", vecs)
	}
	if vecs[0][0] != 0.1 || vecs[1][1] != 0.4 {
		t.Errorf("vecs = %+v", vecs)
	}
}

func TestTEIEmbedEmptyMakesNoRequest(t *testing.T) {
	c := NewTEIClient("http://unreachable.invalid", "")
	vecs, err := c.Embed(context.Background(), nil)
	if err != nil || vecs != nil {
		t.Errorf("Embed(nil) = %v, %v; want nil, nil (no request should be made)", vecs, err)
	}
}

func TestTEIEmbedMismatchedCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([][]float32{{0.1}}) // 1 vector for 2 inputs
	}))
	defer srv.Close()

	c := NewTEIClient(srv.URL, "")
	if _, err := c.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Error("Embed with mismatched vector count: want error, got nil")
	}
}

func TestTEIEmbedServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewTEIClient(srv.URL, "")
	if _, err := c.Embed(context.Background(), []string{"a"}); err == nil {
		t.Error("Embed against a failing server: want error, got nil")
	}
}

func TestTEIRerank(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" {
			t.Errorf("path = %s, want /rerank", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]RerankResult{{Index: 1, Score: 0.9}, {Index: 0, Score: 0.2}})
	}))
	defer srv.Close()

	c := NewTEIClient("http://unused.invalid", srv.URL)
	results, err := c.Rerank(context.Background(), "query", []string{"a", "b"})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(results) != 2 || results[0].Index != 1 || results[0].Score != 0.9 {
		t.Errorf("results = %+v", results)
	}
}

func TestTEIRerankNotConfigured(t *testing.T) {
	c := NewTEIClient("http://unused.invalid", "")
	if _, err := c.Rerank(context.Background(), "q", []string{"a"}); err == nil {
		t.Error("Rerank with no RerankURL: want error, got nil")
	}
}
