// Command temporaldb-mcp exposes TemporalDB to MCP clients (ADR-001 D10):
// tql_query (raw TQL passthrough) plus typed conveniences tql_get,
// tql_put, tql_history, tql_search — each a thin wrapper over the client
// package (ADR-001 D9). No logic is duplicated between these handlers and
// the client; they never touch the database directly.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/atvirokodosprendimai/temporaldbv1/client"
)

func main() {
	addr := os.Getenv("TEMPORALDB_ADDR")
	if addr == "" {
		addr = "http://localhost:7777"
	}
	c := client.New(addr)

	s := mcp.NewServer(&mcp.Implementation{
		Name:    "temporaldb",
		Title:   "TemporalDB",
		Version: "0.1.0",
	}, nil)
	registerTools(s, c)

	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("temporaldb-mcp: %v", err)
	}
}

func registerTools(s *mcp.Server, c *client.Client) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "tql_query",
		Description: "Execute one or more newline-separated TQL (Temporal Query Language) statements " +
			"against TemporalDB. TQL is a small, human-readable query language: GET/FIND/PUT/DELETE/" +
			"HISTORY for documents (with AS OF for time travel), RELATE/UNRELATE/RELATED for graph " +
			"edges, SEARCH for vector search (if configured), PURGE for retention. This is the " +
			"general-purpose escape hatch; prefer tql_get/tql_put/tql_history/tql_search for the common " +
			"cases, which validate their own arguments instead of requiring correct TQL syntax.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in queryIn) (*mcp.CallToolResult, queryOut, error) {
		qr, err := c.Query(ctx, in.TQL)
		if err != nil {
			return nil, queryOut{}, err
		}
		if qr.Error != "" {
			return nil, queryOut{}, fmt.Errorf("%s", qr.Error)
		}
		results, err := toMCPResults(qr.Results)
		if err != nil {
			return nil, queryOut{}, err
		}
		return nil, queryOut{Results: results}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tql_get",
		Description: "Fetch the current value of one document by collection and key.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, getOut, error) {
		v, err := c.Get(ctx, in.Collection, in.Key)
		if err != nil {
			return nil, getOut{}, err
		}
		if v == nil {
			return nil, getOut{Found: false}, nil
		}
		mv, err := toMCPValue(*v)
		if err != nil {
			return nil, getOut{}, err
		}
		return nil, getOut{Found: true, Value: &mv}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tql_put",
		Description: "Create or replace one document by collection and key. value must be a JSON object.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in putIn) (*mcp.CallToolResult, putOut, error) {
		raw, err := json.Marshal(in.Value)
		if err != nil {
			return nil, putOut{}, fmt.Errorf("encode value: %w", err)
		}
		v, err := c.Put(ctx, in.Collection, in.Key, raw)
		if err != nil {
			return nil, putOut{}, err
		}
		mv, err := toMCPValue(v)
		if err != nil {
			return nil, putOut{}, err
		}
		return nil, putOut{Value: mv}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "tql_history",
		Description: "List every version of one document, oldest first — the full time-travel " +
			"history for that key.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in historyIn) (*mcp.CallToolResult, historyOut, error) {
		versions, err := c.History(ctx, in.Collection, in.Key)
		if err != nil {
			return nil, historyOut{}, err
		}
		mvs, err := toMCPValues(versions)
		if err != nil {
			return nil, historyOut{}, err
		}
		return nil, historyOut{Versions: mvs}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "tql_search",
		Description: "Vector-search a collection by natural-language text, optionally filtered by a " +
			"TQL WHERE clause. Requires the server to have vector search configured (QDRANT_URL and " +
			"TEI_URL); otherwise returns an error explaining that.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
		rows, err := c.Search(ctx, in.Collection, in.Query, in.Where, in.Limit)
		if err != nil {
			return nil, searchOut{}, err
		}
		mvs, err := toMCPValues(rows)
		if err != nil {
			return nil, searchOut{}, err
		}
		return nil, searchOut{Results: mvs}, nil
	})
}

