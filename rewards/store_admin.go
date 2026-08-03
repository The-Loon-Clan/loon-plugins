package rewards

import (
	"context"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// The admin/config half of the store. Split from store.go because these run on
// an operator page at human pace, not on the login path — so they may join and
// aggregate where the hot-path reads may not.

// EventStats is an event plus what an operator actually needs to see: whether
// its windows are being materialised, and whether one is open right now.
type EventStats struct {
	Event
	Windows int
	Current *Window
	Next    *Window
}

// GrantRow is a grant joined to its reward slug for the admin table.
type GrantRow struct {
	Grant
	RewardSlug string `db:"reward_slug"`
	Payout     string `db:"payout"`
}

// AdminStore is the configuration surface. Kept separate from Store so the
// engine's dependency stays the narrow hot-path one.
type AdminStore interface {
	ListEventStats(ctx context.Context, at time.Time) ([]EventStats, error)
	ListRewards(ctx context.Context) ([]Reward, error)
	RecentGrants(ctx context.Context, limit int) ([]GrantRow, error)

	CreateEvent(ctx context.Context, ev Event) (int64, error)
	CreateReward(ctx context.Context, r Reward) (int64, error)
	SetRewardEnabled(ctx context.Context, rewardID int64, enabled bool) error
	SetEventEnabled(ctx context.Context, eventID int64, enabled bool) error
}

var _ AdminStore = (*PGStore)(nil)

func (s *PGStore) ListEventStats(ctx context.Context, at time.Time) ([]EventStats, error) {
	var rows []eventRow
	err := s.db.SelectContext(ctx, &rows, `
		SELECT id, slug, name, description, cron,
		       EXTRACT(EPOCH FROM duration) AS duration_secs, timezone, enabled
		  FROM events ORDER BY slug`)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	out := make([]EventStats, 0, len(rows))
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, EventStats{Event: r.toEvent()})
		ids = append(ids, r.ID)
	}
	if len(ids) == 0 {
		return out, nil
	}

	// Counts, the open window, and the next one — three small queries rather
	// than three per event.
	counts := map[int64]int{}
	var cRows []struct {
		EventID int64 `db:"event_id"`
		N       int   `db:"n"`
	}
	if err := s.db.SelectContext(ctx, &cRows,
		`SELECT event_id, count(*) AS n FROM event_windows WHERE event_id = ANY($1) GROUP BY event_id`,
		pq.Array(ids)); err != nil {
		return nil, fmt.Errorf("window counts: %w", err)
	}
	for _, c := range cRows {
		counts[c.EventID] = c.N
	}
	current, err := s.OpenWindowsFor(ctx, ids, at)
	if err != nil {
		return nil, err
	}
	var nextRows []Window
	if err := s.db.SelectContext(ctx, &nextRows, `
		SELECT DISTINCT ON (event_id) id, event_id, starts_at, ends_at
		  FROM event_windows WHERE event_id = ANY($1) AND starts_at > $2
		 ORDER BY event_id, starts_at ASC`, pq.Array(ids), at); err != nil {
		return nil, fmt.Errorf("next windows: %w", err)
	}
	next := map[int64]Window{}
	for _, w := range nextRows {
		next[w.EventID] = w
	}
	for i := range out {
		id := out[i].ID
		out[i].Windows = counts[id]
		if w, ok := current[id]; ok {
			out[i].Current = &w
		}
		if w, ok := next[id]; ok {
			out[i].Next = &w
		}
	}
	return out, nil
}

func (s *PGStore) ListRewards(ctx context.Context) ([]Reward, error) {
	var rows []rewardRow
	// sqllint:allow rewardCols is a compile-time const column list, no user input
	err := s.db.SelectContext(ctx, &rows, `SELECT `+rewardCols+` FROM rewards ORDER BY slug`)
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
	err := s.db.SelectContext(ctx, &rows, `
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

func (s *PGStore) CreateEvent(ctx context.Context, ev Event) (int64, error) {
	var id int64
	// The duration is passed as seconds and cast, because lib/pq has no
	// INTERVAL binding — the same reason the reads use EXTRACT(EPOCH).
	var secs *float64
	if ev.Duration != nil {
		v := ev.Duration.Seconds()
		secs = &v
	}
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO events (slug, name, description, cron, duration, timezone, enabled)
		VALUES ($1,$2,$3,NULLIF($4,''), ($5::float8 || ' seconds')::interval, $6, $7)
		RETURNING id`,
		ev.Slug, ev.Name, ev.Description, derefStr(ev.Cron), secs, ev.Timezone, ev.Enabled).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create event: %w", err)
	}
	return id, nil
}

func (s *PGStore) CreateReward(ctx context.Context, r Reward) (int64, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var secs *float64
	if r.ExpiresAfter != nil {
		v := r.ExpiresAfter.Seconds()
		secs = &v
	}
	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO rewards (slug, name, kind, event_id, trigger, expires_after, delivery, enabled)
		VALUES ($1,$2,$3,$4,$5, ($6::float8 || ' seconds')::interval, $7,$8) RETURNING id`,
		r.Slug, r.Name, string(r.Kind), r.EventID, r.Trigger, secs, string(r.Delivery), r.Enabled).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create reward: %w", err)
	}
	// Payout lines in the same transaction: a reward that exists with no
	// payouts pays nothing while looking perfectly healthy.
	for i, p := range r.Payouts {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO reward_payouts (reward_id, kind, target, amount, ordinal)
			VALUES ($1,$2,NULLIF($3,''),$4,$5)`,
			id, string(p.Kind), p.Target, p.Amount, i); err != nil {
			return 0, fmt.Errorf("create payout %s: %w", p.Kind, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit reward: %w", err)
	}
	return id, nil
}

func (s *PGStore) SetRewardEnabled(ctx context.Context, rewardID int64, enabled bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE rewards SET enabled = $2 WHERE id = $1`, rewardID, enabled)
	return err
}

func (s *PGStore) SetEventEnabled(ctx context.Context, eventID int64, enabled bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE events SET enabled = $2 WHERE id = $1`, eventID, enabled)
	return err
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
