// Package client is the reference implementation of TemporalDB's wire
// protocol (ADR-001 D9): everything here is expressed in terms of the
// HTTP+TQL protocol the server exposes (ADR-001 D8) — this package adds
// no capability the protocol doesn't already have.
//
// It defines its own Result/ResultValue/Edge types rather than importing
// internal/tql's: an internal package cannot be named by code outside this
// module, so re-exporting its types through this public package's API
// would leave external callers holding values of a type they cannot
// declare. The wire format (JSON) doesn't care which side's Go type
// encoded or decoded it, as long as the field tags match.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to a TemporalDB server over HTTP.
type Client struct {
	Addr       string // e.g. "http://localhost:7777"
	HTTPClient *http.Client
}

// New builds a Client. addr must include the scheme, e.g.
// "http://localhost:7777".
func New(addr string) *Client {
	return &Client{
		Addr:       strings.TrimRight(addr, "/"),
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// ResultValue is one document, as returned by GET/FIND/PUT/HISTORY/SEARCH.
type ResultValue struct {
	Collection string          `json:"collection"`
	Key        string          `json:"key"`
	Value      json.RawMessage `json:"value,omitempty"`
	ValidFrom  time.Time       `json:"valid_from"`
	TxTime     time.Time       `json:"tx_time"`
}

// Edge is one relation, as returned by RELATE/RELATED.
type Edge struct {
	From      string          `json:"from"`
	Type      string          `json:"type"`
	To        string          `json:"to"`
	Props     json.RawMessage `json:"props,omitempty"`
	Seq       int64           `json:"seq,omitempty"`
	ValidFrom time.Time       `json:"valid_from,omitzero"`
	TxTime    time.Time       `json:"tx_time,omitzero"`
}

// Result is what one statement produced.
type Result struct {
	Rows      []ResultValue `json:"rows,omitempty"`
	Edges     []Edge        `json:"edges,omitempty"`
	Purged    int64         `json:"purged,omitempty"`
	EdgeTypes []string      `json:"edge_types,omitempty"`
}

// QueryResponse mirrors the server's response shape (ADR-001 D8): one
// Result per statement in the request, in order, up to the first one that
// failed (Error is then that failure's message; Results holds whatever
// ran before it — a batch is not atomic, see ADR-001 D8's amended note).
type QueryResponse struct {
	Results []Result `json:"results"`
	Error   string   `json:"error,omitempty"`
}

// Query sends one or more newline-separated TQL statements and returns the
// server's response as-is.
func (c *Client) Query(ctx context.Context, tqlText string) (QueryResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Addr+"/query", bytes.NewBufferString(tqlText))
	if err != nil {
		return QueryResponse{}, fmt.Errorf("client: build request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return QueryResponse{}, fmt.Errorf("client: request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return QueryResponse{}, fmt.Errorf("client: read response: %w", err)
	}

	var qr QueryResponse
	if err := json.Unmarshal(body, &qr); err != nil {
		return QueryResponse{}, fmt.Errorf("client: decode response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode >= 400 && qr.Error == "" {
		qr.Error = fmt.Sprintf("server returned status %d", resp.StatusCode)
	}
	return qr, nil
}

// one runs Query and unwraps exactly one expected result, surfacing a
// server-reported error or a mismatched-result-count as a Go error, so
// typed callers below don't each have to check QueryResponse.Error.
func (c *Client) one(ctx context.Context, tqlText string) (Result, error) {
	qr, err := c.Query(ctx, tqlText)
	if err != nil {
		return Result{}, err
	}
	if qr.Error != "" {
		return Result{}, fmt.Errorf("client: %s", qr.Error)
	}
	if len(qr.Results) != 1 {
		return Result{}, fmt.Errorf("client: expected 1 result, got %d", len(qr.Results))
	}
	return qr.Results[0], nil
}

// Get fetches the current value of a key, or nil if it does not exist.
func (c *Client) Get(ctx context.Context, collection, key string) (*ResultValue, error) {
	res, err := c.one(ctx, fmt.Sprintf("GET %s/%s", collection, quoteKeyIfNeeded(key)))
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, nil
	}
	return &res.Rows[0], nil
}

// Put creates or replaces a document.
func (c *Client) Put(ctx context.Context, collection, key string, value json.RawMessage) (ResultValue, error) {
	res, err := c.one(ctx, fmt.Sprintf("PUT %s/%s %s", collection, quoteKeyIfNeeded(key), string(value)))
	if err != nil {
		return ResultValue{}, err
	}
	if len(res.Rows) == 0 {
		return ResultValue{}, fmt.Errorf("client: PUT returned no result")
	}
	return res.Rows[0], nil
}

// Delete removes a document.
func (c *Client) Delete(ctx context.Context, collection, key string) error {
	_, err := c.one(ctx, fmt.Sprintf("DELETE %s/%s", collection, quoteKeyIfNeeded(key)))
	return err
}

// Find runs FIND against collection. clause is everything that would
// follow the collection name in TQL (WHERE/AS OF/ORDER BY/LIMIT); pass ""
// for none.
func (c *Client) Find(ctx context.Context, collection, clause string) ([]ResultValue, error) {
	q := "FIND " + collection
	if clause != "" {
		q += " " + clause
	}
	res, err := c.one(ctx, q)
	if err != nil {
		return nil, err
	}
	return res.Rows, nil
}

// History returns every version of a key, oldest first.
func (c *Client) History(ctx context.Context, collection, key string) ([]ResultValue, error) {
	res, err := c.one(ctx, fmt.Sprintf("HISTORY %s/%s", collection, quoteKeyIfNeeded(key)))
	if err != nil {
		return nil, err
	}
	return res.Rows, nil
}

// EdgeTypes returns every distinct edge type currently in use across the
// whole graph, sorted.
func (c *Client) EdgeTypes(ctx context.Context) ([]string, error) {
	res, err := c.one(ctx, "EDGETYPES")
	if err != nil {
		return nil, err
	}
	return res.EdgeTypes, nil
}

// Search runs SEARCH against collection (ADR-001 D6 — requires the server
// to have vector search configured; if it does not, this returns the
// server's "not configured" error). whereClause is an optional TQL WHERE
// clause without the WHERE keyword, e.g. `lang = "en"`; pass "" for none.
// limit <= 0 means unspecified.
func (c *Client) Search(ctx context.Context, collection, query, whereClause string, limit int) ([]ResultValue, error) {
	qb, err := json.Marshal(query)
	if err != nil {
		return nil, fmt.Errorf("client: encode search query: %w", err)
	}
	q := fmt.Sprintf("SEARCH %s NEAR %s", collection, qb)
	if whereClause != "" {
		q += " WHERE " + whereClause
	}
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	res, err := c.one(ctx, q)
	if err != nil {
		return nil, err
	}
	return res.Rows, nil
}

// quoteKeyIfNeeded quotes a key for TQL if it contains a character a bare
// identifier cannot hold — notably '-', so a UUID key round-trips (see
// internal/tql's lexer, which excludes '-' from identifiers so RELATE's
// "-edge->" arrow is never ambiguous with a hyphenated name).
func quoteKeyIfNeeded(key string) string {
	for _, r := range key {
		bare := r == '_' || r == '.' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !bare {
			b, _ := json.Marshal(key)
			return string(b)
		}
	}
	return key
}
