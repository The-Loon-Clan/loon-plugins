-- Keeps were stored as junk_rule = '' rather than NULL.
--
-- Migration 037 added the column and every reader asks "junk_rule IS NOT NULL"
-- to mean "this subject was dropped". But the insert binds Go's zero value, and
-- for a KEPT subject that is "" -- which Postgres stores as an empty string.
-- An empty string is not null, so the keeps answered yes: the drop list showed
-- 100% of sampled subjects as junk, and the recovery probe would have spent
-- provider bytes fetching the bodies of releases that were indexed correctly.
--
-- Found in production 17 minutes after 037 shipped, by reading the rule
-- breakdown of what the column had actually recorded: 1,137 of 5,102 rows in
-- the first window carried '' with sample subjects like `"payload" yEnc` that
-- no rule drops. The bind now applies nullif(), so this repairs the rows
-- written in between.
UPDATE subject_corpus SET junk_rule  = NULL WHERE junk_rule  = '';
UPDATE subject_corpus SET message_id = NULL WHERE message_id = '';
