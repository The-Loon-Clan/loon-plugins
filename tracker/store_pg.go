package tracker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"
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

func (s *PGStore) exec(ctx context.Context, q string, args ...any) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, q, args...)
		return err
	})
}

// ── Passkeys ────────────────────────────────────────────────────────────────

func (s *PGStore) UserByPasskey(ctx context.Context, passkey string) (int64, bool, error) {
	// An empty passkey must never match. The column is UNIQUE and NOT NULL, so
	// without this guard a request with no passkey at all would be a lookup for
	// "" — which finds nothing today, and would find the first badly-minted row
	// the day one exists. Cheaper to refuse it here than to rely on that.
	if passkey == "" {
		return 0, false, nil
	}
	var id int64
	err := s.get(ctx, &id, `SELECT user_id FROM passkeys WHERE passkey = $1`, passkey)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("user by passkey: %w", err)
	}
	return id, true, nil
}

func (s *PGStore) SetPasskey(ctx context.Context, userID int64, passkey string) error {
	if passkey == "" {
		return errors.New("tracker: refusing to store an empty passkey")
	}
	// rotated_at is stamped only on REPLACEMENT, hence the CASE: a first mint has
	// not rotated anything, and stamping it would make every member look like
	// they had once been rotated.
	err := s.exec(ctx, `
		INSERT INTO passkeys (user_id, passkey) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET
			passkey    = EXCLUDED.passkey,
			rotated_at = CASE WHEN passkeys.passkey <> EXCLUDED.passkey THEN now() ELSE passkeys.rotated_at END`,
		userID, passkey)
	if err != nil {
		return fmt.Errorf("set passkey: %w", err)
	}
	return nil
}

func (s *PGStore) Passkey(ctx context.Context, userID int64) (string, bool, error) {
	var pk string
	err := s.get(ctx, &pk, `SELECT passkey FROM passkeys WHERE user_id = $1`, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("passkey: %w", err)
	}
	return pk, true, nil
}

// ── The catalogue ───────────────────────────────────────────────────────────

const torrentCols = `info_hash, name, size, piece_length, file_count, files_json,
	info_bytes, uploaded_by, nzb_id, added_at, seeders, leechers, snatches`

func (s *PGStore) UpsertTorrent(ctx context.Context, t *Torrent) error {
	// info_bytes and the shape fields are NOT updated on conflict.
	//
	// The info_hash IS the hash of info_bytes, so a conflicting upload is by
	// definition the same torrent — rewriting the bytes could only ever replace
	// them with different bytes that hash the same, which is either impossible or
	// an attack. The mutable fields are the ones about how it got here.
	err := s.exec(ctx, `
		INSERT INTO torrents
			(info_hash, name, size, piece_length, file_count, files_json, info_bytes, uploaded_by, nzb_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (info_hash) DO UPDATE SET
			name        = EXCLUDED.name,
			uploaded_by = COALESCE(torrents.uploaded_by, EXCLUDED.uploaded_by),
			nzb_id      = COALESCE(torrents.nzb_id, EXCLUDED.nzb_id)`,
		t.InfoHash, t.Name, t.Size, t.PieceLength, t.FileCount, t.FilesJSON,
		t.InfoBytes, t.UploadedBy, t.NzbID)
	if err != nil {
		return fmt.Errorf("upsert torrent: %w", err)
	}
	return nil
}

func (s *PGStore) Torrent(ctx context.Context, infoHash string) (*Torrent, error) {
	var t Torrent
	err := s.get(ctx, &t, `SELECT `+torrentCols+` FROM torrents WHERE info_hash = $1`, infoHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("torrent %s: %w", infoHash, err)
	}
	return &t, nil
}

func (s *PGStore) TorrentByNzbID(ctx context.Context, nzbID int64) (*Torrent, error) {
	var t Torrent
	// ORDER BY added_at DESC: the schema permits two torrents for one release,
	// and a caller showing "the" swarm should show the current upload rather
	// than whichever row the planner reached first.
	err := s.get(ctx, &t,
		`SELECT `+torrentCols+` FROM torrents WHERE nzb_id = $1 ORDER BY added_at DESC LIMIT 1`, nzbID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("torrent for release %d: %w", nzbID, err)
	}
	return &t, nil
}

