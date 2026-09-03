# ADR-001: The event log is the database; everything else is a projection

**Status:** Proposed
**Date:** 2026-09-03
**Owner:** M
**Spec:** None — no spec stage (greenfield repository; no `docs/specs/` or `eidos/` corpus exists)
**Cross-references:** none — first decision record in this corpus
**Governs:** `internal/`, `client/`, `cmd/`, `go.mod`, `docs/adr/`
**Enforced-by:** None — no quality-harness mutation/gate registry (`tests/mutations.json`, `bin/`) exists in this repository yet; enforcement is `go build ./...`, `go vet ./...`, and `go test ./...` run and inspected after each Implementation task (see Implementation), plus `adr-lint` on this file.
**Invalidates:** none — checked (first record; no prior corpus to conflict with)
**Served-path change:** None yet — this ADR ships no user-facing behavior by itself. Once Implementation (T1–T10) lands, a user or agent gains a running TemporalDB server, CLI, client package, and MCP server where none existed before.

This record is written **before** the code, for a genuinely greenfield repository — the only
commit on `main` is the initial empty scaffold (LICENSE, README, .gitignore). It proposes the
whole-system architecture for TemporalDB in one record because the request was for one coherent
system, not ten independent features, and the load-bearing choice (§D2/§D5 below) is what makes
the other nine consistent with each other. Implementation proceeds in this same session,
task-by-task as listed under Implementation; this ADR is the contract that implementation is
checked against, not a retrospective description of it.

## Context

The ask (M, 2026-09-03, verbatim intent, lightly reformatted):

> temporaldb using sqlite with nocgo as backend. timetravel, streaming backup (with purge option)
> and litestream like mechanism to make backups of all database. query engine should be simple
> and human readable not sql but more like mongodb style - kvdb but for temporal database should
> run as "server" client package also needed in same code base it also should provide mcp, be
> able to call qdrantdb and TEI embeder/reranker (.env if set enable vector database if not ...)
> we need commands to query vector via same interface as KG its knowledge graph, so documents +
> how they related. something like schematix where you can in cli write "command"s sourcing and
> eventsourcing included for internal datastructures. write adr and implement it

Decomposed into requirements:

1. **Storage backend:** SQLite, pure Go, no CGO (`CGO_ENABLED=0` must produce a working binary).
2. **Time travel:** query data as it existed at a past point in time.
3. **Streaming backup with a purge option**, and a **litestream-like** continuous-replication
   mechanism, for the whole database.
4. **Query language:** not SQL; human-readable; "MongoDB style"; the storage model is
   fundamentally KV, extended with temporal semantics.
5. **Runs as a server**; a **Go client package** ships in the same module.
6. **MCP server** exposing the database to LLM agents.
7. **Optional vector search** via Qdrant + TEI (embedding/reranking), gated by `.env` — the
   system must be fully functional with vector search absent.
8. **Vector search and the knowledge graph (documents + relations) share one query interface.**
9. **A CLI** where the user writes structured "commands" (M's reference, "schematix", names a
   tool this session has no record of and could not verify — treated as "an interactive/scriptable
   command-language CLI," not a specific product to imitate. Flagged here as an assumption, not
   silently resolved.)
10. **Event sourcing for internal data structures.**

### Assumptions stated, per the ambiguity in the ask

- **"streaming backup... and litestream like mechanism"** is read as one mechanism described
  twice, not two: the streaming backup *is* the litestream-like mechanism, and it has a purge
  option. See §D7 for why it is not implemented as literal SQLite-WAL shipping.
- **"query vector via same interface as KG"** is read as: relations (the KG) and vector search
  are both queryable through TQL (§D4), and a vector `SEARCH` can be composed with graph and
  temporal filters in one query, hydrated from the same primary store. See §D5/§D6.
- **No auth, no multi-tenancy, no clustering** are in scope. Nothing in the ask names them, and
  adding them speculatively would violate the standing "Simplicity First" rule. See Out of Scope.

## Existing Primitives Audit

Greenfield — there is no prior code in this repository to reuse. What exists instead is a
resolved module path and house convention, checked rather than assumed:

- **Module path:** `github.com/atvirokodosprendimai/temporaldbv1`, taken from
  `git remote get-url origin` (`git@github.com:atvirokodosprendimai/temporaldbv1.git`) rather than
  guessed.
- **Pure-Go SQLite:** `modernc.org/sqlite` is already a dependency (direct or transitive) of three
  sibling repositories in this workspace (`statsv1`, `agentsmemory`, and `wing_craft`'s own
  gotchas reference "glebarez/modernc, pure Go") — it is the house driver, registers itself as
  `"sqlite"` for `database/sql`, and needs no C toolchain.
