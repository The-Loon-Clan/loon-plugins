-- Comments, on whatever the host says the page is about.
--
-- Keyed by (subject_kind, subject_id) rather than by release_id, and that is
-- the load-bearing decision in this table.
--
-- A release on this site can also exist as a torrent on the tracker, and the
-- two have different ids. A comment belongs to the RELEASE — it is about the
-- encode, the audio, whether the pack is complete — and a schema that keyed on
-- whichever id the page happened to have would strand the conversation the day
-- somebody mirrored it. The kind names the domain; the id is that domain's own.
--
-- It also means the next thing that wants comments (a series, a group, a
-- collection) is a new value in one column rather than a second table with the
-- same six problems solved slightly differently.
CREATE TABLE IF NOT EXISTS comments (
    id           BIGSERIAL PRIMARY KEY,
    subject_kind TEXT   NOT NULL,
    subject_id   BIGINT NOT NULL,
    user_id      BIGINT NOT NULL,
    body         TEXT   NOT NULL,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Set when the author edits. Shown, because a comment that changed after
    -- people replied to it is a different comment, and hiding that is how a
    -- thread stops making sense.
    edited_at    TIMESTAMPTZ,

    -- SOFT delete. The row stays and the body is withheld, so a thread keeps
    -- its shape — "this was removed" between two replies is legible, where a
    -- vanished row makes the replies read as non-sequiturs. It is also what
    -- lets staff see what was said after the author removed it.
    deleted_at   TIMESTAMPTZ,
    deleted_by   BIGINT
);

-- The only read that matters: one subject's comments, oldest first, because a
-- conversation is read in the order it happened.
CREATE INDEX IF NOT EXISTS comments_subject_idx
    ON comments (subject_kind, subject_id, created_at);

-- "How many does this one have", for a listing that wants a count without the
-- bodies. Partial, because a deleted comment is not one a count should claim.
CREATE INDEX IF NOT EXISTS comments_live_idx
    ON comments (subject_kind, subject_id)
    WHERE deleted_at IS NULL;

-- A member's own comments — for their profile, and for a moderator reading
-- everything one account has said.
CREATE INDEX IF NOT EXISTS comments_author_idx ON comments (user_id, created_at DESC);
