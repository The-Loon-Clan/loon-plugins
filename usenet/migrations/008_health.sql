-- NZB health: is this release still downloadable?
--
-- An NZB is a list of message-ids; Usenet providers expire articles, so a
-- release that assembled fine last month may be partly or wholly gone. The
-- health job STATs each segment and records what it found.
--
-- health_status:
--   unknown  never checked, or the last check was too inconclusive to trust
--   healthy  every data segment present
--   broken   some data segments missing, but no more than the surviving PAR2
--            segments could repair
--   dead     more data segments missing than PAR2 can repair
--
-- Counts are kept alongside the label so the UI can show "12 of 900 missing"
-- rather than just a colour, and so a later change to the scoring rule can be
-- re-derived without re-STATing the archive.
ALTER TABLE nzbs ADD COLUMN IF NOT EXISTS health_status        TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE nzbs ADD COLUMN IF NOT EXISTS total_segments       INT  NOT NULL DEFAULT 0;
ALTER TABLE nzbs ADD COLUMN IF NOT EXISTS missing_segments     INT  NOT NULL DEFAULT 0;
ALTER TABLE nzbs ADD COLUMN IF NOT EXISTS par2_segments        INT  NOT NULL DEFAULT 0;
ALTER TABLE nzbs ADD COLUMN IF NOT EXISTS last_health_check_at TIMESTAMPTZ;

-- Drives the "what needs checking next" query: never-checked first, then oldest.
CREATE INDEX IF NOT EXISTS idx_nzbs_health_due
    ON nzbs (last_health_check_at NULLS FIRST)
    WHERE status = 'completed';
