-- Grouping observability (observe-only, no behaviour change).
--
-- subject_corpus: a rolling sample of RAW subjects as the crawl saw them,
-- with the parser's verdict alongside. This is the differential-testing
-- substrate: any change to parseSubject or the junk grammar replays the
-- corpus and diffs verdicts instead of hand-tracing (the method the
-- reFileOfBare/430k-title audit proved). residue marks subjects the parser
-- recognised NO counter in — the cohort where new formats like "{ 1 | 100 }"
-- hide. Pruned on the prune job's horizon; sampled at ingest so the table
-- stays small however large the crawl.
CREATE TABLE IF NOT EXISTS subject_corpus (
    id         BIGSERIAL PRIMARY KEY,
    group_name TEXT        NOT NULL,
    subject    TEXT        NOT NULL,
    residue    BOOLEAN     NOT NULL DEFAULT FALSE,
    seen_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_subject_corpus_seen ON subject_corpus (seen_at);
CREATE INDEX IF NOT EXISTS idx_subject_corpus_residue ON subject_corpus (residue) WHERE residue;
