-- What actually left the building.
--
-- Everything else in this schema describes what production HAS. This table is
-- the only record of what something else successfully took away, and it is the
-- answer to the question the other tables cannot reach: "is my backup
-- happening?" A pull that silently stopped a month ago is indistinguishable
-- from one that ran last night, unless the puller says so.
--
-- One row per (generation, source): several backup targets are distinguishable,
-- and one of them going quiet stays visible instead of being masked by another.
CREATE TABLE IF NOT EXISTS acks (
    generation BIGINT      NOT NULL,
    source     TEXT        NOT NULL DEFAULT '',
    acked_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    packs      BIGINT      NOT NULL DEFAULT 0,
    bytes      BIGINT      NOT NULL DEFAULT 0,
    PRIMARY KEY (generation, source)
);
CREATE INDEX IF NOT EXISTS idx_acks_at ON acks (acked_at DESC);