// value is TemporalDB's document shape re-expressed for MCP's JSON-schema
// inference. client.ResultValue.Value is json.RawMessage ([]byte under
// the hood); the SDK's automatic schema generation infers that as a byte
// ARRAY, not an arbitrary JSON value, and then rejects any real document
// ({"name":"Ada"}, an object) at both input and output validation.
// Decoding into `any` first gives the schema inferrer — and the MCP
// client on the other end — the actual JSON shape.
type value struct {
	Collection string    `json:"collection"`
	Key        string    `json:"key"`
	Value      any       `json:"value,omitempty"`
	ValidFrom  time.Time `json:"valid_from"`
	TxTime     time.Time `json:"tx_time"`
}

func toMCPValue(v client.ResultValue) (value, error) {
	out := value{Collection: v.Collection, Key: v.Key, ValidFrom: v.ValidFrom, TxTime: v.TxTime}
	if len(v.Value) > 0 {
		if err := json.Unmarshal(v.Value, &out.Value); err != nil {
			return value{}, fmt.Errorf("decode %s/%s value: %w", v.Collection, v.Key, err)
		}
	}
	return out, nil
}

func toMCPValues(vs []client.ResultValue) ([]value, error) {
	out := make([]value, len(vs))
	for i, v := range vs {
		mv, err := toMCPValue(v)
		if err != nil {
			return nil, err
		}
		out[i] = mv
	}
	return out, nil
}

// edge mirrors client.Edge for the same reason value mirrors
// client.ResultValue: Props is json.RawMessage there.
type edge struct {
	From      string    `json:"from"`
	Type      string    `json:"type"`
	To        string    `json:"to"`
	Props     any       `json:"props,omitempty"`
	Seq       int64     `json:"seq,omitempty"`
	ValidFrom time.Time `json:"valid_from,omitzero"`
	TxTime    time.Time `json:"tx_time,omitzero"`
}

func toMCPEdges(es []client.Edge) ([]edge, error) {
	out := make([]edge, len(es))
	for i, e := range es {
		out[i] = edge{From: e.From, Type: e.Type, To: e.To, Seq: e.Seq, ValidFrom: e.ValidFrom, TxTime: e.TxTime}
		if len(e.Props) > 0 {
			if err := json.Unmarshal(e.Props, &out[i].Props); err != nil {
				return nil, fmt.Errorf("decode edge %s-%s->%s props: %w", e.From, e.Type, e.To, err)
			}
		}
	}
	return out, nil
}

// result mirrors client.Result for tql_query's output, for the same
// json.RawMessage reason.
type result struct {
	Rows   []value `json:"rows,omitempty"`
	Edges  []edge  `json:"edges,omitempty"`
	Purged int64   `json:"purged,omitempty"`
}

func toMCPResults(rs []client.Result) ([]result, error) {
	out := make([]result, len(rs))
	for i, r := range rs {
		rows, err := toMCPValues(r.Rows)
		if err != nil {
			return nil, err
		}
		edges, err := toMCPEdges(r.Edges)
		if err != nil {
			return nil, err
		}
		out[i] = result{Rows: rows, Edges: edges, Purged: r.Purged}
	}
	return out, nil
}

type queryIn struct {
	TQL string `json:"tql" jsonschema:"one or more newline-separated TQL statements"`
}
type queryOut struct {
	Results []result `json:"results"`
}

type getIn struct {
	Collection string `json:"collection" jsonschema:"the collection name"`
	Key        string `json:"key" jsonschema:"the document key"`
}
type getOut struct {
	Found bool   `json:"found"`
	Value *value `json:"value,omitempty"`
}

type putIn struct {
	Collection string         `json:"collection" jsonschema:"the collection name"`
	Key        string         `json:"key" jsonschema:"the document key"`
	Value      map[string]any `json:"value" jsonschema:"the JSON object to store"`
}
type putOut struct {
	Value value `json:"value"`
}

type historyIn struct {
	Collection string `json:"collection" jsonschema:"the collection name"`
	Key        string `json:"key" jsonschema:"the document key"`
}
type historyOut struct {
	Versions []value `json:"versions"`
}

type searchIn struct {
	Collection string `json:"collection" jsonschema:"the collection to search"`
	Query      string `json:"query" jsonschema:"natural-language search text"`
	Where      string `json:"where,omitempty" jsonschema:"optional TQL WHERE clause without the WHERE keyword, e.g. lang = \"en\""`
	Limit      int    `json:"limit,omitempty" jsonschema:"maximum number of results"`
}
type searchOut struct {
	Results []value `json:"results"`
}
