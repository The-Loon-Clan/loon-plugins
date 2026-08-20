package polls

import (
	"context"
	"strings"
	"time"
)

// Poll is one question.
type Poll struct {
	ID        int64      `db:"id"`
	Slug      string     `db:"slug"`
	Question  string     `db:"question"`
	Results   string     `db:"results"`
	CreatedBy *int64     `db:"created_by"`
	CreatedAt time.Time  `db:"created_at"`
	ClosesAt  *time.Time `db:"closes_at"`
	ClosedAt  *time.Time `db:"closed_at"`
}

// Results policies. When the tally becomes readable, and by whom.
const (
	// ResultsAfterVote is the default and the honest one: the numbers arrive
	// once you have committed to an answer, so the running tally cannot move
	// how you answer.
	ResultsAfterVote = "after_vote"
	// ResultsAlways is a temperature check where the tally IS the content —
	// nobody minds being told what the room thinks before joining it.
	ResultsAlways = "always"
	// ResultsOnClose is for a vote where early numbers would campaign: a
	// two-to-nothing lead in the first hour reads as a settled question.
	ResultsOnClose = "on_close"
)

// Option is one thing you can pick, with its tally attached.
//
// Votes travels WITH the option rather than in a parallel map because every
// caller that has one wants the other, and the pairing is the thing a template
// draws a bar from.
type Option struct {
	ID      int64  `db:"id"`
	PollID  int64  `db:"poll_id"`
	Ordinal int    `db:"ordinal"`
	Label   string `db:"label"`
	Votes   int    `db:"votes"`
}

// Closed reports whether the poll still takes votes.
//
// Two ways in — a deadline that passed, or somebody ending it — and they are
// deliberately one question here: everything downstream (the ballot, the
// results policy, the admin list) cares only whether it is over.
func (p Poll) Closed(now time.Time) bool {
	if p.ClosedAt != nil {
		return true
	}
	return p.ClosesAt != nil && !p.ClosesAt.After(now)
}

// Store is everything polls need from the database.
type Store interface {
	// BySlug loads one poll and its options, tallied.
	BySlug(ctx context.Context, slug string) (Poll, []Option, bool, error)

	// List returns every poll, newest first, with its total vote count — the
	// admin index. Options are NOT loaded: the list shows how many people
	// answered, not what they answered.
	List(ctx context.Context) ([]Poll, []int, error)

	// Create writes a poll and its options in one transaction. A poll with no
	// options is not a poll, and half of one in the table is worse than none.
	Create(ctx context.Context, p Poll, labels []string) error

	// Vote records or CHANGES one member's answer — see the primary key on
	// poll_votes. Returns false when the option does not belong to the poll,
	// which is the whole of the tampering surface here.
	Vote(ctx context.Context, pollID, userID, optionID int64) (bool, error)

	// VoteOf reports what this member picked, if anything.
	VoteOf(ctx context.Context, pollID, userID int64) (int64, bool, error)

	// SetClosed ends a poll or reopens it.
	SetClosed(ctx context.Context, id int64, closed bool) error

	// Delete removes a poll and, by cascade, its options and its votes.
	Delete(ctx context.Context, id int64) error
}

// slugify turns a question into something a shortcode can name it by, so an
// operator who does not care about slugs never has to type one.
func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			// Runs of punctuation and spaces collapse to ONE dash, and a
			// leading one never gets written: "Should we...?" becomes
			// "should-we", not "should-we---".
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 60 {
		out = strings.Trim(out[:60], "-")
	}
	return out
}
