# temporaldbv1

A temporal, event-sourced document database on pure-Go SQLite (no CGO). One
append-only event log is the source of truth for time travel, a knowledge
graph of relations, and streaming backup — see
[`docs/adr/ADR-001-the-event-log-is-the-database.md`](docs/adr/ADR-001-the-event-log-is-the-database.md)
for the full design and rationale.

## What's here

- **TQL** — a small, human-readable query language (not SQL): `GET`, `FIND
  ... WHERE`, `PUT`, `DELETE`, `HISTORY`, `RELATE`/`UNRELATE`/`RELATED` (graph
  edges), `SEARCH` (vector, optional), `PURGE`. This is the one wire protocol
  the server, CLI, and MCP tools all speak.
- **Time travel** — every write is versioned; `AS OF "<time>"` (or `BETWEEN
  ... AND ...` for `HISTORY`) queries the database as it stood at any past
  instant.
- **A knowledge graph** — `RELATE`/`RELATED` are relations between documents,
  versioned through the same event log as everything else.
- **Vector search** — optional. Set `QDRANT_URL` and `TEI_URL` to enable
  `SEARCH`; the server is fully functional without them.
- **Streaming backup** — a continuous shipper plus periodic consistent
  snapshots, with `PURGE` to prune history and `backup.Restore` to rebuild a
  database from backups, preserving original commit times exactly.
- A **server**, a **Go client** package, a **CLI**, and an **MCP server** —
  all built on the same client-to-server TQL protocol.

## Quick start

```sh
go build -o temporaldb-server ./cmd/temporaldb-server
go build -o temporaldb-cli ./cmd/temporaldb-cli

./temporaldb-server &            # listens on :7777, data in ./data by default
./temporaldb-cli -e 'PUT users/1 {"name":"Ada"}'
./temporaldb-cli -e 'FIND users WHERE name = "Ada"'
./temporaldb-cli                 # interactive REPL
```

Or talk to it directly over HTTP — the wire body is raw TQL:

```sh
curl -X POST localhost:7777/query --data 'GET users/1 AS OF "2026-01-01"'
```

## TQL tutorial

TQL (Temporal Query Language) is TemporalDB's one query language — the wire
protocol, the CLI's command language, and the MCP tools all speak it. It reads
as a verb, a target, and clauses — not SQL, not a JSON filter object. Every
example below works as-is with `temporaldb-cli -e '...'` or
`curl -X POST localhost:7777/query --data '...'`.

### Documents: GET, FIND, PUT, DELETE

Every document lives at `<collection>/<key>`, and its value is a JSON object.

```
PUT users/1 {"name":"Ada","age":30,"city":"NYC"}
GET users/1
DELETE users/1
```

`FIND` scans a collection, optionally filtered, ordered, and limited:

```
FIND users
FIND users WHERE age > 21
FIND users WHERE age > 21 AND city = "NYC"
FIND users ORDER BY age DESC LIMIT 10
```

`WHERE` supports `=`, `!=`, `<`, `<=`, `>`, `>=`, `IN`, and `CONTAINS`:

```
FIND users WHERE city IN ["NYC", "LA", "SF"]
FIND docs WHERE labels CONTAINS "urgent"
```

A value can be a string, a number, `true`/`false`, `null`, or a `[...]`
array. Field names can reach into nested JSON with dots:
`WHERE address.city = "NYC"`.

Keys are usually bare (`users/1`, `users/alice`), but a key containing a
hyphen — a UUID, say — must be quoted, since TQL's bare identifiers never
contain `-` (that's what keeps `RELATE`'s `-knows->` arrow unambiguous):

```
GET users/"550e8400-e29b-41d4-a716-446655440000"
```

### Time travel: AS OF, AT, HISTORY

Every write is versioned. Read it back as of any past instant with `AS OF`,
and write into the past with `AT` — both take a quoted RFC3339 instant or a
bare date (`"2026-01-01"`, midnight UTC):

```
PUT users/1 {"name":"Ada","age":30} AT "2026-01-01"
PUT users/1 {"name":"Ada","age":31} AT "2026-06-01"

GET users/1 AS OF "2026-03-01"          # {"name":"Ada","age":30} - the version then current
FIND users AS OF "2026-03-01"           # AS OF works on FIND too, across the whole collection
```

`HISTORY` lists every version of one key, oldest first, optionally bounded:

```
HISTORY users/1
HISTORY users/1 BETWEEN "2026-01-01" AND "2026-05-01"
```

### The knowledge graph: RELATE, UNRELATE, RELATED

Relations between documents are edges — versioned through the same event log
as everything else, so they have history too. The arrow syntax is
`-<edge-type>->`:

