-- +goose Up

-- events is the append-only log and the system of record (ADR-001 D2).
-- Nothing ever updates or deletes a row here except PURGE (ADR-001 D7).
CREATE TABLE events (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    collection  TEXT    NOT NULL,
    key         TEXT    NOT NULL,
    op          TEXT    NOT NULL CHECK (op IN ('put','delete')),
    value       BLOB,
    valid_from  TEXT    NOT NULL,
    tx_time     TEXT    NOT NULL,
    meta        TEXT    NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_events_ck_seq ON events(collection, key, seq);

-- live is a materialized projection of events, kept in the same
-- transaction as the events insert (ADR-001 D2): current state without
-- replaying history.
CREATE TABLE live (
    collection  TEXT    NOT NULL,
    key         TEXT    NOT NULL,
    value       BLOB,
    valid_from  TEXT    NOT NULL,
    tx_time     TEXT    NOT NULL,
    deleted     INTEGER NOT NULL DEFAULT 0,
    seq         INTEGER NOT NULL,
    PRIMARY KEY (collection, key)
);

-- +goose Down

DROP TABLE live;
DROP TABLE events;
