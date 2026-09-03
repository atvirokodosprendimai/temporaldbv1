package tql

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/atvirokodosprendimai/temporaldbv1/internal/graph"
)

// Parse parses one TQL statement. Trailing input after a complete
// statement is an error — Parse never silently ignores it.
func Parse(src string) (Stmt, error) {
	toks, err := tokenize(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}

	if p.peek().Kind == TEOF {
		return nil, fmt.Errorf("tql: empty statement")
	}
	verb := p.peek()
	if verb.Kind != TIdent {
		return nil, fmt.Errorf("tql: expected a command verb at position %d, got %s", verb.Pos, verb)
	}

	switch strings.ToUpper(verb.Text) {
	case "GET":
		return p.parseGet()
	case "FIND":
		return p.parseFind()
	case "PUT":
		return p.parsePut()
	case "DELETE":
		return p.parseDelete()
	case "HISTORY":
		return p.parseHistory()
	case "RELATE":
		return p.parseRelate()
	case "UNRELATE":
		return p.parseUnrelate()
	case "RELATED":
		return p.parseRelated()
	case "SEARCH":
		return p.parseSearch()
	case "PURGE":
		return p.parsePurge()
	case "EDGETYPES":
		return p.parseEdgeTypes()
	default:
		return nil, fmt.Errorf("tql: unknown command %q at position %d", verb.Text, verb.Pos)
	}
}

// ParseBatch parses newline-separated TQL commands, skipping blank lines.
// One line is one statement — a JSON value that itself contains a literal
// newline (e.g. hand-pretty-printed) is not supported; write it on one
// line.
func ParseBatch(src string) ([]Stmt, error) {
	lines := strings.Split(src, "\n")
	var stmts []Stmt
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		s, err := Parse(line)
		if err != nil {
			return nil, fmt.Errorf("tql: line %d: %w", i+1, err)
		}
		stmts = append(stmts, s)
	}
	return stmts, nil
}

type parser struct {
	toks []Token
	pos  int
}

func (p *parser) peek() Token { return p.toks[p.pos] }

func (p *parser) next() Token {
	t := p.toks[p.pos]
	if p.pos < len(p.toks)-1 {
		p.pos++
	}
	return t
}

func (p *parser) atKeyword(kw string) bool {
	t := p.peek()
	return t.Kind == TIdent && strings.EqualFold(t.Text, kw)
}

func (p *parser) expectKeyword(kw string) error {
	if !p.atKeyword(kw) {
		return fmt.Errorf("tql: expected %q at position %d, got %s", kw, p.peek().Pos, p.peek())
	}
	p.next()
	return nil
}

func (p *parser) expect(kind TokenKind, what string) (Token, error) {
	t := p.peek()
	if t.Kind != kind {
		return Token{}, fmt.Errorf("tql: expected %s at position %d, got %s", what, t.Pos, t)
	}
	return p.next(), nil
}

func (p *parser) expectEnd() error {
	if p.peek().Kind != TEOF {
		return fmt.Errorf("tql: unexpected trailing input at position %d: %s", p.peek().Pos, p.peek())
	}
	return nil
}

// parsePath parses <collection>/<key>. The key may be a bare identifier, a
// bare number, or a quoted string — quoting is how a key with a hyphen
// (a UUID) or other special characters is written, since bare identifiers
// never contain '-' (see isIdentCont).
func (p *parser) parsePath() (collection, key string, err error) {
	c, err := p.expect(TIdent, "a collection name")
	if err != nil {
		return "", "", err
	}
	if _, err := p.expect(TSlash, "'/'"); err != nil {
		return "", "", err
	}
	k := p.peek()
	switch k.Kind {
	case TIdent, TNumber, TString:
		p.next()
		return c.Text, k.Text, nil
	default:
		return "", "", fmt.Errorf("tql: expected a key after '/' at position %d, got %s", k.Pos, k)
	}
}

func (p *parser) parseAsOf() (*time.Time, error) {
	if err := p.expectKeyword("AS"); err != nil {
		return nil, err
	}
	if err := p.expectKeyword("OF"); err != nil {
		return nil, err
	}
	return p.parseTime()
}

func (p *parser) parseTime() (*time.Time, error) {
	t := p.peek()
	if t.Kind != TString {
		return nil, fmt.Errorf("tql: expected a quoted time value at position %d, got %s", t.Pos, t)
	}
	p.next()
	tm, err := parseTimeLiteral(t.Text)
	if err != nil {
		return nil, fmt.Errorf("tql: invalid time %q at position %d: %w", t.Text, t.Pos, err)
	}
	return &tm, nil
}

