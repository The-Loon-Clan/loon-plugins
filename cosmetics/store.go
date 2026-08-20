package cosmetics

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/the-loon-clan/loon/core"
)

// SlotName is the only slot today: the username, the one place a member's name
// is drawn with any styling at all.
const SlotName = "name"

// Owned is one unlock.
type Owned struct {
	UserID    int64      `db:"user_id"`
	Slug      string     `db:"slug"`
	Source    string     `db:"source"`
	GrantedAt time.Time  `db:"granted_at"`
	ExpiresAt *time.Time `db:"expires_at"`
}

// Sources an unlock can come from.
const (
	SourceStore = "store"
	SourceGrant = "grant"
)

type Store interface {
	// Unlock records one, extending rather than replacing an unlock the member
	// already has — see the implementation on why buying the same effect twice
	// must never shorten it.
	Unlock(ctx context.Context, userID int64, slug, source string, days int) error

	// OwnedBy lists a member's LIVE unlocks, newest first.
	OwnedBy(ctx context.Context, userID int64) ([]Owned, error)

	// Equip puts one on, or takes it off when slug is empty. Refuses anything
	// the member does not currently own, which is the whole tampering surface.
	Equip(ctx context.Context, userID int64, slot, slug string) (bool, error)

	// EquippedBy reports what the member is wearing in a slot.
	EquippedBy(ctx context.Context, userID int64, slot string) (string, error)

	// LiveEquipped returns user_id -> slug for everybody wearing something in
	// a slot, skipping any whose unlock has lapsed.
	LiveEquipped(ctx context.Context, slot string) (map[int64]string, error)
}

type PGStore struct{ db *core.SchemaDB }

func NewPGStore(db *core.SchemaDB) *PGStore { return &PGStore{db: db} }

var _ Store = (*PGStore)(nil)

func (s *PGStore) sel(ctx context.Context, dest any, q string, args ...any) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error { return tx.SelectContext(ctx, dest, q, args...) })
}

// Unlock records a purchase or a grant.
//
// EXTENDING, not replacing. Somebody who buys thirty days of an effect they
// already have twenty days of should end at fifty, and the naive upsert — set
// expires_at = now + days — silently takes thirty days off them for the crime
// of buying more. The GREATEST(...) is that bug not happening.
//
// And a permanent unlock stays permanent: NULL expires_at is absorbing, so a
// dated purchase on top of one somebody already owns for good cannot put an
// end date on it.
func (s *PGStore) Unlock(ctx context.Context, userID int64, slug, source string, days int) error {
	var expires any
	if days > 0 {
		expires = time.Now().Add(time.Duration(days) * 24 * time.Hour)
	}
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO cosmetic_owned (user_id, slug, source, expires_at)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (user_id, slug) DO UPDATE
			   SET expires_at = CASE
			           -- Already theirs for good, or now being made so.
			           WHEN cosmetic_owned.expires_at IS NULL THEN NULL
			           WHEN EXCLUDED.expires_at IS NULL THEN NULL
			           -- Add the new run to whatever is left, never truncate.
			           ELSE GREATEST(cosmetic_owned.expires_at, NOW()) +
			                (EXCLUDED.expires_at - NOW())
			       END,
			       source = EXCLUDED.source`,
			userID, slug, source, expires)
		return err
	})
}

const liveWhere = ` AND (expires_at IS NULL OR expires_at > NOW())`

func (s *PGStore) OwnedBy(ctx context.Context, userID int64) ([]Owned, error) {
	var rows []Owned
	if err := s.sel(ctx, &rows, `
		SELECT user_id, slug, source, granted_at, expires_at
		  FROM cosmetic_owned
		 WHERE user_id = $1`+liveWhere+`
		 ORDER BY granted_at DESC`, userID); err != nil {
		return nil, fmt.Errorf("owned cosmetics: %w", err)
	}
	return rows, nil
}

// Equip puts an effect on, or takes one off.
//
// The ownership check is IN THE STATEMENT — the insert selects from
// cosmetic_owned — rather than a read followed by a write. A member posting a
// slug they do not own writes nothing, and there is no window between the two
// halves in which an unlock could lapse.
func (s *PGStore) Equip(ctx context.Context, userID int64, slot, slug string) (bool, error) {
	if slug == "" {
		err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx,
				`DELETE FROM cosmetic_equipped WHERE user_id = $1 AND slot = $2`, userID, slot)
			return err
		})
		return err == nil, err
	}
	var n int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO cosmetic_equipped (user_id, slot, slug)
			SELECT $1, $2, o.slug
			  FROM cosmetic_owned o
			 WHERE o.user_id = $1 AND o.slug = $3
			   AND (o.expires_at IS NULL OR o.expires_at > NOW())
			ON CONFLICT (user_id, slot) DO UPDATE
			   SET slug = EXCLUDED.slug, equipped_at = NOW()`,
			userID, slot, slug)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return false, fmt.Errorf("equip cosmetic: %w", err)
	}
	return n > 0, nil
}

func (s *PGStore) EquippedBy(ctx context.Context, userID int64, slot string) (string, error) {
	var slugs []string
	if err := s.sel(ctx, &slugs, `
		SELECT slug FROM cosmetic_equipped WHERE user_id = $1 AND slot = $2`, userID, slot); err != nil {
		return "", fmt.Errorf("equipped cosmetic: %w", err)
	}
	if len(slugs) == 0 {
		return "", nil
	}
	return slugs[0], nil
}

// LiveEquipped is the renderer's query.
//
// The JOIN back to cosmetic_owned is what makes a lapsed VIP effect stop
// rendering without anything having to sweep the equipped table: the row stays
// (so the effect comes back if they renew), and it simply stops matching.
func (s *PGStore) LiveEquipped(ctx context.Context, slot string) (map[int64]string, error) {
	var rows []struct {
		UserID int64  `db:"user_id"`
		Slug   string `db:"slug"`
	}
	if err := s.sel(ctx, &rows, `
		SELECT e.user_id, e.slug
		  FROM cosmetic_equipped e
		  JOIN cosmetic_owned o
		    ON o.user_id = e.user_id AND o.slug = e.slug
		 WHERE e.slot = $1
		   AND (o.expires_at IS NULL OR o.expires_at > NOW())`, slot); err != nil {
		return nil, fmt.Errorf("live equipped: %w", err)
	}
	out := make(map[int64]string, len(rows))
	for _, r := range rows {
		out[r.UserID] = r.Slug
	}
	return out, nil
}
