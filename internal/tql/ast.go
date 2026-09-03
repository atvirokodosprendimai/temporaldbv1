// Package tql implements the Temporal Query Language (ADR-001 D4): a small,
// line-oriented, human-typeable grammar that is simultaneously the server's
// wire protocol, the CLI's command language, and the MCP surface. This file
// defines the parsed statement and expression shapes; lexer.go and
// parser.go turn TQL source text into these.
package tql

import (
	"encoding/json"
	"time"
)

// Stmt is any parsed TQL statement.
type Stmt interface{ stmt() }

// GetStmt is "GET <collection>/<key> [AS OF <time>]".
type GetStmt struct {
	Collection, Key string
	AsOf            *time.Time // nil means "current"
}

// FindStmt is "FIND <collection> [WHERE <expr>] [AS OF <time>] [ORDER BY
// <field> [ASC|DESC]] [LIMIT <n>]".
type FindStmt struct {
	Collection string
	Where      *Expr
	AsOf       *time.Time
	OrderBy    string // "" means unspecified
	Desc       bool
	Limit      int // 0 means unspecified
}

// PutStmt is "PUT <collection>/<key> <json-object> [AT <time>]".
type PutStmt struct {
	Collection, Key string
	Value           json.RawMessage
	At              *time.Time // nil means "default to tx_time", ADR-001 D3
}

// DeleteStmt is "DELETE <collection>/<key>".
type DeleteStmt struct {
	Collection, Key string
}

// HistoryStmt is "HISTORY <collection>/<key> [BETWEEN <time> AND <time>]".
type HistoryStmt struct {
	Collection, Key string
	Between         *[2]time.Time // nil means "all history"
}

// RelateStmt is "RELATE <from> -<edge-type>-> <to> [<json-object>]".
type RelateStmt struct {
	FromCollection, FromKey string
	EdgeType                string
	ToCollection, ToKey     string
	Props                   json.RawMessage // nil means no properties
}

// UnrelateStmt is "UNRELATE <from> -<edge-type>-> <to>".
type UnrelateStmt struct {
	FromCollection, FromKey string
	EdgeType                string
	ToCollection, ToKey     string
}

// RelatedStmt is "RELATED <collection>/<key> [-<edge-type>->] [AS OF
// <time>] [LIMIT <n>]".
type RelatedStmt struct {
	Collection, Key string
	EdgeType        string // "" means any type
	AsOf            *time.Time
	Limit           int
}

// SearchStmt is "SEARCH <collection> NEAR "<text>" [WHERE <expr>] [LIMIT
// <n>]" (ADR-001 D6: requires vector search to be configured).
type SearchStmt struct {
	Collection string
	Query      string
	Where      *Expr
	Limit      int
}

// PurgeStmt is "PURGE <collection> BEFORE <time>" (ADR-001 D7).
type PurgeStmt struct {
	Collection string
	Before     time.Time
}

func (*GetStmt) stmt()      {}
func (*FindStmt) stmt()     {}
func (*PutStmt) stmt()      {}
func (*DeleteStmt) stmt()   {}
func (*HistoryStmt) stmt()  {}
func (*RelateStmt) stmt()   {}
func (*UnrelateStmt) stmt() {}
func (*RelatedStmt) stmt()  {}
func (*SearchStmt) stmt()   {}
func (*PurgeStmt) stmt()    {}

// CmpOp is a WHERE-clause comparison operator (ADR-001 D4).
type CmpOp string

const (
	OpEq       CmpOp = "="
	OpNeq      CmpOp = "!="
	OpLt       CmpOp = "<"
	OpLte      CmpOp = "<="
	OpGt       CmpOp = ">"
	OpGte      CmpOp = ">="
	OpIn       CmpOp = "IN"
	OpContains CmpOp = "CONTAINS"
)

// Cmp is one "<field> <op> <value>" term. Field may be dotted
// (address.city) to reach nested JSON.
type Cmp struct {
	Field string
	Op    CmpOp
	Value any // string, float64, bool, nil, or []any — never a nested object
}

// Expr is a chain of AND-conjoined comparisons. ADR-001 D4 scopes v1 to
// conjunction only — no OR, no subqueries (see docs/adr/BACKLOG.md §4).
type Expr struct {
	Terms []Cmp
}
