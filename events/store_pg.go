package events

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

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

// duration_seconds is a plain INTEGER, read and written as one. It was an
// INTERVAL when this came across from rewards, which cost a round trip through
// two grammars to move a single number — EXTRACT(EPOCH FROM …) on the way out
// and a "%d seconds" string for Postgres to re-parse on the way in.
//
// NULL means no duration, which is not zero: contiguous for a recurring event,
// never closing for a one-off. A zero-length window is a different (and useless)
// thing, which is why the column stays nullable.
const eventCols = `slug, name, description, coalesce(cron,'') AS cron,
	duration_seconds, starts_at, timezone, enabled`

type eventRow struct {
	Slug            string     `db:"slug"`
	Name            string     `db:"name"`
	Description     string     `db:"description"`
	Cron            string     `db:"cron"`
	DurationSeconds *int64     `db:"duration_seconds"`
	StartsAt        *time.Time `db:"starts_at"`
	Timezone        string     `db:"timezone"`
	Enabled         bool       `db:"enabled"`
}

func (r eventRow) toEvent() pluginapi.ScheduledEvent {
	ev := pluginapi.ScheduledEvent{
		Slug: r.Slug, Name: r.Name, Description: r.Description,
		Cron: r.Cron, StartsAt: r.StartsAt, Timezone: r.Timezone, Enabled: r.Enabled,
	}
	if r.DurationSeconds != nil {
		ev.Duration = time.Duration(*r.DurationSeconds) * time.Second
	}
	return ev
}

func (s *PGStore) ListEvents(ctx context.Context) ([]pluginapi.ScheduledEvent, error) {
	var rows []eventRow
	if err := s.sel(ctx, &rows, `SELECT `+eventCols+` FROM events ORDER BY slug`); err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	out := make([]pluginapi.ScheduledEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toEvent())
	}
	return out, nil
}

func (s *PGStore) GetEvent(ctx context.Context, slug string) (pluginapi.ScheduledEvent, bool, error) {
	var r eventRow
	err := s.get(ctx, &r, `SELECT `+eventCols+` FROM events WHERE slug = $1`, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return pluginapi.ScheduledEvent{}, false, nil
	}
	if err != nil {
		return pluginapi.ScheduledEvent{}, false, fmt.Errorf("get event %q: %w", slug, err)
	}
	return r.toEvent(), true, nil
}

func (s *PGStore) UpsertEvent(ctx context.Context, ev pluginapi.ScheduledEvent) error {
	var cron any
	if ev.Cron != "" {
		cron = ev.Cron
	}
	var dur any
	if ev.Duration > 0 {
		dur = int64(ev.Duration / time.Second)
	}
	tz := ev.Timezone
	if tz == "" {
		tz = "UTC"
	}
	_, err := s.exec(ctx, `
		INSERT INTO events (slug, name, description, cron, duration_seconds, starts_at, timezone, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (slug) DO UPDATE SET
			name = EXCLUDED.name, description = EXCLUDED.description,
			cron = EXCLUDED.cron, duration_seconds = EXCLUDED.duration_seconds,
			starts_at = EXCLUDED.starts_at, timezone = EXCLUDED.timezone,
			enabled = EXCLUDED.enabled`,
		ev.Slug, ev.Name, ev.Description, cron, dur, ev.StartsAt, tz, ev.Enabled)
	if err != nil {
		return fmt.Errorf("upsert event %q: %w", ev.Slug, err)
	}
	return nil
}

func (s *PGStore) DeleteEvent(ctx context.Context, slug string) error {
	if _, err := s.exec(ctx, `DELETE FROM events WHERE slug = $1`, slug); err != nil {
		return fmt.Errorf("delete event %q: %w", slug, err)
	}
	return nil
}

// windowRow carries the slug alongside the window so one query answers for many
// events without a second lookup to name them.
type windowRow struct {
	Slug     string    `db:"slug"`
	StartsAt time.Time `db:"starts_at"`
	EndsAt   time.Time `db:"ends_at"`
}

