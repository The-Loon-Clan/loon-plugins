-- Walk-past evictions join the completion-distance series (kind 'evicted'),
-- and every resolution row now carries the set's base subject. Two reasons:
-- the window derivation must see the sets the sweep DESTROYS, not only the
-- ones that resolved happily (deriving p99.9 from survivors alone biases the
-- window short — exactly the mistake the series exists to prevent), and for
-- an evicted set this row is the only record of what was removed, so it must
-- be identifiable when an operator asks "did the sweep eat release X?".
ALTER TABLE set_resolutions ADD COLUMN IF NOT EXISTS base_subject TEXT NOT NULL DEFAULT '';