func parseTimeLiteral(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time format %q (want RFC3339 or YYYY-MM-DD)", s)
}

func (p *parser) parseLimit() (int, error) {
	n, err := p.expect(TNumber, "a LIMIT value")
	if err != nil {
		return 0, err
	}
	lim, err := strconv.Atoi(n.Text)
	if err != nil || lim < 0 {
		return 0, fmt.Errorf("tql: invalid LIMIT %q at position %d", n.Text, n.Pos)
	}
	return lim, nil
}

func (p *parser) parseOffset() (int, error) {
	n, err := p.expect(TNumber, "an OFFSET value")
	if err != nil {
		return 0, err
	}
	off, err := strconv.Atoi(n.Text)
	if err != nil || off < 0 {
		return 0, fmt.Errorf("tql: invalid OFFSET %q at position %d", n.Text, n.Pos)
	}
	return off, nil
}

func (p *parser) parseGet() (Stmt, error) {
	p.next() // GET
	coll, key, err := p.parsePath()
	if err != nil {
		return nil, err
	}
	stmt := &GetStmt{Collection: coll, Key: key}
	if p.atKeyword("AS") {
		if stmt.AsOf, err = p.parseAsOf(); err != nil {
			return nil, err
		}
	}
	if err := p.expectEnd(); err != nil {
		return nil, err
	}
	return stmt, nil
}

func (p *parser) parseFind() (Stmt, error) {
	p.next() // FIND
	coll, err := p.expect(TIdent, "a collection name")
	if err != nil {
		return nil, err
	}
	stmt := &FindStmt{Collection: coll.Text}

	if p.atKeyword("WHERE") {
		p.next()
		if stmt.Where, err = p.parseExpr(); err != nil {
			return nil, err
		}
	}
	if p.atKeyword("AS") {
		if stmt.AsOf, err = p.parseAsOf(); err != nil {
			return nil, err
		}
	}
	if p.atKeyword("ORDER") {
		p.next()
		if err := p.expectKeyword("BY"); err != nil {
			return nil, err
		}
		f, err := p.expect(TIdent, "an ORDER BY field")
		if err != nil {
			return nil, err
		}
		stmt.OrderBy = f.Text
		switch {
		case p.atKeyword("DESC"):
			p.next()
			stmt.Desc = true
		case p.atKeyword("ASC"):
			p.next()
		}
	}
	if p.atKeyword("LIMIT") {
		p.next()
		if stmt.Limit, err = p.parseLimit(); err != nil {
			return nil, err
		}
	}
	if p.atKeyword("OFFSET") {
		p.next()
		if stmt.Offset, err = p.parseOffset(); err != nil {
			return nil, err
		}
	}
	if err := p.expectEnd(); err != nil {
		return nil, err
	}
	return stmt, nil
}

func (p *parser) parseExpr() (*Expr, error) {
	var terms []Cmp
	for {
		field, err := p.expect(TIdent, "a field name")
		if err != nil {
			return nil, err
		}
		op, err := p.parseCmpOp()
		if err != nil {
			return nil, err
		}
		val, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		terms = append(terms, Cmp{Field: field.Text, Op: op, Value: val})

		if p.atKeyword("AND") {
			p.next()
			continue
		}
		break
	}
	return &Expr{Terms: terms}, nil
}

func (p *parser) parseCmpOp() (CmpOp, error) {
	t := p.peek()
	switch t.Kind {
	case TEq:
		p.next()
		return OpEq, nil
	case TNeq:
		p.next()
		return OpNeq, nil
	case TLt:
		p.next()
		return OpLt, nil
	case TLte:
		p.next()
		return OpLte, nil
	case TGt:
		p.next()
		return OpGt, nil
	case TGte:
		p.next()
		return OpGte, nil
	case TIdent:
		switch strings.ToUpper(t.Text) {
		case "IN":
			p.next()
			return OpIn, nil
		case "CONTAINS":
			p.next()
			return OpContains, nil
		}
	}
	return "", fmt.Errorf("tql: expected a comparison operator at position %d, got %s", t.Pos, t)
}

func (p *parser) parseValue() (any, error) {
	t := p.peek()
	switch t.Kind {
	case TNumber:
		p.next()
		f, err := strconv.ParseFloat(t.Text, 64)
		if err != nil {
			return nil, fmt.Errorf("tql: invalid number %q at position %d", t.Text, t.Pos)
		}
		return f, nil
	case TString:
		p.next()
		return t.Text, nil
	case TIdent:
		switch strings.ToLower(t.Text) {
		case "true":
			p.next()
			return true, nil
		case "false":
			p.next()
			return false, nil
		case "null":
			p.next()
			return nil, nil
		}
		return nil, fmt.Errorf("tql: expected a value at position %d, got %s", t.Pos, t)
	case TLBracket:
		return p.parseArrayValue()
	default:
		return nil, fmt.Errorf("tql: expected a value at position %d, got %s", t.Pos, t)
	}
}

