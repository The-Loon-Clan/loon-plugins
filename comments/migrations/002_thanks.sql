-- Thanks: one member telling another that what they said was useful.
--
-- The classic form of this on a site of this kind is thanking an UPLOAD, and
-- that is not available here: this index is crawled, so a release has no
-- uploader to credit. A comment always has an author, which makes it the one
-- place a thanks can actually reach a person.
--
-- ONE ROW PER (comment, member), and it is never deleted. Withdrawing sets
-- withdrawn_at rather than removing the row, and that is what makes the points
-- safe: the award happens when the row is CREATED, so thank → withdraw →
-- thank again finds the row already there and pays nothing. Deleting on
-- withdrawal would turn the button into a faucet.
CREATE TABLE IF NOT EXISTS comment_thanks (
    comment_id   BIGINT NOT NULL REFERENCES comments(id) ON DELETE CASCADE,
    user_id      BIGINT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Set when taken back, cleared when given again. The row's existence means
    -- "this member has thanked this comment at some point"; this column means
    -- "and it still stands".
    withdrawn_at TIMESTAMPTZ,
    PRIMARY KEY (comment_id, user_id)
);

-- The count under each comment, and "did I thank this" for the viewer. Partial
-- on the ones that still stand, since a withdrawn thanks is not one a count
-- should claim.
CREATE INDEX IF NOT EXISTS comment_thanks_live_idx
    ON comment_thanks (comment_id) WHERE withdrawn_at IS NULL;
