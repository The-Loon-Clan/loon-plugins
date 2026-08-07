package rewards

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
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
	ListAchievementDefs(ctx context.Context) ([]AchievementDef, error)
	// AchievementDefsByMetric backs the event subscriber: one indexed read
	// per event, rather than filtering the whole catalogue per post.
	AchievementDefsByMetric(ctx context.Context, metric string) ([]AchievementDef, error)
	// The source catalogue: configuration, not code. See sources.go.
	ListSources(ctx context.Context) (SourceCatalog, error)
	CountSources(ctx context.Context) (int, error)
	SeedSources(ctx context.Context, cat SourceCatalog) error
	// Definition CRUD -- see admin_achievements.go for why these validate
	// rather than just insert.
	CreateAchievement(ctx context.Context, a NewAchievement) (int64, error)
	SetAchievementEnabled(ctx context.Context, id int64, on bool) error
	UpsertSource(ctx context.Context, d SourceDef) error
	SetSourceEnabled(ctx context.Context, key string, on bool) error
	RecentGrants(ctx context.Context, limit int) ([]GrantRow, error)

	CreateEvent(ctx context.Context, ev Event) (int64, error)
	// AddWindow authors one window by hand. The only way a one-off event
	// (no cron, so nothing generates for it) ever becomes usable.
	AddWindow(ctx context.Context, w Window) error
	// DeleteWindow removes an unused window. Refuses one that grants are
	// keyed on — see the implementation.
	DeleteWindow(ctx context.Context, windowID int64) error
	ListWindows(ctx context.Context, eventID int64, limit int) ([]Window, error)

	// EventCoverage reports every event's window health in ONE query: how many,
	// how far ahead, and how many gaps. Feeds the validator.
	EventCoverage(ctx context.Context) (map[int64]Coverage, error)
	// CountStalePending counts pending grants past their expiry — a number
	// that should always be zero if the expiry sweep is running.
	CountStalePending(ctx context.Context, now time.Time) (int, error)
	CreateReward(ctx context.Context, r Reward) (int64, error)
	SetRewardEnabled(ctx context.Context, rewardID int64, enabled bool) error
	SetEventEnabled(ctx context.Context, eventID int64, enabled bool) error
}

var _ AdminStore = (*PGStore)(nil)

func (s *PGStore) ListEventStats(ctx context.Context, at time.Time) ([]EventStats, error) {
	var rows []eventRow
	err := s.sel(ctx, &rows, `
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
	if err := s.sel(ctx, &cRows,
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
	if err := s.sel(ctx, &nextRows, `
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

func (s *PGStore) CreateEvent(ctx context.Context, ev Event) (int64, error) {
	var id int64
	// The duration is passed as seconds and cast, because lib/pq has no
	// INTERVAL binding — the same reason the reads use EXTRACT(EPOCH).
	var secs *float64
	if ev.Duration != nil {
		v := ev.Duration.Seconds()
		secs = &v
	}
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.QueryRowContext(ctx, `
			INSERT INTO events (slug, name, description, cron, duration, timezone, enabled)
			VALUES ($1,$2,$3,NULLIF($4,''), ($5::float8 || ' seconds')::interval, $6, $7)
			RETURNING id`,
			ev.Slug, ev.Name, ev.Description, derefStr(ev.Cron), secs, ev.Timezone, ev.Enabled).Scan(&id)
	})
	if err != nil {
		return 0, fmt.Errorf("create event: %w", err)
	}
	return id, nil
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
			INSERT INTO rewards (slug, name, kind, event_id, trigger, expires_after, delivery, enabled)
			VALUES ($1,$2,$3,$4,$5, ($6::float8 || ' seconds')::interval, $7,$8) RETURNING id`,
			r.Slug, r.Name, string(r.Kind), r.EventID, r.Trigger, secs, string(r.Delivery), r.Enabled).Scan(&id)
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

func (s *PGStore) SetEventEnabled(ctx context.Context, eventID int64, enabled bool) error {
	_, err := s.exec(ctx, `UPDATE events SET enabled = $2 WHERE id = $1`, eventID, enabled)
	return err
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (s *PGStore) AddWindow(ctx context.Context, w Window) error {
	_, err := s.exec(ctx, `
		INSERT INTO event_windows (event_id, starts_at, ends_at) VALUES ($1,$2,$3)
		ON CONFLICT (event_id, starts_at) DO NOTHING`, w.EventID, w.StartsAt, w.EndsAt)
	if err != nil {
		return fmt.Errorf("add window: %w", err)
	}
	return nil
}

// DeleteWindow refuses to remove a window any grant is keyed on.
//
// reward_grants.reference holds the window id for a recurring reward, and it
// is not a foreign key -- it cannot be, because the same column means a
// high-water mark for a per_unit reward. So nothing at the schema level stops
// this, and deleting the window a grant was issued against would leave that
// grant pointing at nothing: the member could then be paid again for a period
// they were already paid for.
func (s *PGStore) DeleteWindow(ctx context.Context, windowID int64) error {
	var used int
	err := s.get(ctx, &used, `
		SELECT count(*) FROM reward_grants g
		  JOIN rewards r ON r.id = g.reward_id
		 WHERE r.kind = 'recurring' AND g.reference = $1`, windowID)
	if err != nil {
		return fmt.Errorf("check window use: %w", err)
	}
	if used > 0 {
		return fmt.Errorf("window %d has %d grant(s) keyed on it; deleting it would let those members be paid again", windowID, used)
	}
	_, err = s.exec(ctx, `DELETE FROM event_windows WHERE id = $1`, windowID)
	return err
}

func (s *PGStore) ListWindows(ctx context.Context, eventID int64, limit int) ([]Window, error) {
	var out []Window
	err := s.sel(ctx, &out, `
		SELECT id, event_id, starts_at, ends_at FROM event_windows
		 WHERE event_id = $1 ORDER BY starts_at DESC LIMIT $2`, eventID, limit)
	if err != nil {
		return nil, fmt.Errorf("list windows: %w", err)
	}
	return out, nil
}

// EventCoverage computes counts, lookahead and gaps for every event at once.
//
// lead() over each event's windows is what makes the gap check cheap: a gap is
// simply a row whose successor does not start where it ended. Doing this per
// event in Go would mean pulling every window row into memory, and the whole
// point of the check is to run it casually on a page render.
func (s *PGStore) EventCoverage(ctx context.Context) (map[int64]Coverage, error) {
	var rows []Coverage
	err := s.sel(ctx, &rows, `
		WITH ordered AS (
		    SELECT event_id, starts_at, ends_at,
		           lead(starts_at) OVER (PARTITION BY event_id ORDER BY starts_at) AS next_start
		      FROM event_windows
		)
		SELECT event_id,
		       count(*)                                    AS windows,
		       max(ends_at)                                AS last_end,
		       count(*) FILTER (WHERE next_start IS NOT NULL
		                          AND next_start <> ends_at) AS gaps
		  FROM ordered GROUP BY event_id`)
	if err != nil {
		return nil, fmt.Errorf("event coverage: %w", err)
	}
	out := make(map[int64]Coverage, len(rows))
	for _, c := range rows {
		out[c.EventID] = c
	}
	return out, nil
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
