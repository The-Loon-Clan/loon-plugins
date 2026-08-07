package rewards

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/the-loon-clan/loon/core"
)

// PGStore is the production Store.
//
// It holds the *core.SchemaDB, not the raw pool. Every statement below is
// unqualified, and the ONLY thing that makes `events` mean `rewards.events` is
// running inside SchemaDB.WithTx, which issues SET LOCAL search_path for that
// transaction. Unwrapping with .DB() yields a connection with no such scoping
// and every query then resolves against public -- which is exactly what
// shipped, because the test harness applied the schema unqualified and so had
// no separation to expose it.
type PGStore struct{ db *core.SchemaDB }

func NewPGStore(db *core.SchemaDB) *PGStore { return &PGStore{db: db} }

var _ Store = (*PGStore)(nil)

// sel / get / exec are the scoped equivalents of sqlx's SelectContext,
// GetContext and ExecContext. They exist so that reaching for an unscoped
// connection is not something a caller can do by accident: there is no
// s.db.Select to reach for.
func (s *PGStore) sel(ctx context.Context, dest any, q string, args ...any) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error { return tx.SelectContext(ctx, dest, q, args...) })
}

func (s *PGStore) get(ctx context.Context, dest any, q string, args ...any) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error { return tx.GetContext(ctx, dest, q, args...) })
}

func (s *PGStore) exec(ctx context.Context, q string, args ...any) (int64, error) {
	var n int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

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
	err := s.sel(ctx, &rows, `
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
	err := s.sel(ctx, &rows, `
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
	err := s.get(ctx, &row, `SELECT `+rewardCols+` FROM rewards WHERE `+clause, arg) // sqllint:allow clause is a compile-time literal, the value flows through $1
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
	err := s.sel(ctx, &rows, `
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
	err := s.sel(ctx, &rows, `
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
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return insertGrantTx(ctx, tx, &g, payouts)
	})
	if err != nil {
		return Grant{}, err
	}
	return g, nil
}

