package tql

import (
	"strings"
	"testing"
	"time"

	"github.com/atvirokodosprendimai/temporaldbv1/internal/graph"
)

func TestParseGet(t *testing.T) {
	stmt, err := Parse(`GET users/1`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	g, ok := stmt.(*GetStmt)
	if !ok {
		t.Fatalf("got %T, want *GetStmt", stmt)
	}
	if g.Collection != "users" || g.Key != "1" {
		t.Errorf("Collection/Key = %q/%q, want users/1", g.Collection, g.Key)
	}
	if g.AsOf != nil {
		t.Errorf("AsOf = %v, want nil", g.AsOf)
	}
}

func TestParseGetAsOf(t *testing.T) {
	stmt, err := Parse(`GET users/1 AS OF "2026-01-01T00:00:00Z"`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	g := stmt.(*GetStmt)
	want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if g.AsOf == nil || !g.AsOf.Equal(want) {
		t.Errorf("AsOf = %v, want %v", g.AsOf, want)
	}
}

func TestParseGetQuotedKey(t *testing.T) {
	stmt, err := Parse(`GET users/"550e8400-e29b-41d4-a716-446655440000"`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	g := stmt.(*GetStmt)
	if g.Key != "550e8400-e29b-41d4-a716-446655440000" {
		t.Errorf("Key = %q, want the UUID", g.Key)
	}
}

func TestParseFindWhereAndOrderLimit(t *testing.T) {
	stmt, err := Parse(`FIND users WHERE age > 21 AND city = "NYC" ORDER BY age DESC LIMIT 10`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	f := stmt.(*FindStmt)
	if f.Collection != "users" {
		t.Errorf("Collection = %q", f.Collection)
	}
	if f.Where == nil || len(f.Where.Terms) != 2 {
		t.Fatalf("Where = %+v, want 2 terms", f.Where)
	}
	if f.Where.Terms[0].Field != "age" || f.Where.Terms[0].Op != OpGt || f.Where.Terms[0].Value != 21.0 {
		t.Errorf("term0 = %+v", f.Where.Terms[0])
	}
	if f.Where.Terms[1].Field != "city" || f.Where.Terms[1].Op != OpEq || f.Where.Terms[1].Value != "NYC" {
		t.Errorf("term1 = %+v", f.Where.Terms[1])
	}
	if f.OrderBy != "age" || !f.Desc {
		t.Errorf("OrderBy/Desc = %q/%v, want age/true", f.OrderBy, f.Desc)
	}
	if f.Limit != 10 {
		t.Errorf("Limit = %d, want 10", f.Limit)
	}
}

func TestParseFindDottedFieldAndIn(t *testing.T) {
	stmt, err := Parse(`FIND users WHERE address.city IN ["NYC","LA"]`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	f := stmt.(*FindStmt)
	term := f.Where.Terms[0]
	if term.Field != "address.city" || term.Op != OpIn {
		t.Fatalf("term = %+v", term)
	}
	arr, ok := term.Value.([]any)
	if !ok || len(arr) != 2 || arr[0] != "NYC" || arr[1] != "LA" {
		t.Errorf("Value = %#v, want [NYC LA]", term.Value)
	}
}

func TestParseComparisonOperators(t *testing.T) {
	stmt, err := Parse(`FIND x WHERE a != 1 AND b <= 2 AND c >= 3`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	f := stmt.(*FindStmt)
	want := []CmpOp{OpNeq, OpLte, OpGte}
	for i, w := range want {
		if f.Where.Terms[i].Op != w {
			t.Errorf("term[%d].Op = %q, want %q", i, f.Where.Terms[i].Op, w)
		}
	}
}

func TestParseValueTypes(t *testing.T) {
	stmt, err := Parse(`FIND x WHERE a = true AND b = false AND c = null AND d = -4.5`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	terms := stmt.(*FindStmt).Where.Terms
	if len(terms) != 4 {
		t.Fatalf("got %d terms, want 4", len(terms))
	}
	if terms[0].Value != true {
		t.Errorf("a = %#v, want true", terms[0].Value)
	}
	if terms[1].Value != false {
		t.Errorf("b = %#v, want false", terms[1].Value)
	}
	if terms[2].Value != nil {
		t.Errorf("c = %#v, want nil", terms[2].Value)
	}
	if terms[3].Value != -4.5 {
		t.Errorf("d = %#v, want -4.5", terms[3].Value)
	}
}

func TestParseStringEscapes(t *testing.T) {
	stmt, err := Parse(`FIND x WHERE a = "hello \"world\"\nline2"`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := "hello \"world\"\nline2"
	got := stmt.(*FindStmt).Where.Terms[0].Value
	if got != want {
		t.Errorf("Value = %q, want %q", got, want)
	}
}

func TestParsePut(t *testing.T) {
	stmt, err := Parse(`PUT users/1 {"name":"Ada","age":30}`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := stmt.(*PutStmt)
	if p.Collection != "users" || p.Key != "1" {
		t.Errorf("Collection/Key = %q/%q", p.Collection, p.Key)
	}
	if string(p.Value) != `{"name":"Ada","age":30}` {
		t.Errorf("Value = %s", p.Value)
	}
	if p.At != nil {
		t.Errorf("At = %v, want nil", p.At)
	}
}

func TestParsePutAt(t *testing.T) {
	stmt, err := Parse(`PUT users/1 {"v":1} AT "2026-01-01"`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := stmt.(*PutStmt)
	want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if p.At == nil || !p.At.Equal(want) {
		t.Errorf("At = %v, want %v", p.At, want)
	}
}

func TestParsePutJSONWithNestedBracesAndStrings(t *testing.T) {
	stmt, err := Parse(`PUT users/1 {"name":"a{b}c","nested":{"x":1}}`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := `{"name":"a{b}c","nested":{"x":1}}`
	if got := string(stmt.(*PutStmt).Value); got != want {
		t.Errorf("Value = %s, want %s", got, want)
	}
}

func TestParsePutJSONBoundaryThenTrailingClause(t *testing.T) {
	// Confirms the JSON lexer stops exactly at the object boundary rather
	// than over- or under-consuming: what follows must still parse.
	stmt, err := Parse(`PUT users/1 {"a":1} AT "2026-01-01"`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := stmt.(*PutStmt)
	if string(p.Value) != `{"a":1}` {
		t.Errorf("Value = %s", p.Value)
	}
	if p.At == nil {
		t.Error("At = nil, want set")
	}
}

func TestParsePutTrailingGarbageIsError(t *testing.T) {
	if _, err := Parse(`PUT users/1 {"a":1} extra`); err == nil {
		t.Error("Parse(trailing garbage) = nil error, want error")
	}
}

func TestParseDelete(t *testing.T) {
	stmt, err := Parse(`DELETE users/1`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, ok := stmt.(*DeleteStmt); !ok {
		t.Fatalf("got %T, want *DeleteStmt", stmt)
	}
}

func TestParseHistoryBetween(t *testing.T) {
	stmt, err := Parse(`HISTORY users/1 BETWEEN "2026-01-01" AND "2026-02-01"`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	h := stmt.(*HistoryStmt)
	if h.Between == nil {
		t.Fatal("Between = nil, want non-nil")
	}
	want1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	want2 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if !h.Between[0].Equal(want1) || !h.Between[1].Equal(want2) {
		t.Errorf("Between = %v, want [%v %v]", h.Between, want1, want2)
	}
}

func TestParseRelate(t *testing.T) {
	stmt, err := Parse(`RELATE users/1 -knows-> users/2`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r := stmt.(*RelateStmt)
	if r.FromCollection != "users" || r.FromKey != "1" || r.EdgeType != "knows" ||
		r.ToCollection != "users" || r.ToKey != "2" {
		t.Errorf("RelateStmt = %+v", r)
	}
	if r.Props != nil {
		t.Errorf("Props = %s, want nil", r.Props)
	}
}

func TestParseRelateWithProps(t *testing.T) {
	stmt, err := Parse(`RELATE users/1 -knows-> users/2 {"since":2020}`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := string(stmt.(*RelateStmt).Props); got != `{"since":2020}` {
		t.Errorf("Props = %s", got)
	}
}

// TestParseRelateHyphenatedKeyRequiresQuoting proves the documented
// workaround for isIdentCont excluding '-': a hyphenated key (a UUID) must
// be quoted, or it would be misread as the start of an edge arrow.
func TestParseRelateHyphenatedKeyRequiresQuoting(t *testing.T) {
	stmt, err := Parse(`RELATE users/"a-b" -knows-> users/2`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := stmt.(*RelateStmt).FromKey; got != "a-b" {
		t.Errorf("FromKey = %q, want a-b", got)
	}
}

func TestParseUnrelate(t *testing.T) {
	stmt, err := Parse(`UNRELATE users/1 -knows-> users/2`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, ok := stmt.(*UnrelateStmt); !ok {
		t.Fatalf("got %T, want *UnrelateStmt", stmt)
	}
}

func TestParseRelatedAnyType(t *testing.T) {
	stmt, err := Parse(`RELATED users/1`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := stmt.(*RelatedStmt).EdgeTypes; len(got) != 0 {
		t.Errorf("EdgeTypes = %v, want empty (any type)", got)
	}
}

func TestParseRelatedTypedWithLimit(t *testing.T) {
	stmt, err := Parse(`RELATED users/1 -knows-> LIMIT 5`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r := stmt.(*RelatedStmt)
	if len(r.EdgeTypes) != 1 || r.EdgeTypes[0] != "knows" || r.Limit != 5 {
		t.Errorf("EdgeTypes/Limit = %v/%d, want [knows]/5", r.EdgeTypes, r.Limit)
	}
}

func TestParseSearch(t *testing.T) {
	stmt, err := Parse(`SEARCH docs NEAR "golang concurrency" LIMIT 5`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s := stmt.(*SearchStmt)
	if s.Collection != "docs" || s.Query != "golang concurrency" || s.Limit != 5 {
		t.Errorf("SearchStmt = %+v", s)
	}
}

func TestParseSearchWithWhere(t *testing.T) {
	stmt, err := Parse(`SEARCH docs NEAR "x" WHERE lang = "en"`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s := stmt.(*SearchStmt)
	if s.Where == nil || len(s.Where.Terms) != 1 {
		t.Fatalf("Where = %+v", s.Where)
	}
}

func TestParseEdgeTypes(t *testing.T) {
	stmt, err := Parse(`EDGETYPES`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, ok := stmt.(*EdgeTypesStmt); !ok {
		t.Fatalf("got %T, want *EdgeTypesStmt", stmt)
	}
}

func TestParseEdgeTypesRejectsTrailingInput(t *testing.T) {
	if _, err := Parse(`EDGETYPES users`); err == nil {
		t.Error("Parse(EDGETYPES with trailing input) = nil error, want error")
	}
}

func TestParsePurge(t *testing.T) {
	stmt, err := Parse(`PURGE users BEFORE "2026-01-01"`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := stmt.(*PurgeStmt)
	want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if p.Collection != "users" || !p.Before.Equal(want) {
		t.Errorf("PurgeStmt = %+v", p)
	}
}

func TestParseCaseInsensitiveKeywords(t *testing.T) {
	stmt, err := Parse(`get users/1 as of "2026-01-01"`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, ok := stmt.(*GetStmt); !ok {
		t.Fatalf("got %T, want *GetStmt", stmt)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []string{
		``,
		`   `,
		`BOGUS users/1`,
		`GET`,
		`GET users`,
		`GET users/`,
		`GET users/1 AS`,
		`PUT users/1`,
		`PUT users/1 {bad json`,
		`FIND users WHERE age >`,
		`FIND users WHERE age @ 1`,
		`RELATE users/1 knows-> users/2`,
		`GET users/1 "unterminated`,
		`FIND users LIMIT -1`,
		`FIND users LIMIT abc`,
	}
	for _, src := range cases {
		if _, err := Parse(src); err == nil {
			t.Errorf("Parse(%q) = nil error, want error", src)
		}
	}
}

func TestParseBatch(t *testing.T) {
	src := "PUT users/1 {\"a\":1}\n\nGET users/1\n  \nDELETE users/1"
	stmts, err := ParseBatch(src)
	if err != nil {
		t.Fatalf("ParseBatch: %v", err)
	}
	if len(stmts) != 3 {
		t.Fatalf("got %d statements, want 3", len(stmts))
	}
	if _, ok := stmts[0].(*PutStmt); !ok {
		t.Errorf("stmts[0] = %T, want *PutStmt", stmts[0])
	}
	if _, ok := stmts[1].(*GetStmt); !ok {
		t.Errorf("stmts[1] = %T, want *GetStmt", stmts[1])
	}
	if _, ok := stmts[2].(*DeleteStmt); !ok {
		t.Errorf("stmts[2] = %T, want *DeleteStmt", stmts[2])
	}
}

func TestParseBatchReportsLineNumber(t *testing.T) {
	_, err := ParseBatch("GET users/1\nBOGUS")
	if err == nil {
		t.Fatal("ParseBatch: want error")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error %q does not mention line 2", err.Error())
	}
}

func TestParseRelatedMultiType(t *testing.T) {
	stmt, err := Parse(`RELATED users/1 -[knows,parents]->`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r := stmt.(*RelatedStmt)
	if len(r.EdgeTypes) != 2 || r.EdgeTypes[0] != "knows" || r.EdgeTypes[1] != "parents" {
		t.Errorf("EdgeTypes = %v, want [knows parents]", r.EdgeTypes)
	}
}

func TestParseRelatedDirectionDefaultsToOut(t *testing.T) {
	stmt, err := Parse(`RELATED users/1`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := stmt.(*RelatedStmt).Direction; got != graph.DirOut {
		t.Errorf("Direction = %v, want DirOut (default)", got)
	}
}

func TestParseRelatedDirectionIn(t *testing.T) {
	stmt, err := Parse(`RELATED users/1 DIRECTION IN`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := stmt.(*RelatedStmt).Direction; got != graph.DirIn {
		t.Errorf("Direction = %v, want DirIn", got)
	}
}

func TestParseRelatedTypeDirectionLimitOffset(t *testing.T) {
	stmt, err := Parse(`RELATED users/1 -knows-> DIRECTION BOTH LIMIT 5 OFFSET 2`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r := stmt.(*RelatedStmt)
	if len(r.EdgeTypes) != 1 || r.EdgeTypes[0] != "knows" || r.Direction != graph.DirBoth || r.Limit != 5 || r.Offset != 2 {
		t.Errorf("RelatedStmt = %+v, want [knows]/DirBoth/5/2", r)
	}
}

func TestParseRelatedInvalidDirection(t *testing.T) {
	if _, err := Parse(`RELATED users/1 DIRECTION SIDEWAYS`); err == nil {
		t.Fatal("Parse: want error for invalid DIRECTION value, got nil")
	}
}

func TestParseFindOffset(t *testing.T) {
	stmt, err := Parse(`FIND users LIMIT 10 OFFSET 20`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	f := stmt.(*FindStmt)
	if f.Limit != 10 || f.Offset != 20 {
		t.Errorf("Limit/Offset = %d/%d, want 10/20", f.Limit, f.Offset)
	}
}

func TestParseSearchOffset(t *testing.T) {
	stmt, err := Parse(`SEARCH docs NEAR "golang concurrency" LIMIT 5 OFFSET 10`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s := stmt.(*SearchStmt)
	if s.Limit != 5 || s.Offset != 10 {
		t.Errorf("Limit/Offset = %d/%d, want 5/10", s.Limit, s.Offset)
	}
}