func (s *PGStore) ListTorrents(ctx context.Context, limit, offset int) ([]*Torrent, int, error) {
	if limit <= 0 {
		limit = 100
	}
	var total int
	if err := s.get(ctx, &total, `SELECT count(*) FROM torrents`); err != nil {
		return nil, 0, fmt.Errorf("count torrents: %w", err)
	}
	var rows []*Torrent
	// info_bytes is deliberately NOT selected here. It is the whole torrent's
	// info dict — kilobytes per row, and a listing renders none of it. Selecting
	// it would make a 100-row page a multi-megabyte TOAST read for nothing.
	err := s.sel(ctx, &rows, `
		SELECT info_hash, name, size, piece_length, file_count, files_json,
		       ''::bytea AS info_bytes, uploaded_by, nzb_id, added_at,
		       seeders, leechers, snatches
		  FROM torrents ORDER BY added_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list torrents: %w", err)
	}
	return rows, total, nil
}

// ── The announce path ───────────────────────────────────────────────────────

// ApplyAnnounceDelta is the single Postgres write an announce makes, lifted
// verbatim from the host.
//
// upDelta/downDelta must be non-negative. A negative delta means the client
// restarted and reset its own counters; the caller passes zero rather than
// letting the totals go backwards.
//
// completed is stored idempotently — `completed OR EXCLUDED.completed` — so it
// stays true once set. A client that re-announces after finishing must not undo
// its own snatch.
func (s *PGStore) ApplyAnnounceDelta(ctx context.Context, userID int64, infoHash string,
	upDelta, downDelta, left int64, completed bool) error {
	err := s.exec(ctx, `
		INSERT INTO user_stats
			(user_id, info_hash, uploaded, downloaded, left_bytes, completed, last_seen)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (user_id, info_hash) DO UPDATE SET
			uploaded   = user_stats.uploaded + EXCLUDED.uploaded,
			downloaded = user_stats.downloaded + EXCLUDED.downloaded,
			left_bytes = EXCLUDED.left_bytes,
			completed  = user_stats.completed OR EXCLUDED.completed,
			last_seen  = now()`,
		userID, infoHash, upDelta, downDelta, left, completed)
	if err != nil {
		return fmt.Errorf("apply announce delta: %w", err)
	}
	return nil
}

func (s *PGStore) IncrementSnatches(ctx context.Context, infoHash string) error {
	if err := s.exec(ctx, `UPDATE torrents SET snatches = snatches + 1 WHERE info_hash = $1`, infoHash); err != nil {
		return fmt.Errorf("increment snatches: %w", err)
	}
	return nil
}

func (s *PGStore) SetSwarmCounts(ctx context.Context, infoHash string, seeders, leechers int) error {
	err := s.exec(ctx, `UPDATE torrents SET seeders = $2, leechers = $3 WHERE info_hash = $1`,
		infoHash, seeders, leechers)
	if err != nil {
		return fmt.Errorf("set swarm counts: %w", err)
	}
	return nil
}

// ── Reads ───────────────────────────────────────────────────────────────────

func (s *PGStore) Totals(ctx context.Context, userID int64) (Totals, error) {
	var t Totals
	// Summed over user_stats rather than read from a counter, so there is no
	// second place to drift. Seeding/leeching are derived from left_bytes and
	// recency: "seeding" means nothing left and seen lately.
	err := s.get(ctx, &t, `
		SELECT coalesce(sum(uploaded),0)   AS uploaded,
		       coalesce(sum(downloaded),0) AS downloaded,
		       count(*) FILTER (WHERE left_bytes = 0 AND last_seen > now() - interval '1 hour') AS seeding,
		       count(*) FILTER (WHERE left_bytes > 0 AND last_seen > now() - interval '1 hour') AS leeching,
		       count(*) FILTER (WHERE completed) AS snatched
		  FROM user_stats WHERE user_id = $1`, userID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Totals{}, fmt.Errorf("totals: %w", err)
	}
	return t, nil
}

func (s *PGStore) ListUserStats(ctx context.Context, userID int64, limit int) ([]*UserStat, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows []*UserStat
	err := s.sel(ctx, &rows, `
		SELECT s.user_id, s.info_hash, s.uploaded, s.downloaded, s.seedtime,
		       s.left_bytes, s.completed, s.last_seen, t.name, t.size AS tsize
		  FROM user_stats s JOIN torrents t ON t.info_hash = s.info_hash
		 WHERE s.user_id = $1 ORDER BY s.last_seen DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list user stats: %w", err)
	}
	return rows, nil
}

// aggregateOrder is the ORDER BY allowlist.
//
// A map rather than string concatenation, and the reason is the SQL lint's whole
// purpose: sortBy arrives from a query parameter. Interpolating it would be
// injection; binding it as $N is impossible for an identifier. An allowlist is the
// third option, and an unknown key falls back rather than erroring because a
// mistyped sort in a URL should show a table, not a failure.
var aggregateOrder = map[string]string{
	"uploaded":   `sum(uploaded) DESC`,
	"downloaded": `sum(downloaded) DESC`,
	"last_seen":  `max(last_seen) DESC NULLS LAST`,
	"torrents":   `count(*) DESC`,
}

func (s *PGStore) ListAggregates(ctx context.Context, sortBy string, limit, offset int) ([]*Aggregate, int, error) {
	if limit <= 0 {
		limit = 50
	}
	order, ok := aggregateOrder[sortBy]
	if !ok {
		order = aggregateOrder["last_seen"]
	}
	var total int
	if err := s.get(ctx, &total, `SELECT count(DISTINCT user_id) FROM user_stats`); err != nil {
		return nil, 0, fmt.Errorf("count aggregates: %w", err)
	}
	var rows []*Aggregate
	// sqllint:allow order comes from aggregateOrder, an allowlist of literals; the
	// query parameter only selects a key and never reaches the SQL text.
	q := `
		SELECT user_id,
		       coalesce(sum(uploaded),0)   AS uploaded,
		       coalesce(sum(downloaded),0) AS downloaded,
		       count(*) FILTER (WHERE last_seen > now() - interval '1 hour') AS active_count,
		       count(*)                    AS torrent_count,
		       count(*) FILTER (WHERE completed) AS snatched_count,
		       max(last_seen)              AS last_seen
		  FROM user_stats GROUP BY user_id ORDER BY ` + order + ` LIMIT $1 OFFSET $2`
	if err := s.sel(ctx, &rows, q, limit, offset); err != nil {
		return nil, 0, fmt.Errorf("list aggregates: %w", err)
	}
	return rows, total, nil
}