// insertGrantTx writes a grant and freezes its payout lines, inside a
// transaction the caller owns.
//
// Extracted from CreateGrant so an achievement completion can put the SAME
// writes in the SAME transaction as its user_achievements row — a second copy
// of this SQL is how the two paths would drift on which columns a grant needs.
// It mutates *g so the caller sees the assigned id, created_at and frozen
// payout lines.
func insertGrantTx(ctx context.Context, tx *sqlx.Tx, g *Grant, payouts []Payout) error {
	err := tx.QueryRowContext(ctx, `
		INSERT INTO reward_grants (reward_id, user_id, reference, state, reason, expires_at, silent)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id, created_at`,
		g.RewardID, g.UserID, g.Reference, string(g.State), g.Reason, g.ExpiresAt, g.Silent,
	).Scan(&g.ID, &g.CreatedAt)
	if uniqueViolation(err) {
		// The constraint arbitrated: someone else got there first. Not an
		// error condition, just the answer.
		return ErrAlreadyGranted
	}
	if err != nil {
		return fmt.Errorf("insert grant: %w", err)
	}

	g.Payouts = make([]Payout, 0, len(payouts))
	for _, p := range payouts {
		var id int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO reward_grant_payouts (grant_id, kind, target, amount)
			VALUES ($1, $2, NULLIF($3,''), $4) RETURNING id`,
			g.ID, string(p.Kind), p.Target, p.Amount).Scan(&id)
		if err != nil {
			return fmt.Errorf("freeze payout %s: %w", p.Kind, err)
		}
		p.ID = id
		g.Payouts = append(g.Payouts, p)
	}
	return nil
}

func (s *PGStore) GrantByID(ctx context.Context, id int64) (*Grant, error) {
	var g Grant
	// The rewards join exists solely for reward_slug — Settle hands it to
	// payout handlers so a ledger row can say WHICH reward paid. The grant row
	// itself never stores the slug; renaming a reward renames its history.
	err := s.get(ctx, &g, `
		SELECT g.id, g.reward_id, r.slug AS reward_slug, g.user_id, g.reference,
		       g.state, g.reason, g.created_at, g.expires_at, g.settled_at, g.silent
		  FROM reward_grants g JOIN rewards r ON r.id = g.reward_id
		 WHERE g.id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load grant: %w", err)
	}
	// Only unsettled lines: a resumed grant must not re-execute what already
	// landed, and the caller iterates whatever it is handed.
	err = s.sel(ctx, &g.Payouts, `
		SELECT id, kind, COALESCE(target,'') AS target, amount
		  FROM reward_grant_payouts
		 WHERE grant_id = $1 AND settled_at IS NULL ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("load frozen payouts: %w", err)
	}
	return &g, nil
}

func (s *PGStore) MarkPayoutSettled(ctx context.Context, payoutID int64, at time.Time) error {
	_, err := s.exec(ctx,
		`UPDATE reward_grant_payouts SET settled_at = $2 WHERE id = $1 AND settled_at IS NULL`,
		payoutID, at)
	return err
}

// SettleGrant marks a grant credited once every line has executed.
//
// `state <> 'credited'` rather than `= 'pending'`, because the expiry sweep
// can race a settle: Settle checks the state, the sweep flips it to expired,
// the payout lines execute anyway. At that point the money has moved, and the
// honest record is credited — an "expired" grant whose points were paid is a
// ledger reader's nightmare. Settle refuses to START on an expired grant, so
// the only path that reaches here from expired is that race, where payment
// has in fact happened.
func (s *PGStore) SettleGrant(ctx context.Context, grantID int64, at time.Time) error {
	_, err := s.exec(ctx, `
		UPDATE reward_grants SET state = 'credited', settled_at = $2
		 WHERE id = $1 AND state <> 'credited'`, grantID, at)
	return err
}

// ExpireGrants lapses pending grants past their expiry — EXCEPT ones with a
// settled payout line. A settled line means delivery is mid-flight: part of
// the payout has already left the building, and expiring the grant now would
// strand the remainder in a state Settle refuses to resume. Those grants
// belong to their settle, however long it takes; the sweep's business is only
// the ones nobody ever collected.
func (s *PGStore) ExpireGrants(ctx context.Context, now time.Time, limit int) (int, error) {
	n, err := s.exec(ctx, `
		UPDATE reward_grants SET state = 'expired'
		 WHERE id IN (SELECT g.id FROM reward_grants g
		               WHERE g.state = 'pending' AND g.expires_at IS NOT NULL AND g.expires_at <= $1
		                 AND NOT EXISTS (SELECT 1 FROM reward_grant_payouts p
		                                  WHERE p.grant_id = g.id AND p.settled_at IS NOT NULL)
		               ORDER BY g.expires_at LIMIT $2)`, now, limit)
	if err != nil {
		return 0, fmt.Errorf("expire grants: %w", err)
	}
	return int(n), nil
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
	err := s.sel(ctx, &rows, `
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
	err := s.get(ctx, &t,
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
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		inserted = 0 // guard against a re-invoked fn ever double-counting
		for _, w := range ws {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO event_windows (event_id, starts_at, ends_at) VALUES ($1, $2, $3)
				ON CONFLICT (event_id, starts_at) DO NOTHING`, w.EventID, w.StartsAt, w.EndsAt)
			if err != nil {
				return fmt.Errorf("insert window: %w", err)
			}
			if n, _ := res.RowsAffected(); n > 0 {
				inserted++
			}
		}
		return nil
	})
	return inserted, err
}

// PreviousMark takes the greater of "what has been granted" and "where we were
// told to start". Both in one statement, because two round trips could see
// different states and the larger of the two is the only safe answer: paying
// from too far back pays twice, and there is no undo.
func (s *PGStore) PreviousMark(ctx context.Context, rewardID, userID int64) (int64, error) {
	var mark int64
	err := s.get(ctx, &mark, `
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
	_, err := s.exec(ctx, `
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
	err := s.sel(ctx, &rows, `
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

// PendingGrantsFor drives the member-facing card, so it is deliberately two
// small indexed reads rather than a join: reward_grants_pending_idx is a
// partial index on (user_id, state) and the payout lines come back keyed by
// grant. A member has a handful of these at most.
func (s *PGStore) PendingGrantsFor(ctx context.Context, userID int64, limit int) ([]Grant, error) {
	var grants []Grant
	err := s.sel(ctx, &grants, `
		SELECT id, reward_id, user_id, reference, state, reason, created_at, expires_at, settled_at
		  FROM reward_grants
		 WHERE user_id = $1 AND state = 'pending'
		 ORDER BY id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("pending grants: %w", err)
	}
	if len(grants) == 0 {
		return nil, nil
	}
	ids := make([]int64, len(grants))
	idx := make(map[int64]*Grant, len(grants))
	for i := range grants {
		ids[i] = grants[i].ID
		idx[grants[i].ID] = &grants[i]
	}
	var lines []struct {
		GrantID int64      `db:"grant_id"`
		Kind    PayoutKind `db:"kind"`
		Target  string     `db:"target"`
		Amount  int        `db:"amount"`
	}
	err = s.sel(ctx, &lines, `
		SELECT grant_id, kind, COALESCE(target,'') AS target, amount
		  FROM reward_grant_payouts WHERE grant_id = ANY($1) ORDER BY id`, pq.Array(ids))
	if err != nil {
		return nil, fmt.Errorf("pending grant payouts: %w", err)
	}
	for _, l := range lines {
		if g := idx[l.GrantID]; g != nil {
			g.Payouts = append(g.Payouts, Payout{Kind: l.Kind, Target: l.Target, Amount: l.Amount})
		}
	}
	return grants, nil
}
