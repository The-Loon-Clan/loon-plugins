package polls

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"
)

type PGStore struct{ db *core.SchemaDB }

func NewPGStore(db *core.SchemaDB) *PGStore { return &PGStore{db: db} }

var _ Store = (*PGStore)(nil)

func (s *PGStore) sel(ctx context.Context, dest any, q string, args ...any) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error { return tx.SelectContext(ctx, dest, q, args...) })
}

func (s *PGStore) get(ctx context.Context, dest any, q string, args ...any) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error { return tx.GetContext(ctx, dest, q, args...) })
}

const pollCols = `id, slug, question, results, created_by, created_at, closes_at, closed_at`

func (s *PGStore) BySlug(ctx context.Context, slug string) (Poll, []Option, bool, error) {
	var p Poll
	err := s.get(ctx, &p, `SELECT `+pollCols+` FROM polls WHERE slug = $1`, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return Poll{}, nil, false, nil
	}
	if err != nil {
		return Poll{}, nil, false, fmt.Errorf("poll %q: %w", slug, err)
	}
	opts, err := s.options(ctx, p.ID)
	if err != nil {
		return Poll{}, nil, false, err
	}
	return p, opts, true, nil
}

// options loads a ballot with its tally.
//
// A LEFT JOIN so an option nobody picked comes back with zero rather than
// vanishing — the options with no votes are frequently the interesting ones,
// and a ballot that silently drops them misreports the question.
func (s *PGStore) options(ctx context.Context, pollID int64) ([]Option, error) {
	var rows []Option
	if err := s.sel(ctx, &rows, `
		SELECT o.id, o.poll_id, o.ordinal, o.label,
		       count(v.user_id)::int AS votes
		  FROM poll_options o
		  LEFT JOIN poll_votes v ON v.option_id = o.id
		 WHERE o.poll_id = $1
		 GROUP BY o.id
		 ORDER BY o.ordinal, o.id`, pollID); err != nil {
		return nil, fmt.Errorf("poll options: %w", err)
	}
	return rows, nil
}

func (s *PGStore) List(ctx context.Context) ([]Poll, []int, error) {
	var rows []struct {
		Poll
		Votes int `db:"votes"`
	}
	if err := s.sel(ctx, &rows, `
		SELECT p.id, p.slug, p.question, p.results, p.created_by, p.created_at,
		       p.closes_at, p.closed_at,
		       (SELECT count(*) FROM poll_votes v WHERE v.poll_id = p.id)::int AS votes
		  FROM polls p
		 ORDER BY p.created_at DESC`); err != nil {
		return nil, nil, fmt.Errorf("list polls: %w", err)
	}
	polls := make([]Poll, 0, len(rows))
	votes := make([]int, 0, len(rows))
	for _, r := range rows {
		polls = append(polls, r.Poll)
		votes = append(votes, r.Votes)
	}
	return polls, votes, nil
}

// Create writes the question and the ballot together.
//
// ONE transaction, because a poll whose options failed to insert is a page
// asking a question nobody can answer — and it would look like a working poll
// in the admin list.
func (s *PGStore) Create(ctx context.Context, p Poll, labels []string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var id int64
		if err := tx.GetContext(ctx, &id, `
			INSERT INTO polls (slug, question, results, created_by, closes_at)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			p.Slug, strings.TrimSpace(p.Question), p.Results, p.CreatedBy, p.ClosesAt); err != nil {
			return err
		}
		for i, label := range labels {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO poll_options (poll_id, ordinal, label)
				VALUES ($1, $2, $3)`, id, i, strings.TrimSpace(label)); err != nil {
				return err
			}
		}
		return nil
	})
}

// Vote records an answer, or replaces the one already there.
//
// The option is checked against the POLL inside the statement rather than in
// Go, so a forged option id from another poll writes nothing instead of
// landing a vote in a ballot it does not belong to. That check is the reason
// this is one statement and not a read followed by a write.
func (s *PGStore) Vote(ctx context.Context, pollID, userID, optionID int64) (bool, error) {
	var n int
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO poll_votes (poll_id, user_id, option_id)
			SELECT $1, $2, o.id
			  FROM poll_options o
			 WHERE o.id = $3 AND o.poll_id = $1
			ON CONFLICT (poll_id, user_id) DO UPDATE
			   SET option_id = EXCLUDED.option_id, voted_at = now()`,
			pollID, userID, optionID)
		if err != nil {
			return err
		}
		rows, err := res.RowsAffected()
		n = int(rows)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("vote: %w", err)
	}
	return n > 0, nil
}

func (s *PGStore) VoteOf(ctx context.Context, pollID, userID int64) (int64, bool, error) {
	var id int64
	err := s.get(ctx, &id, `
		SELECT option_id FROM poll_votes WHERE poll_id = $1 AND user_id = $2`, pollID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("vote of: %w", err)
	}
	return id, true, nil
}

// SetClosed ends a poll or reopens it.
//
// Reopening clears closed_at but leaves closes_at alone: a deadline that
// already passed would close it again on the next render, which is the correct
// answer — an operator reopening a poll that expired should also move the
// deadline, and this makes that visible rather than quietly discarding what
// they set.
func (s *PGStore) SetClosed(ctx context.Context, id int64, closed bool) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var err error
		if closed {
			_, err = tx.ExecContext(ctx, `UPDATE polls SET closed_at = now() WHERE id = $1`, id)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE polls SET closed_at = NULL WHERE id = $1`, id)
		}
		return err
	})
}

func (s *PGStore) Delete(ctx context.Context, id int64) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM polls WHERE id = $1`, id)
		return err
	})
}
