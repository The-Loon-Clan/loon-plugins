package cosmetics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/jmoiron/sqlx"
)

// Custom titles — the one part of this plugin that publishes words somebody
// typed, and therefore the only part with a staff queue.
//
// THE RIGHT IS BOUGHT, THE WORDS ARE REVIEWED. Buying unlocks the ability to
// have a title; it does not publish one. What gets published is whatever staff
// pass, which means the shop can sell this without selling anybody a billboard.

// titleUnlock is the pseudo-slug an unlock is stored under in cosmetic_owned.
//
// The same table as the effects, deliberately: "may this member have a title"
// and "does this member own the gold aura" are the same question about the same
// person, they expire the same way, and a second table would need its own copy
// of the extend-never-truncate rule that Unlock already gets right.
//
// Prefixed so it can never collide with a catalogue slug — EffectBySlug refuses
// it, so it can never be equipped as an effect either.
const titleUnlock = "grant:custom-title"

// titleMax bounds a title. Short enough to sit under a username without
// becoming the page, long enough for a real joke.
const titleMax = 48

// Title states.
const (
	TitlePending  = "pending"
	TitleApproved = "approved"
	TitleRejected = "rejected"
)

// MemberTitle is one member's title, in whatever state it is in.
type MemberTitle struct {
	UserID int64 `db:"user_id"`
	// Text is what they have PROPOSED — the words in the queue.
	Text string `db:"text"`
	// Published is what is SHOWING under their name right now.
	//
	// Separate from Text because a member editing an approved title is
	// proposing new words, not withdrawing the old ones: with one column, the
	// title vanished from every page the instant they pressed send and stayed
	// gone until a moderator got round to them. It also gives the queue
	// something to compare against, which is the difference between "they
	// asked for this" and "they changed something already approved into this".
	Published   string     `db:"published"`
	State       string     `db:"state"`
	SubmittedAt time.Time  `db:"submitted_at"`
	ReviewedAt  *time.Time `db:"reviewed_at"`
	ReviewedBy  *int64     `db:"reviewed_by"`
	Reason      string     `db:"reason"`
}

// TitleStore is the titles half of the store.
type TitleStore interface {
	// SubmitTitle records a member's words, waiting for staff.
	SubmitTitle(ctx context.Context, userID int64, text string) error

	// TitleOf returns one member's title in any state, for their own page.
	TitleOf(ctx context.Context, userID int64) (MemberTitle, bool, error)

	// PendingTitles is the staff queue, oldest first.
	PendingTitles(ctx context.Context, limit int) ([]MemberTitle, error)

	// ReviewTitle passes or refuses one. reason is shown to the member and is
	// required for a refusal — see the handler.
	ReviewTitle(ctx context.Context, userID, byUser int64, approve bool, reason string) (bool, error)

	// ApprovedTitles is the renderer's query: user_id -> the words currently
	// SHOWING, which is not the same as the words in the row — see Published.
	ApprovedTitles(ctx context.Context) (map[int64]string, error)
}

var _ TitleStore = (*PGStore)(nil)

