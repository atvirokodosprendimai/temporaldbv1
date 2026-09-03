package tql

import (
	"encoding/json"
	"sort"
	"strings"
)

// matchExpr evaluates a WHERE expression against a JSON document in Go.
// Used where the candidate set does not come from a live SQL scan and so
// cannot use compile.go's json_extract pushdown: FIND ... AS OF (the
// candidates come from event.Store.ReplayAsOf) and SEARCH (the candidates
// come from the vector index).
func matchExpr(value json.RawMessage, expr *Expr) bool {
	if expr == nil {
		return true
	}
	var doc map[string]any
	if len(value) > 0 {
		if err := json.Unmarshal(value, &doc); err != nil {
			return false
		}
	}
	for _, term := range expr.Terms {
		if !matchTerm(doc, term) {
			return false
		}
	}
	return true
}

func matchTerm(doc map[string]any, term Cmp) bool {
	got, ok := lookupPath(doc, term.Field)
	switch term.Op {
	case OpEq:
		if term.Value == nil {
			return !ok || got == nil
		}
		return ok && compareEqual(got, term.Value)
	case OpNeq:
		if term.Value == nil {
			return ok && got != nil
		}
		return !ok || !compareEqual(got, term.Value)
	case OpLt, OpLte, OpGt, OpGte:
		if !ok {
			return false
		}
		gf, gok := toFloat(got)
		wf, wok := toFloat(term.Value)
		if !gok || !wok {
			return false
		}
		switch term.Op {
		case OpLt:
			return gf < wf
		case OpLte:
			return gf <= wf
		case OpGt:
			return gf > wf
		default: // OpGte
			return gf >= wf
		}
	case OpIn:
		arr, aok := term.Value.([]any)
		if !ok || !aok {
			return false
		}
		for _, v := range arr {
			if compareEqual(got, v) {
				return true
			}
		}
		return false
	case OpContains:
		arr, aok := got.([]any)
		if !ok || !aok {
			return false
		}
		for _, v := range arr {
			if compareEqual(v, term.Value) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func lookupPath(doc map[string]any, path string) (any, bool) {
	var cur any = doc
	for _, p := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// compareEqual is type-strict: a JSON string never equals a JSON number
// or boolean, even if their textual forms coincide.
func compareEqual(a, b any) bool {
	switch av := a.(type) {
	case float64:
		bf, ok := toFloat(b)
		return ok && av == bf
	case string:
		bs, ok := b.(string)
		return ok && av == bs
	case bool:
		bb, ok := b.(bool)
		return ok && av == bb
	case nil:
		return b == nil
	default:
		return false
	}
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}

// sortAndLimit applies ORDER BY/LIMIT in Go for result sets that did not
// come from a SQL query (the AS-OF path). orderBy == "" leaves order
// unchanged (insertion order, i.e. key order from ReplayAsOf).
func sortAndLimit(rows []ResultValue, orderBy string, desc bool, limit int) []ResultValue {
	if orderBy != "" {
		sort.SliceStable(rows, func(i, j int) bool {
			if desc {
				return lessField(rows[j].Value, rows[i].Value, orderBy)
			}
			return lessField(rows[i].Value, rows[j].Value, orderBy)
		})
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

// lessField orders a before b by field; a value missing the field sorts
// last regardless of direction (the caller flips the comparator for DESC).
func lessField(a, b json.RawMessage, field string) bool {
	va, aok := extractField(a, field)
	vb, bok := extractField(b, field)
	if !aok {
		return false
	}
	if !bok {
		return true
	}
	switch x := va.(type) {
	case float64:
		if y, ok := vb.(float64); ok {
			return x < y
		}
	case string:
		if y, ok := vb.(string); ok {
			return x < y
		}
	}
	return false
}

func extractField(raw json.RawMessage, field string) (any, bool) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, false
	}
	return lookupPath(doc, field)
}