```
RELATE users/1 -knows-> users/2
RELATE users/1 -knows-> users/2 {"since":2020}      # edges can carry properties
UNRELATE users/1 -knows-> users/2

RELATED users/1                    # every edge from users/1, any type
RELATED users/1 -knows->           # only "knows" edges
RELATED users/1 -knows-> LIMIT 5
RELATED users/1 AS OF "2026-01-01" # the graph as it stood on that date
```

### Vector search: SEARCH (optional)

`SEARCH` finds documents by meaning rather than exact match. It requires the
server to have `QDRANT_URL` and `TEI_URL` configured (see Configuration
below); without them it returns a clear error rather than degrading
silently. It composes with `WHERE`, just like `FIND`:

```
SEARCH docs NEAR "golang concurrency patterns"
SEARCH docs NEAR "golang concurrency patterns" WHERE lang = "en" LIMIT 5
```

Documents are made searchable with `internal/vector.Index.Upsert(ctx,
collection, key, text)` — nothing calls this automatically on `PUT` yet (see
`docs/adr/BACKLOG.md` §5), so an indexing job (or a future write-time hook)
needs to call it for whatever should be findable by `SEARCH`.

### Retention: PURGE

`PURGE` deletes history older than a cutoff — the "purge option" on
TemporalDB's streaming backup. It only removes old *versions*; the current
value (and anything already backed up) is untouched:

```
PURGE users BEFORE "2025-01-01"
```

### Batches

Send several statements in one request, separated by newlines. They run
sequentially, each in its own transaction — not atomically as a whole — and
execution stops at the first one that errors:

```sh
temporaldb-cli -e '
PUT users/1 {"name":"Ada"}
PUT users/2 {"name":"Grace"}
FIND users
'
```

### Full grammar reference

```
GET      <collection>/<key> [AS OF <time>]
FIND     <collection> [WHERE <expr>] [AS OF <time>] [ORDER BY <field> [ASC|DESC]] [LIMIT <n>]
PUT      <collection>/<key> <json-object> [AT <time>]
DELETE   <collection>/<key>
HISTORY  <collection>/<key> [BETWEEN <time> AND <time>]
RELATE   <collection>/<key> -<edge-type>-> <collection>/<key> [<json-object>]
UNRELATE <collection>/<key> -<edge-type>-> <collection>/<key>
RELATED  <collection>/<key> [-<edge-type>->] [AS OF <time>] [LIMIT <n>]
SEARCH   <collection> NEAR "<text>" [WHERE <expr>] [LIMIT <n>]
PURGE    <collection> BEFORE <time>

<expr>  := <field> <op> <value> [AND <expr>]
<op>    := = | != | < | <= | > | >= | IN | CONTAINS
<value> := string | number | true | false | null | [<value>, ...]
<time>  := "<RFC3339 instant>" | "<YYYY-MM-DD>"
```

Not in v1 — see `docs/adr/BACKLOG.md`: `OR`/subqueries/joins in `WHERE` (§4),
and `SEARCH ... AS OF` (§6).

## Configuration

Environment variables (or a `.env` file):

| Variable | Default | Purpose |
|---|---|---|
| `TEMPORALDB_ADDR` | `:7777` | server listen address |
| `TEMPORALDB_DATA_DIR` | `./data` | SQLite database location |
| `TEMPORALDB_BACKUP_DIR` | `./data/backup` | streaming backup / snapshot destination |
| `TEMPORALDB_BACKUP_INTERVAL` | `30s` | streaming-ship cadence |
| `TEMPORALDB_SNAPSHOT_INTERVAL` | `1h` | full-snapshot cadence |
| `TEMPORALDB_RETENTION` | `0` (disabled) | age-based purge |
| `QDRANT_URL`, `QDRANT_API_KEY` | unset | enables vector search when set with `TEI_URL` |
| `TEI_URL`, `TEI_RERANK_URL` | unset | Text Embeddings Inference endpoints |

## Development

```sh
go build ./...
go vet ./...
go test ./...
```

Builds with `CGO_ENABLED=0` — no C toolchain required. `vendor/` is checked
in, so `go build`/`go test` work offline once cloned.

## Layout

```
internal/storage    SQLite connection, WAL pragmas, goose migrations
internal/event       the event log + live projection (the single writer)
internal/temporal    bitemporal visibility (AS OF / VALID AT)
internal/tql         TQL lexer, parser, and executor
internal/graph       relations (RELATE/RELATED), same event log
internal/vector      Qdrant + TEI clients (optional)
internal/backup      streaming shipper, snapshots, restore
internal/server      HTTP server (TQL in, JSON out)
internal/config      environment / .env loading
client/              Go client package
cmd/temporaldb-server, cmd/temporaldb-cli, cmd/temporaldb-mcp
```