func (s *PGStore) OpenWindows(ctx context.Context, slugs []string, at time.Time) (map[string]pluginapi.EventWindow, error) {
	if len(slugs) == 0 {
		return map[string]pluginapi.EventWindow{}, nil
	}
	var rows []windowRow
	// DISTINCT ON picks the latest-starting open window per event. Two can
	// overlap only if an operator hand-authored them; the newest is the right
	// answer and picking deterministically beats returning whichever the plan
	// happened to emit.
	//
	// Half-open on purpose: starts_at <= at < ends_at, so back-to-back windows
	// of a contiguous event neither overlap nor gap at the boundary.
	err := s.sel(ctx, &rows, `
		SELECT DISTINCT ON (e.id) e.slug, w.starts_at, w.ends_at
		  FROM event_windows w
		  JOIN events e ON e.id = w.event_id
		 WHERE e.slug = ANY($1) AND e.enabled
		   AND w.starts_at <= $2 AND w.ends_at > $2
		 ORDER BY e.id, w.starts_at DESC`,
		pq.Array(slugs), at)
	if err != nil {
		return nil, fmt.Errorf("open windows: %w", err)
	}
	out := make(map[string]pluginapi.EventWindow, len(rows))
	for _, r := range rows {
		out[r.Slug] = pluginapi.EventWindow{Slug: r.Slug, Starts: r.StartsAt, Ends: r.EndsAt}
	}
	return out, nil
}

func (s *PGStore) AllOpen(ctx context.Context, at time.Time) (map[string]bool, error) {
	var slugs []string
	err := s.sel(ctx, &slugs, `
		SELECT DISTINCT e.slug
		  FROM event_windows w
		  JOIN events e ON e.id = w.event_id
		 WHERE e.enabled AND w.starts_at <= $1 AND w.ends_at > $1`, at)
	if err != nil {
		return nil, fmt.Errorf("all open: %w", err)
	}
	out := make(map[string]bool, len(slugs))
	for _, s := range slugs {
		out[s] = true
	}
	return out, nil
}

func (s *PGStore) LastWindowEnd(ctx context.Context, slug string) (time.Time, error) {
	var end *time.Time
	err := s.get(ctx, &end, `
		SELECT max(w.ends_at) FROM event_windows w
		  JOIN events e ON e.id = w.event_id WHERE e.slug = $1`, slug)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, fmt.Errorf("last window end %q: %w", slug, err)
	}
	if end == nil {
		return time.Time{}, nil
	}
	return *end, nil
}

func (s *PGStore) InsertWindows(ctx context.Context, slug string, ws []pluginapi.EventWindow) (int, error) {
	if len(ws) == 0 {
		return 0, nil
	}
	// One statement for the batch. A per-window INSERT would be a write per row
	// inside a loop, which is the thing CLAUDE.md names outright — and a yearly
	// generation pass over a daily event is 365 of them.
	starts := make([]time.Time, 0, len(ws))
	ends := make([]time.Time, 0, len(ws))
	for _, w := range ws {
		starts = append(starts, w.Starts)
		ends = append(ends, w.Ends)
	}
	n, err := s.exec(ctx, `
		INSERT INTO event_windows (event_id, starts_at, ends_at)
		SELECT e.id, s.starts, s.ends
		  FROM events e,
		       unnest($2::timestamptz[], $3::timestamptz[]) AS s(starts, ends)
		 WHERE e.slug = $1
		ON CONFLICT (event_id, starts_at) DO NOTHING`,
		slug, pq.Array(starts), pq.Array(ends))
	if err != nil {
		return 0, fmt.Errorf("insert windows %q: %w", slug, err)
	}
	return int(n), nil
}

func (s *PGStore) ListWindows(ctx context.Context, slug string, limit int) ([]pluginapi.EventWindow, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows []windowRow
	err := s.sel(ctx, &rows, `
		SELECT e.slug, w.starts_at, w.ends_at
		  FROM event_windows w
		  JOIN events e ON e.id = w.event_id
		 WHERE e.slug = $1
		 ORDER BY w.starts_at DESC
		 LIMIT $2`, slug, limit)
	if err != nil {
		return nil, fmt.Errorf("list windows %q: %w", slug, err)
	}
	out := make([]pluginapi.EventWindow, 0, len(rows))
	for _, r := range rows {
		out = append(out, pluginapi.EventWindow{Slug: r.Slug, Starts: r.StartsAt, Ends: r.EndsAt})
	}
	return out, nil
}
