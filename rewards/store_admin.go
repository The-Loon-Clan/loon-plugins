package rewards

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// The admin/config half of the store. Split from store.go because these run on
// an operator page at human pace, not on the login path — so they may join and
// aggregate where the hot-path reads may not.

// EventStats, the events admin page, and the window CRUD moved to the events
// plugin, which now owns both tables and shows exactly this on its own page. The
// comment that used to sit on the old page said why: "Events are not
// reward-specific ... it also keeps each page to one job: Events is WHEN,
// Rewards is WHAT."

// GrantRow is a grant joined to its reward slug for the admin table.
type GrantRow struct {
	Grant
	RewardSlug string `db:"reward_slug"`
	Payout     string `db:"payout"`
}

// AdminStore is the configuration surface. Kept separate from Store so the
// engine's dependency stays the narrow hot-path one.
type AdminStore interface {
	ListRewards(ctx context.Context) ([]Reward, error)
	// The source catalogue: configuration, not code. See sources.go.
	// (Achievement definition CRUD lived here too until the achievements
	// plugin moved out with its tables.)
	ListSources(ctx context.Context) (SourceCatalog, error)
	CountSources(ctx context.Context) (int, error)
	SeedSources(ctx context.Context, cat SourceCatalog) error
	UpsertSource(ctx context.Context, d SourceDef) error
	SetSourceEnabled(ctx context.Context, key string, on bool) error
	RecentGrants(ctx context.Context, limit int) ([]GrantRow, error)

	// CountStalePending counts pending grants past their expiry — a number
	// that should always be zero if the expiry sweep is running.
	CountStalePending(ctx context.Context, now time.Time) (int, error)
	CreateReward(ctx context.Context, r Reward) (int64, error)
	SetRewardEnabled(ctx context.Context, rewardID int64, enabled bool) error

	// ── lootboxes (lootbox.go) ─────────────────────────────────────────────
	//
	// A box has no row of its own, so there is no create or delete for one:
	// adding the first entry makes a box exist and removing the last unmakes
	// it, which is the only definition that cannot go out of step with its
	// contents.

	// LootboxSlugs lists every box that has entries, for a picker.
	LootboxSlugs(ctx context.Context) ([]string, error)
	// LootboxEntries reads one box in display order, reward names joined on.
	LootboxEntries(ctx context.Context, boxSlug string) ([]LootboxEntry, error)
	// AddLootboxEntry adds or re-weights one prize. Upsert on (box, reward),
	// because the schema's UNIQUE says a repeat is a weight rather than a
	// second line, and an admin who adds the same prize twice means the
	// second number.
	AddLootboxEntry(ctx context.Context, e LootboxEntry) error
	// RemoveLootboxEntry drops one line by id.
	RemoveLootboxEntry(ctx context.Context, id int64) error
}

var _ AdminStore = (*PGStore)(nil)

func (s *PGStore) ListRewards(ctx context.Context) ([]Reward, error) {
	var rows []rewardRow
	// sqllint:allow rewardCols is a compile-time const column list, no user input
	err := s.sel(ctx, &rows, `SELECT `+rewardCols+` FROM rewards ORDER BY slug`)
	if err != nil {
		return nil, fmt.Errorf("list rewards: %w", err)
	}
	out := make([]Reward, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toReward())
	}
	return out, s.attachPayouts(ctx, out)
}

