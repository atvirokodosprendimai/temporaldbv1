package temporal

import (
	"testing"
	"time"
)

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tm
}

func TestEncodeParseRoundTrip(t *testing.T) {
	want := mustParse(t, "2026-09-03T10:00:00.123456789Z")
	got, err := Parse(Encode(want))
	if err != nil {
		t.Fatalf("Parse(Encode(want)): %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("round trip = %v, want %v", got, want)
	}
}

// TestEncodeIsLexicallySortable is a regression test for the exact class of
// bug documented in wing_craft/gotchas: a variable-width instant encoding
// (Go's default RFC3339Nano trims trailing fractional zeros) sorts wrong as
// a string. ".5" would format shorter than ".45" and a naive lexical
// comparison would rank it first even though 500ms > 450ms.
func TestEncodeIsLexicallySortable(t *testing.T) {
	base := mustParse(t, "2026-09-03T10:00:00Z") // exactly on the second: no fraction at all
	later := base.Add(500 * time.Millisecond)    // .5 — shorter than some others if trimmed

	encBase, encLater := Encode(base), Encode(later)
	if len(encBase) != len(encLater) {
		t.Fatalf("encoded widths differ: %q (%d) vs %q (%d)", encBase, len(encBase), encLater, len(encLater))
	}
	if !(encBase < encLater) {
		t.Errorf("lexical order wrong: Encode(base)=%q should sort before Encode(later)=%q", encBase, encLater)
	}
}

func TestVisibleCurrentNoTimeQualifiers(t *testing.T) {
	t1 := mustParse(t, "2026-01-01T00:00:00Z")
	t2 := mustParse(t, "2026-02-01T00:00:00Z")
	events := []Event{
		{Seq: 1, ValidFrom: t1, TxTime: t1, Op: OpPut},
		{Seq: 2, ValidFrom: t2, TxTime: t2, Op: OpPut},
	}
	got, ok := Visible(events, time.Time{}, time.Time{})
	if !ok || got.Seq != 2 {
		t.Fatalf("Visible(no qualifiers) = seq %d, ok=%v; want seq 2, ok=true", got.Seq, ok)
	}
}

func TestVisibleAsOfExcludesLaterCommits(t *testing.T) {
	t1 := mustParse(t, "2026-01-01T00:00:00Z")
	t2 := mustParse(t, "2026-02-01T00:00:00Z")
	events := []Event{
		{Seq: 1, ValidFrom: t1, TxTime: t1, Op: OpPut},
		{Seq: 2, ValidFrom: t2, TxTime: t2, Op: OpPut},
	}
	asOf := t1.Add(time.Hour) // after the first commit, before the second
	got, ok := Visible(events, asOf, time.Time{})
	if !ok || got.Seq != 1 {
		t.Fatalf("Visible(asOf between commits) = seq %d, ok=%v; want seq 1, ok=true", got.Seq, ok)
	}
}

func TestVisibleBeforeAnyCommit(t *testing.T) {
	t1 := mustParse(t, "2026-01-01T00:00:00Z")
	events := []Event{{Seq: 1, ValidFrom: t1, TxTime: t1, Op: OpPut}}
	_, ok := Visible(events, t1.Add(-time.Hour), time.Time{})
	if ok {
		t.Errorf("Visible(asOf before any commit) ok=true, want false")
	}
}

// TestVisibleSameInstantCorrection is the exact scenario the interval-bug
// gotcha names: "create-then-correct inside one session yields
// valid_from == valid_to == today". This design stores no ValidTo at all
// (the interval end is derived from the next version's ValidFrom at query
// time), so there is nothing to invert — the later write (higher Seq) must
// win a same-instant tie.
func TestVisibleSameInstantCorrection(t *testing.T) {
	same := mustParse(t, "2026-01-01T00:00:00Z")
	events := []Event{
		{Seq: 1, ValidFrom: same, TxTime: same, Op: OpPut, Value: []byte(`{"v":"first"}`)},
		{Seq: 2, ValidFrom: same, TxTime: same, Op: OpPut, Value: []byte(`{"v":"corrected"}`)},
	}
	got, ok := Visible(events, time.Time{}, same)
	if !ok {
		t.Fatalf("Visible(same-instant correction) ok=false, want true")
	}
	if got.Seq != 2 {
		t.Errorf("Visible(same-instant correction) = seq %d, want seq 2 (the later write)", got.Seq)
	}
}

func TestVisibleHalfOpenInterval(t *testing.T) {
	t1 := mustParse(t, "2026-01-01T00:00:00Z")
	t2 := mustParse(t, "2026-02-01T00:00:00Z")
	t3 := mustParse(t, "2026-03-01T00:00:00Z")
	events := []Event{
		{Seq: 1, ValidFrom: t1, TxTime: t1, Op: OpPut, Value: []byte(`"v1"`)},
		{Seq: 2, ValidFrom: t2, TxTime: t2, Op: OpPut, Value: []byte(`"v2"`)},
	}

	// Exactly at t2, the second version is already visible (half-open:
	// [t2, t3) belongs to seq 2).
	got, ok := Visible(events, time.Time{}, t2)
	if !ok || got.Seq != 2 {
		t.Errorf("Visible(at t2) = seq %d, ok=%v; want seq 2, ok=true", got.Seq, ok)
	}

	// One nanosecond before t2, the first version is still visible.
	got, ok = Visible(events, time.Time{}, t2.Add(-time.Nanosecond))
	if !ok || got.Seq != 1 {
		t.Errorf("Visible(just before t2) = seq %d, ok=%v; want seq 1, ok=true", got.Seq, ok)
	}

	// Well past both, still the second (nothing has superseded it).
	got, ok = Visible(events, time.Time{}, t3)
	if !ok || got.Seq != 2 {
		t.Errorf("Visible(after t2) = seq %d, ok=%v; want seq 2, ok=true", got.Seq, ok)
	}
}

func TestVisibleReturnsDeleteEvent(t *testing.T) {
	t1 := mustParse(t, "2026-01-01T00:00:00Z")
	t2 := mustParse(t, "2026-02-01T00:00:00Z")
	events := []Event{
		{Seq: 1, ValidFrom: t1, TxTime: t1, Op: OpPut, Value: []byte(`"v1"`)},
		{Seq: 2, ValidFrom: t2, TxTime: t2, Op: OpDelete},
	}
	got, ok := Visible(events, time.Time{}, time.Time{})
	if !ok {
		t.Fatalf("Visible after delete: ok=false, want true (Visible reports the delete, callers interpret it)")
	}
	if got.Op != OpDelete {
		t.Errorf("Visible after delete: Op = %q, want %q", got.Op, OpDelete)
	}
}
