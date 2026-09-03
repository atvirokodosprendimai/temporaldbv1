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
FIND users ORDER BY age DESC LIMIT 10 OFFSET 20    # page 3, 10 per page
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
```

`RELATED` walks the edges touching a node — by default the ones pointing
*out* of it, matching the arrow. `DIRECTION` follows edges the other way
(`IN`) or either way (`BOTH`); a bracketed list filters to several edge
types at once (OR semantics, since one edge has exactly one type);
`LIMIT`/`OFFSET` page through the rest, same as `FIND`:

```
RELATED users/1                            # every out-edge from users/1, any type
RELATED users/1 -knows->                   # only "knows" edges out of users/1
RELATED users/1 -[knows,blocks]->          # "knows" OR "blocks" out of users/1
RELATED users/1 DIRECTION IN               # every edge pointing INTO users/1
RELATED users/1 DIRECTION BOTH             # in- and out-edges, either direction
RELATED users/1 -knows-> LIMIT 5 OFFSET 10 # page 3, 5 per page
RELATED users/1 AS OF "2026-01-01"         # the graph as it stood on that date
```

`DIRECTION IN`/`BOTH` is what makes nested containment queryable both ways —
given `boxes/2 -contains-> boxes/3`, `RELATED boxes/3 DIRECTION IN` finds
what contains a box without already knowing its container's key.

`EDGETYPES` lists every distinct edge type currently in use, sorted — useful
for discovering what relation vocabulary a graph actually contains:

```
EDGETYPES     # e.g. ["blocks","knows"]
```

### Vector search: SEARCH (optional)

`SEARCH` finds documents by meaning rather than exact match. It requires the
server to have `QDRANT_URL` and `TEI_URL` configured (see Configuration
below); without them it returns a clear error rather than degrading
silently. It composes with `WHERE`, just like `FIND`:

```
SEARCH docs NEAR "golang concurrency patterns"
SEARCH docs NEAR "golang concurrency patterns" WHERE lang = "en" LIMIT 5
SEARCH docs NEAR "golang concurrency patterns" WHERE lang = "en" LIMIT 5 OFFSET 10
```

Documents are made searchable with `internal/vector.Index.Upsert(ctx,
collection, key, text)` — nothing calls this automatically on `PUT` yet (see
`docs/adr/BACKLOG.md` §5), so an indexing job (or a future write-time hook)
needs to call it for whatever should be findable by `SEARCH`.

### Retention: PURGE

`PURGE` deletes history older than a cutoff — the "purge option" on
TemporalDB's streaming backup. It removes old *versions* from both the
primary event log and the configured backup sink; the current value is
untouched:

```
PURGE users BEFORE "2025-01-01"
```

Set `TEMPORALDB_RETENTION` to purge automatically on a schedule instead of
calling `PURGE` by hand — see Configuration below.

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
FIND     <collection> [WHERE <expr>] [AS OF <time>] [ORDER BY <field> [ASC|DESC]] [LIMIT <n>] [OFFSET <n>]
PUT      <collection>/<key> <json-object> [AT <time>]
DELETE   <collection>/<key>
HISTORY  <collection>/<key> [BETWEEN <time> AND <time>]
RELATE   <collection>/<key> -<edge-type>-> <collection>/<key> [<json-object>]
UNRELATE <collection>/<key> -<edge-type>-> <collection>/<key>
RELATED  <collection>/<key> [<edge-clause>] [DIRECTION OUT|IN|BOTH] [AS OF <time>] [LIMIT <n>] [OFFSET <n>]
EDGETYPES
SEARCH   <collection> NEAR "<text>" [WHERE <expr>] [LIMIT <n>] [OFFSET <n>]
PURGE    <collection> BEFORE <time>

<expr>        := <field> <op> <value> [AND <expr>]
<op>          := = | != | < | <= | > | >= | IN | CONTAINS
<value>       := string | number | true | false | null | [<value>, ...]
<time>        := "<RFC3339 instant>" | "<YYYY-MM-DD>"
<edge-clause> := -<edge-type>-> | -[<edge-type>, <edge-type>, ...]->
```

