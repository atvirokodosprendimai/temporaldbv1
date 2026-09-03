// Package temporal owns TemporalDB's bitemporal data shape and the one
// function that decides what is visible at a point in time (ADR-001 D3).
// Every AS-OF/VALID-AT boundary comparison in the system goes through
// Visible; nothing re-implements it at a call site.
package temporal

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Op names an event's kind.
type Op string

const (
	OpPut    Op = "put"
	OpDelete Op = "delete"
)

// Event is one immutable, versioned record: a row of the event log, or a
// row reconstructed from one. Value is nil for a delete.
type Event struct {
	Seq        int64
	Collection string
	Key        string
	Op         Op
	Value      json.RawMessage
	ValidFrom  time.Time // business time
	TxTime     time.Time // wall-clock commit time
	Meta       json.RawMessage
}

// timeLayout is deliberately fixed-width (always 9 fractional digits, always
// UTC "Z") so that lexical string ordering of encoded instants matches
// chronological ordering — required for AS-OF pushdown into SQL "ORDER BY"
// and "<=" comparisons over the TEXT columns events/live store. Go's default
// RFC3339Nano trims trailing zero digits, which breaks lexical sort (e.g.
// ".5" would sort before ".45" as strings); this layout never trims.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

// Encode renders t as a fixed-width, lexically-sortable instant string, in
// UTC regardless of t's original location.
func Encode(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

// Parse is the inverse of Encode.
func Parse(s string) (time.Time, error) {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("temporal: parse instant %q: %w", s, err)
	}
	return t, nil
}

// Visible returns the single event, among events (all versions of one key,
// in any order), that is visible given the two temporal axes:
//
//   - asOf ("what did the database know"): events committed after asOf
//     (TxTime.After(asOf)) are excluded entirely. Zero means "no cutoff —
//     everything committed so far."
//   - validAt ("what was true in the business timeline"): among the
//     remaining events, intervals are half-open [ValidFrom, next
//     ValidFrom) per key — there is no stored ValidTo, so there is nothing
//     to get wrong on the far boundary. The returned event is the one
//     whose interval contains validAt: the candidate with the latest
//     ValidFrom that does not exceed validAt. Zero validAt defaults to
//     asOf (and if both are zero, to "latest by ValidFrom", i.e. current).
//
// Ties in ValidFrom (a same-instant correction) are broken by Seq: the
// later write wins. The returned Event may have Op == OpDelete — Visible
// answers "what version applies here", not "does the key currently exist";
// callers that want GET-style semantics treat a delete as not-found
// themselves.
//
// ok is false only when no event satisfies the asOf cutoff, or none has a
// ValidFrom at or before validAt.
func Visible(events []Event, asOf, validAt time.Time) (Event, bool) {
	if validAt.IsZero() {
		validAt = asOf
	}

	candidates := make([]Event, 0, len(events))
	for _, e := range events {
		if !asOf.IsZero() && e.TxTime.After(asOf) {
			continue
		}
		candidates = append(candidates, e)
	}
	if len(candidates) == 0 {
		return Event{}, false
	}

	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].ValidFrom.Equal(candidates[j].ValidFrom) {
			return candidates[i].ValidFrom.Before(candidates[j].ValidFrom)
		}
		return candidates[i].Seq < candidates[j].Seq
	})

	var best Event
	found := false
	for _, e := range candidates {
		if !validAt.IsZero() && e.ValidFrom.After(validAt) {
			break // sorted ascending by ValidFrom: nothing after this matches either
		}
		best, found = e, true
	}
	return best, found
}
