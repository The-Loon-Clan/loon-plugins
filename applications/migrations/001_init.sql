-- Applications to join.
--
-- A closed site's front door. Somebody who cannot get an invite from a member
-- asks the site directly, staff read the answer, and an approval issues a real
-- invite through the host — so an accepted applicant joins the same way every
-- other member does, and the invite chain records who approved them.
--
-- The row is kept after the decision. "Why did we let this person in" is the
-- same question the invite chain exists to answer, one step earlier, and an
-- application queue that deleted rejections would lose the reason somebody was
-- turned away the week before they reapply.
CREATE TABLE IF NOT EXISTS applications (
    id          BIGSERIAL PRIMARY KEY,

    -- What they told us. The email is the identity here: it is what an
    -- approval issues the invite to, and what stops one person filling the
    -- queue with variations of themselves.
    email       TEXT NOT NULL,
    username    TEXT NOT NULL DEFAULT '',
    -- Their answer to whatever the site asks. One free-text field rather than
    -- a schema of questions: the questions are the operator's business and
    -- change per site, and a column per question is a migration per edit.
    body        TEXT NOT NULL DEFAULT '',

    -- 'pending' | 'accepted' | 'rejected'. Three states, no more: an
    -- application is a question with a yes, a no, and not-yet.
    status      TEXT NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'accepted', 'rejected')),

    -- The decision, kept in full.
    decided_at  TIMESTAMPTZ,
    decided_by  BIGINT,
    -- Staff-only. Never shown to the applicant, which is what makes it usable
    -- for the honest reason rather than the diplomatic one.
    note        TEXT NOT NULL DEFAULT '',
    -- The invite an acceptance produced, so the queue can show whether the
    -- door actually opened rather than only that somebody clicked accept.
    invite_code TEXT NOT NULL DEFAULT '',

    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Where it came from, for the operator working out whether a run of
    -- applications is one person.
    ip_hash     TEXT NOT NULL DEFAULT ''
);

-- The queue's own read: pending first, oldest first, because an application
-- queue is a queue and the person who has waited longest goes next.
CREATE INDEX IF NOT EXISTS applications_pending_idx
    ON applications (created_at) WHERE status = 'pending';

-- "Has this address already applied?" — asked on every submission.
CREATE INDEX IF NOT EXISTS applications_email_idx ON applications (email);
