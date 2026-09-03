-- +goose Up

-- live must carry meta too: it is a projection of the latest event per
-- key, and dropping meta from that projection silently lost information
-- events keeps (e.g. graph.Store.Related's json_extract(meta,...) scan
-- needs it directly, without replaying history for the common case).
ALTER TABLE live ADD COLUMN meta TEXT NOT NULL DEFAULT '{}';

-- +goose Down

ALTER TABLE live DROP COLUMN meta;