Not in v1 — see `docs/adr/BACKLOG.md`: `OR`/subqueries/joins in `WHERE` (§4),
and `SEARCH ... AS OF` (§6).

## MCP server

`cmd/temporaldb-mcp` exposes TemporalDB to MCP clients (Claude Code, Claude
Desktop, or any MCP-speaking agent) over stdio, using the official
[`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk)
(vendored). It's a thin wrapper over the same `client` package the CLI uses —
it talks to a running `temporaldb-server`; it never opens the database file
itself.

### Running it

```sh
go build -o temporaldb-mcp ./cmd/temporaldb-mcp
```

It reads `TEMPORALDB_ADDR` for which server to connect to (default
`http://localhost:7777`) and speaks MCP over stdin/stdout — an MCP client
launches it as a subprocess; it isn't a service you start and leave running
yourself.

### Configuring an MCP client

Point the client at the built binary. For Claude Code, add to `.mcp.json` (or
run `claude mcp add`):

```json
{
  "mcpServers": {
    "temporaldb": {
      "command": "/path/to/temporaldb-mcp",
      "env": { "TEMPORALDB_ADDR": "http://localhost:7777" }
    }
  }
}
```

### Tools

Every call and result is JSON. `<value>` below is one document:
`{"collection", "key", "value", "valid_from", "tx_time"}`; `<edge>` is one
relation: `{"from", "type", "to", "props", "seq", "valid_from", "tx_time"}`.

**`tql_query`** — run raw TQL (see the tutorial above). The general-purpose
escape hatch for anything a typed tool below doesn't cover.
- In: `{"tql": "<one or more newline-separated TQL statements>"}`
- Out: `{"results": [{"rows": [<value>, ...], "edges": [<edge>, ...], "purged": N}, ...]}` — one entry per statement, in order

**`tql_get`** — fetch the current value of one document.
- In: `{"collection": "...", "key": "..."}`
- Out: `{"found": true, "value": <value>}` or `{"found": false}`

**`tql_put`** — create or replace one document. `value` must be a JSON object.
- In: `{"collection": "...", "key": "...", "value": {...}}`
- Out: `{"value": <value>}`

**`tql_history`** — every version of one document, oldest first.
- In: `{"collection": "...", "key": "..."}`
- Out: `{"versions": [<value>, ...]}`

**`tql_search`** — vector search. Requires the server to have `QDRANT_URL`
and `TEI_URL` configured; otherwise the call errors, explaining that.
- In: `{"collection": "...", "query": "...", "where": "<optional TQL WHERE clause, no WHERE keyword>", "limit": N}`
- Out: `{"results": [<value>, ...]}`

**`tql_edge_types`** — every distinct relation type currently in use across
the whole graph, sorted.
- In: `{}`
- Out: `{"types": ["...", ...]}`

None of these duplicate TQL-compilation logic — each is one call through the
same `client.Client` the CLI uses, so the MCP surface can never drift from
what the server actually does.

## Configuration

Environment variables (or a `.env` file):

| Variable | Default | Purpose |
|---|---|---|
| `TEMPORALDB_ADDR` | `:7777` | server listen address |
| `TEMPORALDB_DATA_DIR` | `./data` | SQLite database location |
| `TEMPORALDB_BACKUP_DIR` | `./data/backup` | streaming backup / snapshot destination |
| `TEMPORALDB_BACKUP_INTERVAL` | `30s` | streaming-ship cadence |
| `TEMPORALDB_SNAPSHOT_INTERVAL` | `1h` | full-snapshot cadence |
| `TEMPORALDB_RETENTION` | `0` (disabled) | age-based purge, swept every `TEMPORALDB_SNAPSHOT_INTERVAL` |
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
