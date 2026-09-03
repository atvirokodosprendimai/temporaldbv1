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
