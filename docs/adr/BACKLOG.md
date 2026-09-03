# Backlog

Deferred scope, tracked with the pointer each ADR's Out of Scope section names. An item here is
either pulled into a future ADR's Context, re-deferred with a fresh pointer, or reclassified as
`(permanent: ...)` — per `adr-debt`'s own stated loop.

## §1 — Authentication / authorization on the server API

Deferred from ADR-001. Nothing in the original ask requests auth, and the v1 server is designed
for a single trusted operator/process (§D8, §D13 of ADR-001). Revisit if TemporalDB is ever run
reachable from an untrusted network, or multi-user access is requested.

## §2 — Cost-based query planner / secondary indexes beyond JSON pushdown

Deferred from ADR-001. `internal/tql` compiles `WHERE` clauses to `json_extract`/`json_each`
predicates over `live.value` (ADR-001 §D4). No index beyond `(collection, key)` exists yet.
Revisit with a measured trigger, not "if it gets slow" (the same lesson ADR-001 cites from
`wing_craft/decisions`, brolis-lizdai ADR-007): a collection whose `FIND` scans exceed roughly
50,000 live rows, or whose observed query latency exceeds ~200ms on real data.

## §3 — Typed per-collection projection table

Deferred from ADR-001 (Alternatives Considered). The generic `live.value` JSON blob is used for
every collection rather than an inferred, typed schema per collection. Same numbered trigger as
§2 above — measure before building a second write path.

## §4 — `OR`, subqueries, and joins in TQL

Deferred from ADR-001 §D4. v1 grammar supports only `AND`-conjoined comparisons in `WHERE`. Revisit
if a real query need surfaces that cannot be expressed as a conjunction (e.g. client-side `OR` via
two `FIND` calls becomes a measured pain point).
