package tql

import (
	"fmt"
	"strings"
)

// compileExpr compiles a WHERE expression to a parameterized SQL fragment
// over live.value (ADR-001 D4: "compilation, not interpretation" for the
// current-state fast path). Every path and value is bound as a parameter
// — none are string-concatenated into the SQL — so no manual quoting is
// needed regardless of what a field name or value contains.
func compileExpr(expr *Expr) (where string, args []any, err error) {
	if expr == nil || len(expr.Terms) == 0 {
		return "", nil, nil
	}
	var clauses []string
	for _, term := range expr.Terms {
		clause, cargs, err := compileTerm("$."+term.Field, term.Op, term.Value)
		if err != nil {
			return "", nil, err
		}
		clauses = append(clauses, clause)
		args = append(args, cargs...)
	}
	return strings.Join(clauses, " AND "), args, nil
}

func compileTerm(path string, op CmpOp, value any) (string, []any, error) {
	switch op {
	case OpEq:
		if value == nil {
			return "json_extract(value, ?) IS NULL", []any{path}, nil
		}
		return "json_extract(value, ?) = ?", []any{path, sqlValue(value)}, nil
	case OpNeq:
		if value == nil {
			return "json_extract(value, ?) IS NOT NULL", []any{path}, nil
		}
		return "json_extract(value, ?) != ?", []any{path, sqlValue(value)}, nil
	case OpLt:
		return "json_extract(value, ?) < ?", []any{path, sqlValue(value)}, nil
	case OpLte:
		return "json_extract(value, ?) <= ?", []any{path, sqlValue(value)}, nil
	case OpGt:
		return "json_extract(value, ?) > ?", []any{path, sqlValue(value)}, nil
	case OpGte:
		return "json_extract(value, ?) >= ?", []any{path, sqlValue(value)}, nil
	case OpIn:
		arr, ok := value.([]any)
		if !ok {
			return "", nil, fmt.Errorf("tql: IN requires an array value")
		}
		if len(arr) == 0 {
			return "0", nil, nil // an empty IN-list matches nothing
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(arr)), ",")
		args := make([]any, 0, len(arr)+1)
		args = append(args, path)
		for _, v := range arr {
			args = append(args, sqlValue(v))
		}
		return fmt.Sprintf("json_extract(value, ?) IN (%s)", placeholders), args, nil
	case OpContains:
		// live.value is qualified deliberately: json_each's own output
		// also has a column named "value", and an unqualified reference
		// here resolves to that empty inner column instead of the outer
		// row being tested — confirmed empirically (json_each(value, ?)
		// silently matches nothing; json_each(live.value, ?) is correct).
		return "EXISTS (SELECT 1 FROM json_each(live.value, ?) WHERE json_each.value = ?)",
			[]any{path, sqlValue(value)}, nil
	default:
		return "", nil, fmt.Errorf("tql: unsupported operator %q", op)
	}
}

// sqlValue adapts a parsed TQL value for binding as a SQL parameter. bool
// becomes 0/1 to compare correctly against json_extract's own 0/1
// encoding of a JSON boolean.
func sqlValue(v any) any {
	if b, ok := v.(bool); ok {
		if b {
			return int64(1)
		}
		return int64(0)
	}
	return v
}
