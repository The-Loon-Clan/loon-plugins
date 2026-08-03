package rewards

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// PGStore is the production Store. All statements are unqualified: loon scopes
// search_path to the plugin's schema.
type PGStore struct{ db *sqlx.DB }

func NewPGStore(db *sqlx.DB) *PGStore { return &PGStore{db: db} }

var _ Store = (*PGStore)(nil)

// uniqueViolation is Postgres 23505. Checked structurally rather than by
// message text, which is localised and version-dependent.
func uniqueViolation(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23505"
}

// rewardCols selects INTERVAL columns as seconds. lib/pq cannot scan INTERVAL
// into time.Duration, and the alternative — a string parsed in Go — reproduces
// Postgres's interval grammar badly.
const rewardCols = `id, slug, name, kind, event_id, trigger, delivery, enabled,
	EXTRACT(EPOCH FROM expires_after) AS expires_secs`

type rewardRow struct {
	ID          int64    `db:"id"`
	Slug        string   `db:"slug"`
	Name        string   `db:"name"`
	Kind        string   `db:"kind"`
	EventID     *int64   `db:"event_id"`
	Trigger     string   `db:"trigger"`
	Delivery    string   `db:"delivery"`
	Enabled     bool     `db:"enabled"`
	ExpiresSecs *float64 `db:"expires_secs"`
}

func (r rewardRow) toReward() Reward {
	out := Reward{
		ID: r.ID, Slug: r.Slug, Name: r.Name, Kind: Kind(r.Kind),
		EventID: r.EventID, Trigger: r.Trigger,
		Delivery: Delivery(r.Delivery), Enabled: r.Enabled,
	}
	if r.ExpiresSecs != nil {
		d := time.Duration(*r.ExpiresSecs * float64(time.Second))
		out.ExpiresAfter = &d
	}
	return out
}

// attachPayouts loads every reward's payout lines in ONE query rather than one
// per reward. Two rewards is not worth optimising; the shape is, because this
// runs per login and the alternative grows with the config.
func (s *PGStore) attachPayouts(ctx context.Context, rewards []Reward) error {
	if len(rewards) == 0 {
		return nil
	}
	ids := make([]int64, len(rewards))
	byID := make(map[int64]*Reward, len(rewards))
	for i := range rewards {
		ids[i] = rewards[i].ID
		byID[rewards[i].ID] = &rewards[i]
	}
	var rows []Payout
	err := s.db.SelectContext(ctx, &rows, `
		SELECT id, reward_id, kind, COALESCE(target,'') AS target, amount, ordinal
		  FROM reward_payouts WHERE reward_id = ANY($1) ORDER BY reward_id, ordinal`,
		pq.Array(ids))
	if err != nil {
		return fmt.Errorf("load payouts: %w", err)
	}
	for _, p := range rows {
		if r := byID[p.RewardID]; r != nil {
			r.Payouts = append(r.Payouts, p)
		}
	}
	return nil
}

func (s *PGStore) RewardsByTrigger(ctx context.Context, trigger string) ([]Reward, error) {
	var rows []rewardRow
	err := s.db.SelectContext(ctx, &rows, `
		SELECT `+rewardCols+` FROM rewards
		 WHERE enabled AND trigger = $1 ORDER BY id`, trigger)
	if err != nil {
		return nil, fmt.Errorf("rewards by trigger %q: %w", trigger, err)
	}
	out := make([]Reward, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toReward())
	}
	return out, s.attachPayouts(ctx, out)
}

func (s *PGStore) rewardWhere(ctx context.Context, clause string, arg any) (*Reward, error) {
	var row rewardRow
	err := s.db.GetContext(ctx, &row, `SELECT `+rewardCols+` FROM rewards WHERE `+clause, arg) // sqllint:allow clause is a compile-time literal, the value flows through $1
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load reward: %w", err)
	}
	one := []Reward{row.toReward()}
	if err := s.attachPayouts(ctx, one); err != nil {
		return nil, err
	}
	return &one[0], nil
}

func (s *PGStore) RewardBySlug(ctx context.Context, slug string) (*Reward, error) {
	return s.rewardWhere(ctx, "slug = $1", slug)
}

func (s *PGStore) RewardByID(ctx context.Context, id int64) (*Reward, error) {
	return s.rewardWhere(ctx, "id = $1", id)
}