func (p *parser) parseArrayValue() (any, error) {
	p.next() // [
	var arr []any
	if p.peek().Kind != TRBracket {
		for {
			v, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
			if p.peek().Kind == TComma {
				p.next()
				continue
			}
			break
		}
	}
	if _, err := p.expect(TRBracket, "']'"); err != nil {
		return nil, err
	}
	return arr, nil
}

func (p *parser) parsePut() (Stmt, error) {
	p.next() // PUT
	coll, key, err := p.parsePath()
	if err != nil {
		return nil, err
	}
	j, err := p.expect(TJSON, "a JSON object")
	if err != nil {
		return nil, err
	}
	stmt := &PutStmt{Collection: coll, Key: key, Value: json.RawMessage(j.Text)}
	if p.atKeyword("AT") {
		p.next()
		if stmt.At, err = p.parseTime(); err != nil {
			return nil, err
		}
	}
	if err := p.expectEnd(); err != nil {
		return nil, err
	}
	return stmt, nil
}

func (p *parser) parseDelete() (Stmt, error) {
	p.next() // DELETE
	coll, key, err := p.parsePath()
	if err != nil {
		return nil, err
	}
	if err := p.expectEnd(); err != nil {
		return nil, err
	}
	return &DeleteStmt{Collection: coll, Key: key}, nil
}

func (p *parser) parseHistory() (Stmt, error) {
	p.next() // HISTORY
	coll, key, err := p.parsePath()
	if err != nil {
		return nil, err
	}
	stmt := &HistoryStmt{Collection: coll, Key: key}
	if p.atKeyword("BETWEEN") {
		p.next()
		t1, err := p.parseTime()
		if err != nil {
			return nil, err
		}
		if err := p.expectKeyword("AND"); err != nil {
			return nil, err
		}
		t2, err := p.parseTime()
		if err != nil {
			return nil, err
		}
		stmt.Between = &[2]time.Time{*t1, *t2}
	}
	if err := p.expectEnd(); err != nil {
		return nil, err
	}
	return stmt, nil
}

// parseEdge parses "-<edge-type>->" and returns the edge type text.
func (p *parser) parseEdge() (string, error) {
	if _, err := p.expect(TMinus, "'-' (start of an edge, e.g. -knows->)"); err != nil {
		return "", err
	}
	et, err := p.expect(TIdent, "an edge type")
	if err != nil {
		return "", err
	}
	if _, err := p.expect(TArrow, "'->'"); err != nil {
		return "", err
	}
	return et.Text, nil
}

// parseRelatedEdgeClause parses RELATED's optional edge-type clause:
// "-<type>->" (one type) or "-[<type>,<type>,...]->" (any of several —
// OR semantics, since one edge has exactly one type). Call only when
// p.peek().Kind == TMinus.
func (p *parser) parseRelatedEdgeClause() ([]string, error) {
	if _, err := p.expect(TMinus, "'-' (start of an edge, e.g. -knows-> or -[a,b]->)"); err != nil {
		return nil, err
	}
	if p.peek().Kind == TLBracket {
		p.next() // [
		var types []string
		for {
			t, err := p.expect(TIdent, "an edge type")
			if err != nil {
				return nil, err
			}
			types = append(types, t.Text)
			if p.peek().Kind == TComma {
				p.next()
				continue
			}
			break
		}
		if _, err := p.expect(TRBracket, "']'"); err != nil {
			return nil, err
		}
		if _, err := p.expect(TArrow, "'->'"); err != nil {
			return nil, err
		}
		return types, nil
	}
	et, err := p.expect(TIdent, "an edge type")
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(TArrow, "'->'"); err != nil {
		return nil, err
	}
	return []string{et.Text}, nil
}

// parseDirection parses RELATED's optional "DIRECTION OUT|IN|BOTH"
// clause's value (the DIRECTION keyword itself is consumed by the
// caller).
func (p *parser) parseDirection() (graph.Direction, error) {
	t := p.peek()
	if t.Kind != TIdent {
		return graph.DirOut, fmt.Errorf("tql: expected OUT, IN, or BOTH at position %d, got %s", t.Pos, t)
	}
	p.next()
	switch strings.ToUpper(t.Text) {
	case "OUT":
		return graph.DirOut, nil
	case "IN":
		return graph.DirIn, nil
	case "BOTH":
		return graph.DirBoth, nil
	default:
		return graph.DirOut, fmt.Errorf("tql: DIRECTION must be OUT, IN, or BOTH, got %q at position %d", t.Text, t.Pos)
	}
}

