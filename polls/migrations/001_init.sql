-- Polls: a question, some options, and one vote per member.
--
-- Staff use them for rule changes, members for arguments, and neither wants a
-- forum thread — a thread tells you who is loudest, a poll tells you what
-- people think.
CREATE TABLE IF NOT EXISTS polls (
    id       BIGSERIAL PRIMARY KEY,

    -- The SLUG is how a placement names a poll, and it is why polls are
    -- placeable at all: the widget takes a slug as its per-placement config,
    -- so the same widget in the sidebar and in a page body is two different
    -- polls. An id would work and would be unreadable in a shortcode.
    slug     TEXT NOT NULL UNIQUE,
    question TEXT NOT NULL,

    -- When results become readable. Three values because they are three real
    -- editorial choices and a boolean cannot hold them:
    --   'after_vote' — the default, and the one that keeps a poll honest:
    --                  seeing the running tally before you answer moves how
    --                  you answer.
    --   'always'     — a temperature check where the tally IS the point.
    --   'on_close'   — a vote where early numbers would campaign for one side.
    results  TEXT NOT NULL DEFAULT 'after_vote'
             CHECK (results IN ('after_vote', 'always', 'on_close')),

    created_by BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Two ways to end, and they are different facts. closes_at is a plan made
    -- when the poll opened; closed_at is somebody deciding it is over. Keeping
    -- both means "closed early" and "ran its course" stay tellable apart.
    closes_at  TIMESTAMPTZ,
    closed_at  TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS poll_options (
    id      BIGSERIAL PRIMARY KEY,
    poll_id BIGINT NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
    -- The order an operator wrote them in. Not alphabetical and not by vote
    -- count: a ballot that reorders itself as votes arrive is one where the
    -- leader is always on top, which is its own campaign.
    ordinal INT  NOT NULL DEFAULT 0,
    label   TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS poll_options_poll_idx ON poll_options (poll_id, ordinal);

-- ONE ROW PER MEMBER PER POLL, so changing your mind is an UPDATE and not a
-- second vote. The primary key is what enforces it — a count that has to
-- deduplicate is a count somebody will forget to deduplicate.
CREATE TABLE IF NOT EXISTS poll_votes (
    poll_id   BIGINT NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
    user_id   BIGINT NOT NULL,
    option_id BIGINT NOT NULL REFERENCES poll_options(id) ON DELETE CASCADE,
    voted_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (poll_id, user_id)
);

-- The tally: every vote for one poll, grouped by option.
CREATE INDEX IF NOT EXISTS poll_votes_tally_idx ON poll_votes (poll_id, option_id);