- **House dialect** (`wing_craft` skill `effective-go`, loaded this session): chi for HTTP
  routing, `urfave/cli/v3` for CLI flags, goose for SQL-format migrations, `github.com/joho/
  godotenv` for `.env` loading — all used by sibling repositories. The same skill names
  `github.com/mark3labs/mcp-go` for MCP (and sibling `agentsmemory` depends on it) — **this ADR
  deviates from that for MCP specifically**: M's explicit, project-scoped direction (2026-09-03)
  is to use the **official** MCP SDK, `github.com/modelcontextprotocol/go-sdk` (confirmed on the
  module proxy: latest stable `v1.7.0`, tagged 2026-07-27, under the `modelcontextprotocol` GitHub
  org — the protocol's own origin, not a third-party client). A specific human decision for this
  project outranks the skill's general default; see §D10 and Alternatives Considered.
- **House architecture doctrine** (`wing_craft` skill `cqrs`, loaded this session), §0: "event-
  source only where the log *is* the domain... could a user, an auditor, or a replay legitimately
  ask 'what did this look like at time T', and would 'we overwrote it' be a defect? If yes, the
  log is the domain." Time travel is not an incidental feature here, it is the literal product
  requirement — so §D2 below is the doctrine applied correctly, not the "self-inflicted wound"
  the same skill warns against (adopting event sourcing for a realtime-UI reason). Single-writer-
  per-aggregate (§1 of that skill) is carried into §D13.
- **Two applicable correctness gotchas** (`wing_craft/gotchas`, loaded this session):
  - *Inclusive-end temporal interval bugs*: a store that ends a fact with a bare date and compares
    it as an instant promoted to end-of-day invites a call-site "subtract a day" fix that writes a
    false fact and can invert `valid_from > valid_to` on the create-then-correct path. Applied in
    §D3: boundaries are fixed once in a single comparison function, using explicit instants
    (`time.Time`, stored as RFC3339 nanosecond strings, not bare dates), with half-open
    `[valid_from, valid_to)` intervals decided deliberately up front.
  - *`json_each`/`json_extract` pushdown*: `modernc.org/sqlite` supports `json_each` as a
    table-valued function, so filtering into a JSON blob column is one correlated SQL statement,
    not a second normalized schema per collection. Applied in §D4/§D5: TQL `WHERE` clauses compile
    to `json_extract`/`json_each` predicates against a single generic `value` column, and a typed
    per-collection projection is explicitly *not* built up front (see Alternatives Considered) —
    the same "probe before inheriting a predicted design" lesson this workspace already paid for
    once (`wing_craft/decisions`, brolis-lizdai ADR-007).
- **Quality gates:** `adr-lint`, `adr-judge`, `adr-debt` are installed and on `PATH`
  (`/Users/mind/code/go/aks/quality-harness/plugin/bin/`). This ADR targets **inline-task mode**
  (`adr-lint ADR.md` with no `tasks/` directory) — the tool's own usage text confirms this is a
  supported, lighter checking mode, not a workaround.
- **File-edit tooling:** per the `human-decisions`/`mrw` skills (loaded this session), file
  work goes through `Read`/`Edit`/`Write`/`mrw`, never ad-hoc shell — this overrides the ambient
  bypass-permissions instruction to prefer `sed`/heredocs, on this team's explicit, standing call.
- **Branch discipline:** a repository hook (`eidos` plugin, `pre-tool-use-branch-check.sh`)
  refuses file writes on `main`. This work proceeds on `task/temporaldb-adr-and-implementation`.

## Decision

**TemporalDB is built as an event-sourced, bitemporal document store on pure-Go SQLite, exposed
through one human-readable query language (TQL) that is simultaneously the server's wire
protocol, the CLI's command language, and the interface for graph and vector queries.** The
thirteen sub-decisions below (D1–D13) fix the specific mechanics; none is optional or deferred —
together they are what "the event log is the database" (this record's title) commits to.

### D1 — Storage engine: `modernc.org/sqlite`, no ORM

Pure-Go, no-cgo SQLite via `database/sql`, driver `modernc.org/sqlite` (registers as `"sqlite"`).
Used **directly**, not through `gorm.io` (the house default for new projects — see Alternatives
Considered for why this ADR deviates). DSN carries WAL pragmas per the `cqrs` skill's own
measured recipe: `?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)`,
and `db.SetMaxOpenConns(1)` — WAL gives readers concurrency without blocking on the single writer,
and bumping open connections above 1 fights WAL rather than helping it.

### D2 — Data model: append-only event log + derived projection (event sourcing)

Every mutation is an immutable row in `events`:

```sql
CREATE TABLE events (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,  -- total order = the log
    collection  TEXT    NOT NULL,
    key         TEXT    NOT NULL,
    op          TEXT    NOT NULL CHECK (op IN ('put','delete')),
    value       BLOB,                                -- JSON document; NULL for delete
    valid_from  TEXT    NOT NULL,                     -- RFC3339Nano instant, business time
    tx_time     TEXT    NOT NULL,                     -- RFC3339Nano instant, commit wall-clock
    meta        TEXT    NOT NULL DEFAULT '{}'         -- JSON: op-specific extras (e.g. edge type)
);
CREATE INDEX idx_events_ck_seq ON events(collection, key, seq);
```

`live` is a materialized projection, updated in the **same SQLite transaction** as the `events`
insert (single-writer, §D13), giving O(1) point reads without replaying history:

```sql
CREATE TABLE live (
    collection  TEXT NOT NULL,
    key         TEXT NOT NULL,
    value       BLOB,                 -- current JSON document; NULL when deleted
    valid_from  TEXT NOT NULL,
    tx_time     TEXT NOT NULL,
    deleted     INTEGER NOT NULL DEFAULT 0,
    seq         INTEGER NOT NULL,     -- last event that touched this row
    PRIMARY KEY (collection, key)
);
```

This is the CQRS shape from the `cqrs` skill applied to the storage engine itself: `events` is
the single writer's append path and the system of record; `live` is a read model, a pure fold of
`events` up to `seq`, rebuildable by replay. A crash between the two writes is impossible to
observe by a reader because they commit in one SQLite transaction.

### D3 — Time travel: bitemporal, half-open intervals, one comparison function

Two independent axes, both stored as full instants (never bare dates — the interval-bug lesson
in Existing Primitives Audit):

- **`tx_time`** — wall-clock commit time; "what did the database believe at time T" (audit /
  replay view). Monotonic with `seq`.
- **`valid_from`** — business time; defaults to `tx_time` when the caller doesn't supply one, so
  the common case never has to think about bitemporality.

Visibility is decided by **one function**, `internal/temporal.Visible(events []Event, asOf,
validAt time.Time) (Event, bool)`, never re-implemented at a call site. Intervals are half-open
`[valid_from, next valid_from)` per key — chosen deliberately, not defaulted into — and every
comparison uses `time.Time.Compare`, never a formatted-string or date-truncated comparison. `GET`/
`FIND` without a time qualifier reads `live` directly (the fast path); `AS OF`/`VALID AT` queries
replay `events` filtered to `seq`s at-or-before the requested instant, last-write-wins per key.

### D4 — Query language: TQL (Temporal Query Language)

Not SQL, not JSON-object Mongo syntax — a small line-oriented, human-typeable grammar that reads
as verbs + a `WHERE` clause, because the ask is explicit that this is meant to be typed by a human
in a CLI, and curly-brace Mongo filter syntax is closer to "human-readable" than SQL but still not
what gets typed comfortably at a prompt:

```
GET    <collection>/<key> [AS OF <time>]
FIND   <collection> [WHERE <expr>] [AS OF <time>] [ORDER BY <field> [ASC|DESC]] [LIMIT <n>]
PUT    <collection>/<key> <json-object> [AT <time>]
DELETE <collection>/<key>
HISTORY <collection>/<key> [BETWEEN <time> AND <time>]
RELATE  <collection>/<key> -<edge-type>-> <collection>/<key> [<json-object>]
UNRELATE <collection>/<key> -<edge-type>-> <collection>/<key>
RELATED <collection>/<key> [-<edge-type>->] [AS OF <time>] [LIMIT <n>]
SEARCH  <collection> NEAR "<text>" [WHERE <expr>] [LIMIT <n>]      -- requires vector config, §D6
PURGE   <collection> BEFORE <time>                                 -- §D7
```

`<expr>` is `<field> <op> <value> [AND <expr>]` with `op ∈ {=, !=, <, <=, >, >=, IN, CONTAINS}`;
`<field>` may be dotted (`address.city`) for nested JSON. `<value>` is a JSON scalar, array, or
string. No `OR`, no subqueries, no joins in v1 — see Out of Scope. A hand-written recursive-
descent lexer/parser (`internal/tql/{lexer,parser,ast}.go`) is used; the grammar is small enough
that a parser-generator dependency would cost more than it saves.

**Compilation, not interpretation, for `FIND`/`SEARCH` filters:** a `WHERE` clause compiles to a
parameterized SQL predicate over `live.value` using `json_extract`/`json_each` (Existing
Primitives Audit), pushed into SQLite rather than deserializing every row into Go and filtering in
process. `GET`/`PUT`/`DELETE`/`RELATE` compile to the same prepared statements the storage layer
already uses for direct key access.

### D5 — Relations (the knowledge graph) live in the same event log

Edges are a distinguished collection (`__edges__`), versioned through the identical `events`/
`live` machinery as any other collection — so relations are temporal too: `RELATE`/`UNRELATE` are
just `PUT`/`DELETE` against a synthetic key `from|type|to`, with `meta` carrying `{from, type, to,
props}`. `RELATED` compiles to a `live` scan filtered by `json_extract(meta,'$.from')`. This
directly satisfies "documents + how they related" without a second storage subsystem — the
KG is not a separate database bolted on, it is what TQL's graph verbs compile to against the one
store.

### D6 — Vector search: optional, additive, never the source of truth

Enabled only when both `QDRANT_URL` and `TEI_URL` are set (via process env or `.env`, §D12); the
server is fully functional with neither set — `SEARCH` then returns a clear "vector search not
configured" error rather than degrading silently. When enabled:

- **One Qdrant collection per TemporalDB collection, same name** — corrected during implementation
  from an earlier "single shared collection with a `collection` field in the payload" reading of
  this section. Scoping by Qdrant's own collection concept means a Qdrant search is already scoped
  correctly by picking the right Qdrant collection, and only the TemporalDB **key** — not the full
  `"collection/key"` path — needs to travel in the payload.
- **Point ID is a deterministic UUID derived from the key** (`uuid.NewSHA1(uuid.NameSpaceOID,
  []byte(key))`, in `internal/vector.PointID`) — **also corrected during implementation**: Qdrant
  requires a point ID to be a UUID or an unsigned integer, so the literal string
  `"<collection>/<key>"` this section originally specified is not a valid Qdrant ID at all. The
  derivation must be deterministic (not random) so re-indexing the same key updates its existing
  point rather than creating a duplicate. Payload is `{key: <key>}` (no document duplication —
  Qdrant is a derived index, TemporalDB stays the source of truth and Qdrant is rebuildable by
  re-embedding every document).
- `internal/vector.Index.Upsert(ctx, collection, key, text)` embeds text via TEI (`POST /embed`)
  and upserts the resulting vector, creating the Qdrant collection first if needed. **Automatic
  invocation on every `PUT` is explicitly not wired up in this pass** — doing so needs a
  per-collection "which fields to embed" configuration surface this ADR never designed, and
  building one untested (no live Qdrant/TEI instance exists in this environment, per Out of Scope)
  would be exactly the speculative complexity the standing "Simplicity First" rule warns against.
  `Index.Upsert` is real, correct, and tested against a mock TEI/Qdrant — a caller (an operator's
  indexing job, or a future `PUT`-time hook once the field-selection design exists) can call it
  today. See docs/adr/BACKLOG.md §5.
- `SEARCH <collection> NEAR "text" [WHERE ...]` embeds the query text via TEI, queries Qdrant
  (`POST /collections/<c>/points/search`) for nearest neighbours, then hydrates full documents
  from `live` by the returned keys, applying `WHERE` as a further SQL predicate scoped to that key
  list (`internal/tql/executor.go`'s `execSearch`) — the same `json_extract` pushdown `FIND` uses,
  just against a key list instead of a full collection scan. This is the "same interface as KG"
  requirement: graph, vector, and plain filtering all terminate in one executor over one store.
  TQL's `SEARCH` grammar (D4) has no `AS OF` clause, so this hydrates current state only — a
  temporal `SEARCH ... AS OF` is not implemented (see docs/adr/BACKLOG.md §6 if wanted later).
- If `TEI_RERANK_URL` is also set, `internal/vector.TEIClient.Rerank` (`POST /rerank`) is available
  to score candidates before hydration — implemented and tested, not yet called from `execSearch`'s
  own path (same reasoning as automatic indexing: no live TEI reranker to verify it against).
- Both clients (`internal/vector/{qdrant,tei}.go`) are hand-rolled thin `net/http` wrappers over
  the 2–3 REST endpoints actually used — no SDK dependency for an optional, small surface. Verified
  against mock HTTP servers standing in for the real APIs (13 tests), and against the real
  `temporaldb-server` binary wired to fake-but-protocol-shaped Qdrant/TEI servers end to end:
  `PUT` a document, `internal/vector.Index.Upsert` it, `SEARCH` over HTTP returns the hydrated
  document.

### D7 — Backup: snapshot + streaming event-log shipping, with purge

Two complementary mechanisms, both driven off facts already true of the design (D2):

- **Snapshot:** periodic consistent full-database copy via SQLite's `VACUUM INTO '<path>'` (plain
  SQL, works through `modernc.org/sqlite` with no special API), written to a configured backup
  directory, timestamped and recorded with its high-water `seq`.
- **Streaming (continuous) backup — the litestream-like mechanism:** because every mutation is
  already an ordered, durable `events` row (D2), a background shipper polls for `seq > last-
  shipped` and appends new events to the configured sink (local file today; the sink is an
  interface, `internal/backup.Sink`, so object storage is a later implementation of the same
  interface, not a redesign) as newline-delimited JSON. This gives continuous, low-latency, off-
  process replication and point-in-time restore — the operational property Litestream provides —
  **without** parsing SQLite's WAL frame format. See Alternatives Considered for why literal WAL
  shipping was rejected for a from-scratch build.
- **Restore** = load the latest snapshot at-or-before a target `seq`/time, then replay shipped
  events after it, in order.
- **Purge**, `PURGE <collection> BEFORE <time>`: deletes `events` rows older than the cutoff
  (never touches `live` — current state is unaffected) and instructs the shipper to prune shipped
  segments before the same cutoff. This trades time-travel depth for storage; it is the ask's
  "purge option" on the streaming backup.

### D8 — Server: HTTP, one transport, TQL text is the wire protocol

`net/http` + `chi`. `POST /query` — request body is raw TQL (newline-separated for a batch);
response is JSON, `{"results":[...], "error"?: "..."}`. `GET /healthz`. No second, binary wire
protocol — the query language *is* the interface, so there is exactly one thing to keep readable
and one thing for the client, curl, and MCP to all speak.

**Correction (implementation, 2026-09-03): a batch is NOT one shared transaction.** This section
originally said a newline-separated batch executes "as one transaction". Building the executor
(D4) revealed that would require threading an externally-supplied `*sql.Tx` through every
`exec*` method and through `event.Store.Append`'s own transaction management — a real refactor
of the single-writer path (D13) — for a guarantee nothing in the original ask actually requested.
Statements in a batch instead run **sequentially, each in its own transaction** (same as sending
them as separate requests): execution stops at the first statement that errors, and the response
still carries every result collected before that point, so a caller can tell how far a script
got. `GET /backup/status` is also removed from this list — added speculatively here, it is
covered by D7's backup mechanism and belongs with that implementation (T7), not invented ahead of
it.

### D9 — Client package: a reference implementation of the wire protocol, nothing more

`client/client.go`, in the same module (`import "github.com/atvirokodosprendimai/temporaldbv1/
client"`). `client.New(addr string) *Client`, `(*Client).Query(ctx, tql string) (Result, error)`,
plus typed convenience wrappers (`Get`, `Put`, `Delete`, `Find`, `History`) that build a TQL
string and call `Query` — they expose nothing the server doesn't already expose over D8.

### D10 — MCP server: official `modelcontextprotocol/go-sdk`, vendored, thin wrapper over the client

`cmd/temporaldb-mcp` uses `github.com/modelcontextprotocol/go-sdk` — the protocol's own official
SDK (M's explicit direction, 2026-09-03: use the **origin** package, **vendored** —
`go mod vendor`, `vendor/` checked in — not the community `mark3labs/mcp-go` the house dialect
skill names, and not a hand-rolled JSON-RPC implementation), pinned to the latest stable tag
confirmed on the module proxy, `v1.7.0`. Tools exposed: `tql_query` (raw TQL passthrough — the
general-purpose escape hatch) plus typed conveniences `tql_get`, `tql_put`, `tql_history`,
`tql_search` mirroring D9, each a call through `client.Client` against a configured server
address (`TEMPORALDB_ADDR`). No logic is duplicated between the MCP tool handlers and the client
package; the exact registration API (tool schema struct shape, server run loop) is taken from the
SDK's own godoc at implementation time rather than guessed in this record.

### D11 — CLI: a REPL and a scriptable one-shot mode

`cmd/temporaldb-cli`, `urfave/cli/v3`. Talks to the server exclusively through `client.Client`
(D9) — never opens the SQLite file directly, preserving single-writer (D13). Modes: an
interactive REPL (each line is one TQL command), `-e "<command>"` for one shot, `-f script.tql`
for a file of commands. This is the "write commands in a CLI" requirement; the unfamiliar
external name in the ask ("schematix") is not otherwise resolved — see Context.

### D12 — Config: env + optional `.env`, no required file

`internal/config`, loaded via `godotenv.Load()` (silently continues if `.env` is absent — v1
scope only). Variables: `TEMPORALDB_ADDR` (default `:7777`), `TEMPORALDB_DATA_DIR`,
`TEMPORALDB_BACKUP_DIR`, `TEMPORALDB_BACKUP_INTERVAL`, `TEMPORALDB_RETENTION`, `QDRANT_URL`,
`QDRANT_API_KEY`, `TEI_URL`, `TEI_RERANK_URL`. Vector config (D6) is read once at startup;
absence of `QDRANT_URL`/`TEI_URL` is the documented, supported "vector search disabled" state,
not an error.

### D13 — Concurrency: single writer, WAL readers, matches D1/§1 of `cqrs`

One in-process mutex serializes the append-to-`events` + upsert-to-`live` path (the server process
is the only writer of the SQLite file — no client, including the CLI, ever opens it directly).
Reads go through WAL and are never blocked by the writer. This is "single writer, many readers,
per aggregate" (`cqrs` §1) with the aggregate being the whole database file, which is the correct
granularity for a single-file embedded store serving through one server process.

## Alternatives Considered

- **`mattn/go-sqlite3` (cgo driver):** rejected outright — the ask is explicit about no-cgo, and
  `modernc.org/sqlite` is already the house driver in three sibling repos.
- **`gorm.io` + `glebarez/sqlite` (the house default for new Go projects, per `effective-go`):**
  rejected for the storage core specifically. GORM is the right default for typical CRUD domain
  models (and every sibling project using it — `statsv1`, `agentsmemory` — is exactly that
  shape). TemporalDB's core is not a domain model with associations to persist; it *is* the
  storage engine, needing precise control of prepared statements, `json_each` pushdown, `ON
  CONFLICT` upserts (with the exact WHERE-predicate-repetition gotcha from Existing Primitives
  Audit), and transaction boundaries that an ORM's abstractions would obscure rather than help.
  This is a deliberate, named deviation from house default, not an oversight — flagged per the
  workspace's own "probe before inheriting a predicted design" lesson rather than applied or
  rejected by reflex either way.
- **`github.com/mark3labs/mcp-go`** (the community MCP package the `effective-go` skill names,
  and sibling `agentsmemory` depends on): this would have been the default by house dialect.
  **Superseded by M's explicit, project-scoped instruction** (2026-09-03, mid-session) to use the
  official `github.com/modelcontextprotocol/go-sdk` instead, vendored. A specific human decision
  for this project outranks a general skill default (`human-decisions` doctrine, loaded this
  session) — recorded here rather than silently overridden, since the skill's recommendation is
  otherwise still correct house guidance for other projects.
- **Hand-rolled MCP JSON-RPC** (no SDK dependency at all): the initial default reasoning before
  any human direction was received, on the grounds of avoiding a dependency for ~5 tools.
  Superseded the same way — an officially maintained SDK vendored into the tree is lower-risk than
  a hand-rolled protocol implementation, and vendoring (rather than a live module dependency)
  answers the original concern about build-time network reliance.
- **Literal Litestream-style SQLite WAL-frame shipping** (true page-level replication): considered
  because it is the closest thing to "litestream like" read literally. Rejected for a from-scratch
  build: it requires faithfully reimplementing SQLite's WAL frame format and page checksums, where
  a subtle bug is silent data corruption in backups nobody inspects until a restore is needed.
  Tailing the application's own already-ordered, already-durable `events` log (D7) gives the same
  operational property — continuous off-host replication, point-in-time restore — at far lower
  risk, because the shipped format is one this project owns and controls.
- **Embedding a real MongoDB query-filter syntax** (curly-brace `{field: {$gt: v}}`) instead of
  TQL: rejected. It is closer to the letter of "MongoDB style" but the ask separately says "simple
  and human readable... something like schematix where you can in cli write commands" — a typed
  verb-first line (`FIND users WHERE age > 21`) is more typeable at a prompt than nested braces
  and operators, and the "MongoDB style" reading applied instead to the *storage model* (schemaless
  JSON documents in collections) rather than to the *filter syntax*. Flagged as an interpretation
  call, not a silent one.
- **A typed projection table per collection** (columns inferred from a schema), instead of the
  generic `live.value` JSON blob + `json_extract`/`json_each` pushdown: rejected for v1 on the
  same "probe before inheriting a predicted design" basis documented in Existing Primitives Audit
  — no measurement yet shows the generic path is too slow, and building the second write path
  first would be exactly the invisible, untested cost that lesson describes. Deferred with a
  numbered trigger, not an open-ended "if it gets slow" — see Out of Scope.

## Component / Boundary Impact

| Package / binary | Owns | New? |
|---|---|---|
| `internal/storage` | `database/sql` setup, WAL pragmas, goose migrations, prepared statements | new |
| `internal/event` | `events`/`live` write path (single writer, D2/D13) | new |
| `internal/temporal` | `Visible()` and all AS-OF/interval comparison logic (D3) | new |
| `internal/tql` | lexer, parser, AST, executor — compiles TQL to SQL (D4) | new |
| `internal/graph` | edge collection helpers over `internal/event` (D5) | new |
| `internal/vector` | Qdrant + TEI thin clients, gated by config (D6) | new |
| `internal/backup` | snapshot, streaming shipper, purge, restore (D7) | new |
| `internal/server` | chi HTTP wiring over `internal/tql` (D8) | new |
| `internal/config` | env + `.env` loading (D12) | new |
| `client` (public) | `Client`, `Query`, typed wrappers (D9) | new |
| `cmd/temporaldb-server` | main: wires config → storage → server → backup | new |
| `cmd/temporaldb-cli` | REPL / `-e` / `-f`, over `client` (D11) | new |
| `cmd/temporaldb-mcp` | `modelcontextprotocol/go-sdk` tools over `client` (D10) | new |
| `vendor/` | vendored `github.com/modelcontextprotocol/go-sdk` and its transitive deps | new |

## Wiring & Contract Changes

| Surface | Change | Producer | Consumer(s) |
|---|---|---|---|
| `event.Store.Append(ctx, Event) (seq int64, err error)` | new write path, single-writer | `internal/event` | `internal/tql` executor, `internal/graph` |
| `temporal.Visible(events, asOf, validAt) (Event, bool)` | the one boundary-comparison function | `internal/temporal` | `internal/tql` executor, `internal/backup` restore |
| `tql.Parse(src string) (ast.Stmt, error)` | TQL text → AST | `internal/tql` | `internal/server`, tests |
| `tql.Executor.Exec(ctx, ast.Stmt) (Result, error)` | AST → SQL/Qdrant calls → `Result` | `internal/tql` | `internal/server` |
| `backup.Sink` interface (`WriteEvents`, `WriteSnapshot`, `Purge`) | pluggable backup destination | `internal/backup` | `cmd/temporaldb-server` (wires the concrete local-file sink today) |
| `client.Client.Query(ctx, tql string) (Result, error)` | the one client-server contract, mirrors D8's HTTP body | `client` | `cmd/temporaldb-cli`, `cmd/temporaldb-mcp` |
| MCP tool schemas (`tql_query`, `tql_get`, `tql_put`, `tql_history`, `tql_search`) | new MCP surface, via `modelcontextprotocol/go-sdk` | `cmd/temporaldb-mcp` | any MCP client (Claude Code, etc.) |

## Implementation

Inline tasks (no `tasks/` directory — this ADR is checked in `adr-lint`'s inline-task mode).
Executed in this order because each depends on the one before it compiling:

1. **T1 — Module + storage core:** `go.mod`, `internal/storage` (DB open, pragmas, goose
   migration `0001_init.sql` creating `events`/`live`), `internal/config`.
2. **T2 — Event log + projection:** `internal/event` (`Append`, single-writer mutex, `live`
   upsert with the partial-index-safe `ON CONFLICT`).
3. **T3 — Temporal semantics:** `internal/temporal` (`Visible`, half-open interval logic), unit
   tests for the exact inclusive-end bug class documented in Existing Primitives Audit.
4. **T4 — TQL front end:** `internal/tql/{lexer,parser,ast}.go` for the full grammar in D4.
5. **T5 — TQL executor:** compiles AST to SQL (`json_extract`/`json_each`) against
   `internal/event`/`internal/temporal`; `internal/graph` edges; vector `SEARCH` wired to
   `internal/vector` when configured.
6. **T6 — Server + client:** `internal/server` (chi, `POST /query`, `/healthz`), `client` package,
   `cmd/temporaldb-server`.
7. **T7 — Backup:** `internal/backup` (snapshot via `VACUUM INTO`, streaming shipper, purge,
   restore), wired into `cmd/temporaldb-server`.
8. **T8 — CLI:** `cmd/temporaldb-cli` (REPL, `-e`, `-f`).
9. **T9 — MCP server:** vendor `github.com/modelcontextprotocol/go-sdk` (`go mod vendor`),
   `cmd/temporaldb-mcp`.
10. **T10 — Vector clients:** `internal/vector/{qdrant,tei}.go`, exercised only when `QDRANT_URL`/
    `TEI_URL` are set; a documented manual/integration test path when they are not (no live
    Qdrant/TEI instance is available in this environment to test against end-to-end).

Each task lands with `go build ./...`, `go vet ./...`, and `go test ./...` green before the next
starts (Verified Execution, per this repo's own `CLAUDE.md`).

## Consequences

- **Positive:** one storage mechanism (`events`) simultaneously provides time travel (D3), the
  knowledge graph (D5), and streaming backup (D7) — there is no second subsystem to keep in sync,
  because all three are views over the same append-only log.
- **Positive:** vector search is strictly additive and the server is fully testable and useful
  with `QDRANT_URL`/`TEI_URL` unset, satisfying the ask's "if not..." requirement directly.
- **Positive:** the wire protocol, the CLI, and MCP all reduce to "send TQL text" — one thing to
  document, one thing to keep human-readable.
- **Negative:** `json_extract`/`json_each` pushdown means `FIND` performance on large collections
  depends on SQLite's ability to optimize JSON functions rather than on typed, indexed columns —
  accepted per Alternatives Considered, with a named trigger for revisiting (Out of Scope).
- **Negative:** the streaming backup is not literal WAL shipping, so a restore replays this
  project's own event format, not a stock SQLite file byte-for-byte — accepted; the snapshot
  (`VACUUM INTO`) side already produces a stock, directly-openable SQLite file as the periodic
  anchor.
- **Neutral:** deviating from `gorm.io` for the storage core, and from `mark3labs/mcp-go` for MCP,
  means this repository has two named non-house-default technical choices — both explicitly
  recorded with their reason rather than blended in silently.

## Out of Scope

- Multi-primary / distributed consensus replication. (permanent: boundary: this ADR specifies a
  single-writer server with a streaming backup sink, per D13; multi-primary is a different
  product with a different consistency model.)
- Authentication / authorization on the server API. (deferred: docs/adr/BACKLOG.md §1)
- A cost-based query planner or secondary indexes beyond the primary key and JSON pushdown.
  (deferred: docs/adr/BACKLOG.md §2)
- A typed per-collection projection table (see Alternatives Considered). (deferred:
  docs/adr/BACKLOG.md §3)
- Literal SQLite WAL-frame-level replication. (permanent: boundary: rejected in Alternatives
  Considered — the risk of a from-scratch, unverified WAL-frame reimplementation silently
  corrupting backups outweighs the benefit of byte-identical replicas.)
- Multi-tenancy (more than one logical database per server process). (permanent: boundary: not
  requested; a data directory is one database, per D12.)
- `OR`, subqueries, and joins in TQL. (deferred: docs/adr/BACKLOG.md §4)
- End-to-end testing of the Qdrant/TEI integration against live instances. (permanent: boundary:
  no Qdrant or TEI instance is reachable from this development environment to test against, and
  standing one up is outside this ADR's scope — checked 2026-09-03, no `QDRANT_URL`/`TEI_URL`
  configured and no such service running locally.)

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| `json_extract`/`json_each` filtering does not scale past some collection size | Med | Med | Trigger deferred with a number, not "if it gets slow" — docs/adr/BACKLOG.md §3 |
| Streaming backup format (app-level event JSON) is not a drop-in replacement for a stock SQLite file | Low | Med | Snapshot side (`VACUUM INTO`) always produces a stock-openable file as the restore anchor |
| Bitemporal interval bugs (the documented gotcha class) reappear at a new call site | Low | High | All boundary comparisons funnel through one function (`temporal.Visible`), unit-tested against the exact failure shape from Existing Primitives Audit |
| Vector integration is unverified against a live Qdrant/TEI (Out of Scope) | Med | Low | Interfaces are narrow and hand-rolled against documented REST contracts; wired behind a config gate so failure mode is "feature absent," not "server broken" |
| Official MCP SDK's exact Go API differs from what D10 assumes | Low | Low | D10 defers exact call shapes to the SDK's own godoc at implementation time rather than guessing them here |
| Deviating from `gorm.io`/`mark3labs/mcp-go` diverges from house convention a future session expects | Low | Low | Both named explicitly in Alternatives Considered and Consequences, not silent |

## Rollback

Greenfield repository, first ADR: rollback is `git revert` of the commit(s) implementing this
ADR, or simply not merging the branch. No migration of existing data, no external integration,
and no other system depends on this repository yet.

## Follow-ups

- [ ] M accepts or rejects this ADR (Proposed — not this session's to accept, per this
      workspace's own convention for agent-proposed records).
- [ ] Confirm the "schematix" reference (Context) — if it names a specific existing tool, TQL's
      CLI ergonomics (D11) may need to be reconciled with it after the fact.
- [ ] Decide whether a future "watch" / change-feed TQL verb (streaming query results over the
      event log) is wanted — NATS is house convention for pub/sub but nothing in the current ask
      requests live push, so it is not designed here.
