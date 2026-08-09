package perks

import (
	"context"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"
)

// ErrNoToken is returned when a member tries to spend a token they do not hold.
var ErrNoToken = errors.New("perks: no unspent token of that kind")

// ErrAlreadyApplied is returned when the torrent already has that perk. A
// distinct error rather than a silent success, because the caller is about to
// tell a member something and "you already had one" is a different sentence
// from "done".
var ErrAlreadyApplied = errors.New("perks: that perk is already on this torrent")

// Store is the plugin's persistence.
type Store interface {
	// Grant mints an unspent token. Called when one is bought.
	Grant(ctx context.Context, userID int64, kind Kind) error
	// Spend attaches an unspent token to a torrent and starts its clock.
	Spend(ctx context.Context, userID int64, kind Kind, infoHash string, duration time.Duration) error
	// Unspent counts what a member is holding, by kind.
	Unspent(ctx context.Context, userID int64) (map[Kind]int, error)
	// ActivePerks loads every perk in force, for the in-memory table.
	ActivePerks(ctx context.Context, now time.Time) ([]Active, error)
	// SpentBy lists a member's applied perks, newest first.
	SpentBy(ctx context.Context, userID int64) ([]Active, error)
}

type PGStore struct{ db *core.SchemaDB }

func NewPGStore(db *core.SchemaDB) *PGStore { return &PGStore{db: db} }

var _ Store = (*PGStore)(nil)

func (s *PGStore) Grant(ctx context.Context, userID int64, kind Kind) error {
	if !Known(kind) {
		// Refused rather than stored. A token of a kind nothing implements is
		// points taken for an effect that will never arrive, and the member
		// would have no way to discover that.
		return errors.New("perks: unknown perk kind " + string(kind))
	}
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO tokens (user_id, kind) VALUES ($1,$2)`, userID, string(kind))
		return err
	})
}

// Spend attaches the OLDEST unspent token of that kind.
//
// Oldest first because tokens are interchangeable and a member who bought one
// during a promotion should not have it stranded behind later purchases.
func (s *PGStore) Spend(ctx context.Context, userID int64, kind Kind, infoHash string, d time.Duration) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// Already applied? Say so specifically — spending a second token here
		// would take points for nothing.
		var exists bool
		if err := tx.GetContext(ctx, &exists,
			`SELECT EXISTS(SELECT 1 FROM tokens
			   WHERE user_id=$1 AND info_hash=$2 AND kind=$3)`,
			userID, infoHash, string(kind)); err != nil {
			return err
		}
		if exists {
			return ErrAlreadyApplied
		}

		var expires any
		if d > 0 {
			expires = time.Now().Add(d)
		}
		// FOR UPDATE SKIP LOCKED so two simultaneous spends cannot claim the
		// same token — a member with one token clicking twice must not spend it
		// on two torrents.
		res, err := tx.ExecContext(ctx,
			`UPDATE tokens SET info_hash=$3, spent_at=now(), expires_at=$4
			  WHERE id = (
			      SELECT id FROM tokens
			       WHERE user_id=$1 AND kind=$2 AND spent_at IS NULL
			       ORDER BY acquired_at
			       FOR UPDATE SKIP LOCKED
			       LIMIT 1)`,
			userID, string(kind), infoHash, expires)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNoToken
		}
		return nil
	})
}

func (s *PGStore) Unspent(ctx context.Context, userID int64) (map[Kind]int, error) {
	out := map[Kind]int{}
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT kind, count(*) FROM tokens
			  WHERE user_id=$1 AND spent_at IS NULL GROUP BY kind`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			var n int
			if err := rows.Scan(&k, &n); err != nil {
				return err
			}
			out[Kind(k)] = n
		}
		return rows.Err()
	})
	return out, err
}

// ActivePerks loads what the announce path needs.
//
// Expired rows are filtered HERE as well as in Table.Factors. Leaving them to
// the table would mean carrying every perk a busy site ever sold in memory
// forever, and the query has an index for it.
func (s *PGStore) ActivePerks(ctx context.Context, now time.Time) ([]Active, error) {
	var out []Active
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT user_id, info_hash, kind, COALESCE(expires_at, 'infinity'::timestamptz)
			   FROM tokens
			  WHERE spent_at IS NOT NULL
			    AND (expires_at IS NULL OR expires_at > $1)`, now)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a Active
			var kind string
			var exp time.Time
			if err := rows.Scan(&a.UserID, &a.InfoHash, &kind, &exp); err != nil {
				return err
			}
			a.Kind = Kind(kind)
			// 'infinity' comes back as a far-future time; keep the zero value
			// the table understands as "never expires".
			if exp.Year() < 9999 {
				a.ExpiresAt = exp
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

func (s *PGStore) SpentBy(ctx context.Context, userID int64) ([]Active, error) {
	var out []Active
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT info_hash, kind, COALESCE(expires_at, 'infinity'::timestamptz)
			   FROM tokens
			  WHERE user_id=$1 AND spent_at IS NOT NULL
			  ORDER BY spent_at DESC`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a := Active{UserID: userID}
			var kind string
			var exp time.Time
			if err := rows.Scan(&a.InfoHash, &kind, &exp); err != nil {
				return err
			}
			a.Kind = Kind(kind)
			if exp.Year() < 9999 {
				a.ExpiresAt = exp
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}