// OpenWindowsFor finds the window of each event containing `at`.
//
// DISTINCT ON with ORDER BY starts_at DESC takes the latest window that has
// opened and not yet closed. Half-open on purpose: at exactly ends_at the
// member belongs to the next window, so a contiguous reset hands over cleanly
// instead of granting one free extra claim on every boundary.
func (s *PGStore) OpenWindowsFor(ctx context.Context, eventIDs []int64, at time.Time) (map[int64]Window, error) {
	if len(eventIDs) == 0 {
		return map[int64]Window{}, nil
	}
	var rows []Window
	err := s.db.SelectContext(ctx, &rows, `
		SELECT DISTINCT ON (event_id) id, event_id, starts_at, ends_at
		  FROM event_windows
		 WHERE event_id = ANY($1) AND starts_at <= $2 AND ends_at > $2
		 ORDER BY event_id, starts_at DESC`,
		pq.Array(eventIDs), at)
	if err != nil {
		return nil, fmt.Errorf("open windows: %w", err)
	}
	out := make(map[int64]Window, len(rows))
	for _, w := range rows {
		out[w.EventID] = w
	}
	return out, nil
}

func (s *PGStore) GrantsForUser(ctx context.Context, userID int64, rewardIDs []int64) (map[int64]Grant, error) {
	if len(rewardIDs) == 0 {
		return map[int64]Grant{}, nil
	}
	var rows []Grant
	err := s.db.SelectContext(ctx, &rows, `
		SELECT id, reward_id, user_id, reference, state, reason, created_at, expires_at, settled_at
		  FROM reward_grants
		 WHERE user_id = $1 AND reward_id = ANY($2)
		 ORDER BY reward_id, id DESC`,
		userID, pq.Array(rewardIDs))
	if err != nil {
		return nil, fmt.Errorf("grants for user: %w", err)
	}
	// ORDER BY id DESC then first-wins keeps the newest grant per reward,
	// which is the one a surface asks about.
	out := make(map[int64]Grant, len(rows))
	for _, g := range rows {
		if _, seen := out[g.RewardID]; !seen {
			out[g.RewardID] = g
		}
	}
	return out, nil
}