func (p *parser) parseRelate() (Stmt, error) {
	p.next() // RELATE
	fc, fk, err := p.parsePath()
	if err != nil {
		return nil, err
	}
	et, err := p.parseEdge()
	if err != nil {
		return nil, err
	}
	tc, tk, err := p.parsePath()
	if err != nil {
		return nil, err
	}
	stmt := &RelateStmt{FromCollection: fc, FromKey: fk, EdgeType: et, ToCollection: tc, ToKey: tk}
	if p.peek().Kind == TJSON {
		stmt.Props = json.RawMessage(p.next().Text)
	}
	if err := p.expectEnd(); err != nil {
		return nil, err
	}
	return stmt, nil
}

func (p *parser) parseUnrelate() (Stmt, error) {
	p.next() // UNRELATE
	fc, fk, err := p.parsePath()
	if err != nil {
		return nil, err
	}
	et, err := p.parseEdge()
	if err != nil {
		return nil, err
	}
	tc, tk, err := p.parsePath()
	if err != nil {
		return nil, err
	}
	if err := p.expectEnd(); err != nil {
		return nil, err
	}
	return &UnrelateStmt{FromCollection: fc, FromKey: fk, EdgeType: et, ToCollection: tc, ToKey: tk}, nil
}

func (p *parser) parseRelated() (Stmt, error) {
	p.next() // RELATED
	coll, key, err := p.parsePath()
	if err != nil {
		return nil, err
	}
	stmt := &RelatedStmt{Collection: coll, Key: key}
	if p.peek().Kind == TMinus {
		if stmt.EdgeTypes, err = p.parseRelatedEdgeClause(); err != nil {
			return nil, err
		}
	}
	if p.atKeyword("DIRECTION") {
		p.next()
		if stmt.Direction, err = p.parseDirection(); err != nil {
			return nil, err
		}
	}
	if p.atKeyword("AS") {
		if stmt.AsOf, err = p.parseAsOf(); err != nil {
			return nil, err
		}
	}
	if p.atKeyword("LIMIT") {
		p.next()
		if stmt.Limit, err = p.parseLimit(); err != nil {
			return nil, err
		}
	}
	if p.atKeyword("OFFSET") {
		p.next()
		if stmt.Offset, err = p.parseOffset(); err != nil {
			return nil, err
		}
	}
	if err := p.expectEnd(); err != nil {
		return nil, err
	}
	return stmt, nil
}

func (p *parser) parseSearch() (Stmt, error) {
	p.next() // SEARCH
	coll, err := p.expect(TIdent, "a collection name")
	if err != nil {
		return nil, err
	}
	if err := p.expectKeyword("NEAR"); err != nil {
		return nil, err
	}
	q, err := p.expect(TString, "a quoted search text")
	if err != nil {
		return nil, err
	}
	stmt := &SearchStmt{Collection: coll.Text, Query: q.Text}
	if p.atKeyword("WHERE") {
		p.next()
		if stmt.Where, err = p.parseExpr(); err != nil {
			return nil, err
		}
	}
	if p.atKeyword("LIMIT") {
		p.next()
		if stmt.Limit, err = p.parseLimit(); err != nil {
			return nil, err
		}
	}
	if p.atKeyword("OFFSET") {
		p.next()
		if stmt.Offset, err = p.parseOffset(); err != nil {
			return nil, err
		}
	}
	if err := p.expectEnd(); err != nil {
		return nil, err
	}
	return stmt, nil
}

func (p *parser) parsePurge() (Stmt, error) {
	p.next() // PURGE
	coll, err := p.expect(TIdent, "a collection name")
	if err != nil {
		return nil, err
	}
	if err := p.expectKeyword("BEFORE"); err != nil {
		return nil, err
	}
	t, err := p.parseTime()
	if err != nil {
		return nil, err
	}
	if err := p.expectEnd(); err != nil {
		return nil, err
	}
	return &PurgeStmt{Collection: coll.Text, Before: *t}, nil
}

func (p *parser) parseEdgeTypes() (Stmt, error) {
	p.next() // EDGETYPES
	if err := p.expectEnd(); err != nil {
		return nil, err
	}
	return &EdgeTypesStmt{}, nil
}