func (s *PGStore) RecentGrants(ctx context.Context, limit int) ([]GrantRow, error) {
	var rows []GrantRow
	err := s.sel(ctx, &rows, `
		SELECT g.id, g.reward_id, g.user_id, g.reference, g.state, g.reason,
		       g.created_at, g.expires_at, g.settled_at, r.slug AS reward_slug,
		       COALESCE((SELECT string_agg(
		           CASE WHEN p.kind = 'points' THEN p.amount || ' points'
		                ELSE p.kind || ' ' || COALESCE(p.target,'') END, ', ' ORDER BY p.id)
		         FROM reward_grant_payouts p WHERE p.grant_id = g.id), '') AS payout
		  FROM reward_grants g JOIN rewards r ON r.id = g.reward_id
		 ORDER BY g.id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("recent grants: %w", err)
	}
	return rows, nil
}

func (s *PGStore) CreateReward(ctx context.Context, r Reward) (int64, error) {
	var secs *float64
	if r.ExpiresAfter != nil {
		v := r.ExpiresAfter.Seconds()
		secs = &v
	}
	var id int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		err := tx.QueryRowContext(ctx, `
			INSERT INTO rewards (slug, name, kind, scheduled_event_slug, trigger, expires_after, delivery, enabled)
			VALUES ($1,$2,$3,$4,$5, ($6::float8 || ' seconds')::interval, $7,$8) RETURNING id`,
			r.Slug, r.Name, string(r.Kind), nullSlug(r.EventSlug), r.Trigger, secs, string(r.Delivery), r.Enabled).Scan(&id)
		if err != nil {
			return fmt.Errorf("create reward: %w", err)
		}
		// Payout lines in the same transaction: a reward that exists with no
		// payouts pays nothing while looking perfectly healthy.
		for i, p := range r.Payouts {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO reward_payouts (reward_id, kind, target, amount, ordinal)
				VALUES ($1,$2,NULLIF($3,''),$4,$5)`,
				id, string(p.Kind), p.Target, p.Amount, i); err != nil {
				return fmt.Errorf("create payout %s: %w", p.Kind, err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *PGStore) SetRewardEnabled(ctx context.Context, rewardID int64, enabled bool) error {
	_, err := s.exec(ctx, `UPDATE rewards SET enabled = $2 WHERE id = $1`, rewardID, enabled)
	return err
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (s *PGStore) CountStalePending(ctx context.Context, now time.Time) (int, error) {
	var n int
	err := s.get(ctx, &n, `
		SELECT count(*) FROM reward_grants
		 WHERE state = 'pending' AND expires_at IS NOT NULL AND expires_at <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("count stale pending: %w", err)
	}
	return n, nil
}

// nullSlug maps "no scheduled event" to NULL rather than the empty string.
//
// Both would read back as "no event" through toReward, so this is about the
// column rather than the model: a mix of NULL and ” makes every hand-written
// query about event-gated rewards need to test for two things, and the one that
// forgets is wrong in a way no test covers.
func nullSlug(slug string) any {
	if slug == "" {
		return nil
	}
	return slug
}

// ── lootboxes ───────────────────────────────────────────────────────────────

func (s *PGStore) LootboxSlugs(ctx context.Context) ([]string, error) {
	var out []string
	err := s.sel(ctx, &out,
		`SELECT DISTINCT box_slug FROM lootbox_entries ORDER BY box_slug`)
	return out, err
}

// LootboxEntries joins the reward's slug and name on, because every surface
// that lists a box lists what is IN it, and an id is not something an operator
// or a member can read.
func (s *PGStore) LootboxEntries(ctx context.Context, boxSlug string) ([]LootboxEntry, error) {
	var out []LootboxEntry
	err := s.sel(ctx, &out, `
		SELECT e.id, e.box_slug, e.reward_id, e.weight, e.ordinal,
		       r.slug AS reward_slug, r.name AS reward_name
		  FROM lootbox_entries e
		  JOIN rewards r ON r.id = e.reward_id
		 WHERE e.box_slug = $1
		 ORDER BY e.ordinal, e.id`, boxSlug)
	return out, err
}

func (s *PGStore) AddLootboxEntry(ctx context.Context, e LootboxEntry) error {
	_, err := s.exec(ctx, `
		INSERT INTO lootbox_entries (box_slug, reward_id, weight, ordinal)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (box_slug, reward_id)
		DO UPDATE SET weight = EXCLUDED.weight, ordinal = EXCLUDED.ordinal`,
		e.BoxSlug, e.RewardID, e.Weight, e.Ordinal)
	return err
}

func (s *PGStore) RemoveLootboxEntry(ctx context.Context, id int64) error {
	_, err := s.exec(ctx, `DELETE FROM lootbox_entries WHERE id = $1`, id)
	return err
}