func (s *PGStore) CreateGrant(ctx context.Context, g Grant, payouts []Payout) (Grant, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return Grant{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	err = tx.QueryRowContext(ctx, `
		INSERT INTO reward_grants (reward_id, user_id, reference, state, reason, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, created_at`,
		g.RewardID, g.UserID, g.Reference, string(g.State), g.Reason, g.ExpiresAt,
	).Scan(&g.ID, &g.CreatedAt)
	if uniqueViolation(err) {
		// The constraint arbitrated: someone else got there first. Not an
		// error condition, just the answer.
		return Grant{}, ErrAlreadyGranted
	}
	if err != nil {
		return Grant{}, fmt.Errorf("insert grant: %w", err)
	}

	g.Payouts = make([]Payout, 0, len(payouts))
	for _, p := range payouts {
		var id int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO reward_grant_payouts (grant_id, kind, target, amount)
			VALUES ($1, $2, NULLIF($3,''), $4) RETURNING id`,
			g.ID, string(p.Kind), p.Target, p.Amount).Scan(&id)
		if err != nil {
			return Grant{}, fmt.Errorf("freeze payout %s: %w", p.Kind, err)
		}
		p.ID = id
		g.Payouts = append(g.Payouts, p)
	}
	if err := tx.Commit(); err != nil {
		return Grant{}, fmt.Errorf("commit grant: %w", err)
	}
	return g, nil
}

func (s *PGStore) GrantByID(ctx context.Context, id int64) (*Grant, error) {
	var g Grant
	err := s.db.GetContext(ctx, &g, `
		SELECT id, reward_id, user_id, reference, state, reason, created_at, expires_at, settled_at
		  FROM reward_grants WHERE id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load grant: %w", err)
	}
	// Only unsettled lines: a resumed grant must not re-execute what already
	// landed, and the caller iterates whatever it is handed.
	err = s.db.SelectContext(ctx, &g.Payouts, `
		SELECT id, kind, COALESCE(target,'') AS target, amount
		  FROM reward_grant_payouts
		 WHERE grant_id = $1 AND settled_at IS NULL ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("load frozen payouts: %w", err)
	}
	return &g, nil
}

func (s *PGStore) MarkPayoutSettled(ctx context.Context, payoutID int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE reward_grant_payouts SET settled_at = $2 WHERE id = $1 AND settled_at IS NULL`,
		payoutID, at)
	return err
}

func (s *PGStore) SettleGrant(ctx context.Context, grantID int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE reward_grants SET state = 'credited', settled_at = $2
		 WHERE id = $1 AND state = 'pending'`, grantID, at)
	return err
}

func (s *PGStore) ExpireGrants(ctx context.Context, now time.Time, limit int) (int, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE reward_grants SET state = 'expired'
		 WHERE id IN (SELECT id FROM reward_grants
		               WHERE state = 'pending' AND expires_at IS NOT NULL AND expires_at <= $1
		               ORDER BY expires_at LIMIT $2)`, now, limit)
	if err != nil {
		return 0, fmt.Errorf("expire grants: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

type eventRow struct {
	ID          int64    `db:"id"`
	Slug        string   `db:"slug"`
	Name        string   `db:"name"`
	Description string   `db:"description"`
	Cron        *string  `db:"cron"`
	DurSecs     *float64 `db:"duration_secs"`
	Timezone    string   `db:"timezone"`
	Enabled     bool     `db:"enabled"`
}

func (r eventRow) toEvent() Event {
	out := Event{
		ID: r.ID, Slug: r.Slug, Name: r.Name, Description: r.Description,
		Cron: r.Cron, Timezone: r.Timezone, Enabled: r.Enabled,
	}
	if r.DurSecs != nil {
		d := time.Duration(*r.DurSecs * float64(time.Second))
		out.Duration = &d
	}
	return out
}

func (s *PGStore) EventsWithCron(ctx context.Context) ([]Event, error) {
	var rows []eventRow
	err := s.db.SelectContext(ctx, &rows, `
		SELECT id, slug, name, description, cron,
		       EXTRACT(EPOCH FROM duration) AS duration_secs, timezone, enabled
		  FROM events WHERE enabled AND cron IS NOT NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("events with cron: %w", err)
	}
	out := make([]Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toEvent())
	}
	return out, nil
}

func (s *PGStore) LastWindowEnd(ctx context.Context, eventID int64) (time.Time, error) {
	var t sql.NullTime
	err := s.db.GetContext(ctx, &t,
		`SELECT max(ends_at) FROM event_windows WHERE event_id = $1`, eventID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, fmt.Errorf("last window end: %w", err)
	}
	return t.Time, nil
}

// InsertWindows is idempotent through ON CONFLICT DO NOTHING against
// UNIQUE (event_id, starts_at), which is what lets the generator run on every
// tick with overlapping ranges and stay cheap.
func (s *PGStore) InsertWindows(ctx context.Context, ws []Window) (int, error) {
	if len(ws) == 0 {
		return 0, nil
	}
	var inserted int
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, w := range ws {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO event_windows (event_id, starts_at, ends_at) VALUES ($1, $2, $3)
			ON CONFLICT (event_id, starts_at) DO NOTHING`, w.EventID, w.StartsAt, w.EndsAt)
		if err != nil {
			return 0, fmt.Errorf("insert window: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit windows: %w", err)
	}
	return inserted, nil
}

// PreviousMark takes the greater of "what has been granted" and "where we were
// told to start". Both in one statement, because two round trips could see
// different states and the larger of the two is the only safe answer: paying
// from too far back pays twice, and there is no undo.
func (s *PGStore) PreviousMark(ctx context.Context, rewardID, userID int64) (int64, error) {
	var mark int64
	err := s.db.GetContext(ctx, &mark, `
		SELECT GREATEST(
		    COALESCE((SELECT max(reference) FROM reward_grants
		               WHERE reward_id = $1 AND user_id = $2), 0),
		    COALESCE((SELECT value FROM reward_baselines
		               WHERE reward_id = $1 AND user_id = $2), 0))`, rewardID, userID)
	if err != nil {
		return 0, fmt.Errorf("previous mark: %w", err)
	}
	return mark, nil
}

// SetBaseline never LOWERS an existing baseline. Re-running a seeding script
// must not move the line backwards past grants already keyed on it, which
// would re-pay everything in between.
func (s *PGStore) SetBaseline(ctx context.Context, rewardID, userID, value int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO reward_baselines (reward_id, user_id, value) VALUES ($1, $2, $3)
		ON CONFLICT (reward_id, user_id) DO UPDATE SET value = GREATEST(reward_baselines.value, EXCLUDED.value)`,
		rewardID, userID, value)
	if err != nil {
		return fmt.Errorf("set baseline: %w", err)
	}
	return nil
}

// PreviousMarks answers for a whole cohort at once. Same GREATEST(grant,
// baseline) rule as the single-member version -- expressed once, over a join,
// so the two can never drift apart into different answers.
func (s *PGStore) PreviousMarks(ctx context.Context, rewardID int64, userIDs []int64) (map[int64]int64, error) {
	if len(userIDs) == 0 {
		return map[int64]int64{}, nil
	}
	var rows []struct {
		UserID int64 `db:"user_id"`
		Mark   int64 `db:"mark"`
	}
	err := s.db.SelectContext(ctx, &rows, `
		SELECT u.id AS user_id,
		       GREATEST(
		           COALESCE((SELECT max(reference) FROM reward_grants g
		                      WHERE g.reward_id = $1 AND g.user_id = u.id), 0),
		           COALESCE((SELECT value FROM reward_baselines b
		                      WHERE b.reward_id = $1 AND b.user_id = u.id), 0)
		       ) AS mark
		  FROM unnest($2::bigint[]) AS u(id)`, rewardID, pq.Array(userIDs))
	if err != nil {
		return nil, fmt.Errorf("previous marks: %w", err)
	}
	out := make(map[int64]int64, len(rows))
	for _, r := range rows {
		out[r.UserID] = r.Mark
	}
	return out, nil
}
