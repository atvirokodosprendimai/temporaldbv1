package vector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// QdrantClient calls a Qdrant instance for collection setup, point
// upsert, and vector search.
type QdrantClient struct {
	URL        string // base URL, e.g. "http://localhost:6333"
	APIKey     string // "" if the instance requires none
	HTTPClient *http.Client
}

// NewQdrantClient builds a QdrantClient. apiKey may be "".
func NewQdrantClient(url, apiKey string) *QdrantClient {
	return &QdrantClient{
		URL:        strings.TrimRight(url, "/"),
		APIKey:     apiKey,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (q *QdrantClient) headers() map[string]string {
	if q.APIKey == "" {
		return nil
	}
	return map[string]string{"api-key": q.APIKey}
}

// EnsureCollection creates the named collection (cosine distance, the
// given vector size) if it does not already exist. Safe to call before
// every upsert — the common case is a single GET that finds it already
// there.
func (q *QdrantClient) EnsureCollection(ctx context.Context, name string, vectorSize int) error {
	exists, err := q.collectionExists(ctx, name)
	if err != nil {
		return fmt.Errorf("vector: qdrant: ensure collection %s: %w", name, err)
	}
	if exists {
		return nil
	}
	body, err := json.Marshal(map[string]any{
		"vectors": map[string]any{"size": vectorSize, "distance": "Cosine"},
	})
	if err != nil {
		return fmt.Errorf("vector: qdrant: encode create collection: %w", err)
	}
	if err := doJSON(ctx, q.HTTPClient, http.MethodPut, q.URL+"/collections/"+name, body, q.headers(), nil); err != nil {
		return fmt.Errorf("vector: qdrant: create collection %s: %w", name, err)
	}
	return nil
}

func (q *QdrantClient) collectionExists(ctx context.Context, name string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, q.URL+"/collections/"+name, nil)
	if err != nil {
		return false, fmt.Errorf("build request: %w", err)
	}
	for k, v := range q.headers() {
		req.Header.Set(k, v)
	}
	resp, err := q.HTTPClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

// Point is one vector to upsert. ID must be a UUID or unsigned integer
// string — Qdrant's own ID format constraint, which a natural
// "collection/key" string does not satisfy; see PointID.
type Point struct {
	ID      string
	Vector  []float32
	Payload map[string]any
}

// Upsert creates or updates points in collection.
func (q *QdrantClient) Upsert(ctx context.Context, collection string, points []Point) error {
	type wirePoint struct {
		ID      string         `json:"id"`
		Vector  []float32      `json:"vector"`
		Payload map[string]any `json:"payload,omitempty"`
	}
	wp := make([]wirePoint, len(points))
	for i, p := range points {
		wp[i] = wirePoint{ID: p.ID, Vector: p.Vector, Payload: p.Payload}
	}
	body, err := json.Marshal(map[string]any{"points": wp})
	if err != nil {
		return fmt.Errorf("vector: qdrant: encode upsert: %w", err)
	}
	url := q.URL + "/collections/" + collection + "/points?wait=true"
	if err := doJSON(ctx, q.HTTPClient, http.MethodPut, url, body, q.headers(), nil); err != nil {
		return fmt.Errorf("vector: qdrant: upsert into %s: %w", collection, err)
	}
	return nil
}

// SearchResult is one match, with the payload the point was upserted with
// (so a caller can recover its TemporalDB key — see Index.Search).
type SearchResult struct {
	ID      string
	Score   float64
	Payload map[string]any
}

// Search returns the nearest neighbours of vector in collection, best
// match first.
func (q *QdrantClient) Search(ctx context.Context, collection string, vec []float32, limit int) ([]SearchResult, error) {
	body, err := json.Marshal(map[string]any{
		"vector":       vec,
		"limit":        limit,
		"with_payload": true,
	})
	if err != nil {
		return nil, fmt.Errorf("vector: qdrant: encode search: %w", err)
	}

	var resp struct {
		Result []struct {
			ID      json.RawMessage `json:"id"`
			Score   float64         `json:"score"`
			Payload map[string]any  `json:"payload"`
		} `json:"result"`
	}
	url := q.URL + "/collections/" + collection + "/points/search"
	if err := doJSON(ctx, q.HTTPClient, http.MethodPost, url, body, q.headers(), &resp); err != nil {
		return nil, fmt.Errorf("vector: qdrant: search %s: %w", collection, err)
	}

	out := make([]SearchResult, len(resp.Result))
	for i, r := range resp.Result {
		out[i] = SearchResult{
			ID:      strings.Trim(string(r.ID), `"`), // Qdrant IDs are a JSON string (UUID) or number
			Score:   r.Score,
			Payload: r.Payload,
		}
	}
	return out, nil
}
