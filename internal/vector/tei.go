// Package vector implements thin HTTP clients for Qdrant (vector storage
// and search) and TEI (Text Embeddings Inference — embedding and
// reranking) — the optional index behind TQL's SEARCH verb (ADR-001 D6).
// Both are hand-rolled against their small REST surfaces (2-3 endpoints
// each) rather than pulling in an SDK dependency for them.
package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// TEIClient calls a Text Embeddings Inference server
// (huggingface/text-embeddings-inference) for embeddings and, optionally,
// reranking.
type TEIClient struct {
	EmbedURL   string // base URL of the embedding TEI instance
	RerankURL  string // base URL of the reranking TEI instance; "" disables Rerank
	HTTPClient *http.Client
}

// NewTEIClient builds a TEIClient. rerankURL may be "".
func NewTEIClient(embedURL, rerankURL string) *TEIClient {
	return &TEIClient{
		EmbedURL:   embedURL,
		RerankURL:  rerankURL,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Embed returns one embedding vector per input text, in order.
func (t *TEIClient) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(map[string]any{"inputs": texts})
	if err != nil {
		return nil, fmt.Errorf("vector: tei: encode embed request: %w", err)
	}
	var out [][]float32
	if err := postJSON(ctx, t.HTTPClient, t.EmbedURL+"/embed", body, &out); err != nil {
		return nil, fmt.Errorf("vector: tei: embed: %w", err)
	}
	if len(out) != len(texts) {
		return nil, fmt.Errorf("vector: tei: embed: got %d vectors for %d inputs", len(out), len(texts))
	}
	return out, nil
}

// RerankResult is one reranked candidate, index into the original texts
// slice passed to Rerank.
type RerankResult struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

// Rerank scores texts against query. Returns an error if RerankURL is
// unset — callers should check that before offering reranking, not treat
// this as the "not configured" signal (ADR-001 D6 gates the whole SEARCH
// verb on QDRANT_URL/TEI_URL; TEI_RERANK_URL is an independent extra).
func (t *TEIClient) Rerank(ctx context.Context, query string, texts []string) ([]RerankResult, error) {
	if t.RerankURL == "" {
		return nil, fmt.Errorf("vector: tei: rerank: no TEI_RERANK_URL configured")
	}
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(map[string]any{"query": query, "texts": texts})
	if err != nil {
		return nil, fmt.Errorf("vector: tei: encode rerank request: %w", err)
	}
	var out []RerankResult
	if err := postJSON(ctx, t.HTTPClient, t.RerankURL+"/rerank", body, &out); err != nil {
		return nil, fmt.Errorf("vector: tei: rerank: %w", err)
	}
	return out, nil
}

// postJSON POSTs body to url and decodes the JSON response into out (if
// non-nil). Shared by TEIClient and QdrantClient — both speak plain
// JSON-over-HTTP with no protocol-specific framing.
func postJSON(ctx context.Context, hc *http.Client, url string, body []byte, out any) error {
	return doJSON(ctx, hc, http.MethodPost, url, body, nil, out)
}

func doJSON(ctx context.Context, hc *http.Client, method, url string, body []byte, headers map[string]string, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, respBody)
	}
	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