// SubmitTitle stores a submission, replacing whatever was there.
//
// Resubmitting always returns to pending, including over an APPROVED title, and
// that is the point rather than an oversight: a member who edits their approved
// title is proposing new words, and publishing them unreviewed would make the
// queue trivially bypassable — submit something harmless, get it passed, edit
// it into anything.
func (s *PGStore) SubmitTitle(ctx context.Context, userID int64, text string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO cosmetic_titles (user_id, text, state, submitted_at,
			                             reviewed_at, reviewed_by, reason)
			VALUES ($1, $2, 'pending', NOW(), NULL, NULL, '')
			ON CONFLICT (user_id) DO UPDATE
			   SET text = EXCLUDED.text,
			       state = 'pending',
			       submitted_at = NOW(),
			       reviewed_at = NULL,
			       reviewed_by = NULL,
			       reason = ''
			   -- published is deliberately untouched: what is showing stays
			   -- showing until the new words pass.
			   `, userID, text)
		return err
	})
}

const titleCols = `user_id, text, published, state, submitted_at, reviewed_at, reviewed_by, reason`

func (s *PGStore) TitleOf(ctx context.Context, userID int64) (MemberTitle, bool, error) {
	var t MemberTitle
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &t,
			`SELECT `+titleCols+` FROM cosmetic_titles WHERE user_id = $1`, userID)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return MemberTitle{}, false, nil
	}
	if err != nil {
		return MemberTitle{}, false, fmt.Errorf("title of: %w", err)
	}
	return t, true, nil
}

func (s *PGStore) PendingTitles(ctx context.Context, limit int) ([]MemberTitle, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var rows []MemberTitle
	if err := s.sel(ctx, &rows, `
		SELECT `+titleCols+` FROM cosmetic_titles
		 WHERE state = 'pending'
		 ORDER BY submitted_at ASC
		 LIMIT $1`, limit); err != nil {
		return nil, fmt.Errorf("pending titles: %w", err)
	}
	return rows, nil
}

// ReviewTitle passes or refuses.
//
// The state is checked in the statement, so two moderators opening the queue
// together cannot both decide the same title — the second writes nothing and is
// told the first got there.
func (s *PGStore) ReviewTitle(ctx context.Context, userID, byUser int64, approve bool, reason string) (bool, error) {
	state := TitleRejected
	if approve {
		state = TitleApproved
	}
	var n int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE cosmetic_titles
			   SET state = $2, reviewed_at = NOW(), reviewed_by = $3, reason = $4,
			       -- Approving is what PUBLISHES. Turning down leaves whatever
			       -- was already showing exactly where it was, which is the
			       -- whole point of the two columns: a refused edit costs a
			       -- member nothing they already had.
			       published = CASE WHEN $2 = 'approved' THEN text ELSE published END
			 WHERE user_id = $1 AND state = 'pending'`,
			userID, state, byUser, reason)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return false, fmt.Errorf("review title: %w", err)
	}
	return n > 0, nil
}

func (s *PGStore) ApprovedTitles(ctx context.Context) (map[int64]string, error) {
	var rows []struct {
		UserID int64  `db:"user_id"`
		Text   string `db:"text"`
	}
	if err := s.sel(ctx, &rows, `
		SELECT user_id, published AS text FROM cosmetic_titles WHERE published <> ''`); err != nil {
		return nil, fmt.Errorf("approved titles: %w", err)
	}
	out := make(map[int64]string, len(rows))
	for _, r := range rows {
		out[r.UserID] = r.Text
	}
	return out, nil
}

// cleanTitle normalises what somebody typed and says whether it is usable.
//
// This is NOT moderation — staff do that, and no amount of character filtering
// substitutes for a person reading it. What it does is remove the tricks that
// are about the RENDERING rather than the words: control characters, the
// bidirectional overrides that let text escape its own element and reorder the
// line around it, combining marks stacked deep enough to paint over the rows
// above, and runs of whitespace that make a short title occupy a tall one.
func cleanTitle(in string) (string, bool) {
	var b strings.Builder
	combining := 0
	lastSpace := false
	for _, r := range strings.TrimSpace(in) {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			// Folded to a single space rather than dropped: "a\nb" is two
			// words, and joining them into "ab" changes what was written.
			r = ' '
		case unicode.IsControl(r):
			continue
		case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200F, r == 0x200E:
			// Bidi overrides and isolates. A title carrying one of these does
			// not stay inside its own element visually — it reorders whatever
			// is drawn beside it, which is somebody else's username.
			continue
		}
		if unicode.Is(unicode.Mn, r) {
			// Combining marks, capped. Three is more than any real script
			// stacks on one base character and far fewer than it takes to
			// paint over the line above.
			combining++
			if combining > 3 {
				continue
			}
		} else {
			combining = 0
		}
		if r == ' ' {
			if lastSpace || b.Len() == 0 {
				continue
			}
			lastSpace = true
		} else {
			lastSpace = false
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "" || len([]rune(out)) > titleMax {
		return "", false
	}
	return out, true
}
