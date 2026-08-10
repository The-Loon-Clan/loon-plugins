package tracker

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// Storage for cheat detection: the previous sample, and what a sweep noticed.
//
// Separate from Store because it is separate machinery — the tracker runs
// perfectly well with none of this, and a host that never enables detection
// should not have it in the interface every implementation must satisfy. It
// hangs off PGStore directly for that reason, and MemStore does not implement
// it.

// CheatCandidate is one member/torrent pair with both readings side by side:
// the sample kept from last sweep, and what the counters say now.
type CheatCandidate struct {
	UserID      int64     `db:"user_id"`
	InfoHash    string    `db:"info_hash"`
	CurUp       int64     `db:"cur_up"`
	PrevUp      int64     `db:"prev_up"`
	PrevAt      time.Time `db:"prev_at"`
	TorrentSize int64     `db:"torrent_size"`
	Peers       int       `db:"peers"`
}

// CheatCandidates returns every pair that HAS a previous sample, joined to what
// the counters read now.
//
// Rows with no previous sample are absent on purpose: the first sighting of a
// member on a torrent has nothing to measure against, and judging it would mean
// dividing a lifetime total by the age of a snapshot taken seconds ago. They
// are recorded by SaveCheatSnapshots and judged from the next sweep on.
//
// limit bounds one sweep. The join is over user_stats, which is the largest
// table this plugin owns.
func (s *PGStore) CheatCandidates(ctx context.Context, limit int) ([]CheatCandidate, error) {
	if limit <= 0 {
		limit = 5000
	}
	var out []CheatCandidate
	err := s.sel(ctx, &out, `
		SELECT us.user_id,
		       us.info_hash,
		       us.uploaded                      AS cur_up,
		       cs.uploaded                      AS prev_up,
		       cs.taken_at                      AS prev_at,
		       coalesce(t.size, 0)              AS torrent_size,
		       coalesce(t.seeders + t.leechers, 0) AS peers
		  FROM user_stats us
		  JOIN cheat_snapshots cs
		    ON cs.user_id = us.user_id AND cs.info_hash = us.info_hash
		  LEFT JOIN torrents t ON t.info_hash = us.info_hash
		 WHERE us.uploaded > cs.uploaded
		 ORDER BY (us.uploaded - cs.uploaded) DESC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("cheat candidates: %w", err)
	}
	return out, nil
}

// SaveCheatSnapshots records the CURRENT counters as the baseline for next
// time, for every member/torrent pair that has moved bytes.
//
// One statement rather than a row per pair: this runs over the whole table on
// every sweep, and a per-row round trip would make the sweep's cost the network
// rather than the work.
func (s *PGStore) SaveCheatSnapshots(ctx context.Context, now time.Time) (int64, error) {
	var n int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO cheat_snapshots (user_id, info_hash, uploaded, taken_at)
			SELECT user_id, info_hash, uploaded, $1 FROM user_stats
			ON CONFLICT (user_id, info_hash)
			DO UPDATE SET uploaded = EXCLUDED.uploaded, taken_at = EXCLUDED.taken_at`, now)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("save cheat snapshots: %w", err)
	}
	return n, nil
}

// RecordCheatFlag writes one finding, or does nothing if the same rule is
// already open against the same member and torrent.
//
// The DO NOTHING is what keeps the queue readable: a sweep every fifteen
// minutes would otherwise turn one member's bad afternoon into ninety-six
// identical rows, and a queue nobody can read is the same as no queue.
func (s *PGStore) RecordCheatFlag(ctx context.Context, f CheatFinding) error {
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO cheat_flags (user_id, info_hash, kind, detail)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT DO NOTHING`, f.UserID, f.InfoHash, string(f.Kind), f.Detail)
		return err
	})
	if err != nil {
		return fmt.Errorf("record cheat flag: %w", err)
	}
	return nil
}

// CheatFlag is one row of the staff queue.
type CheatFlag struct {
	ID        int64     `db:"id"`
	UserID    int64     `db:"user_id"`
	InfoHash  string    `db:"info_hash"`
	Kind      string    `db:"kind"`
	Detail    string    `db:"detail"`
	CreatedAt time.Time `db:"created_at"`
	Name      string    `db:"name"` // join-derived: the torrent, for the reader
}

// OpenCheatFlags lists what is waiting to be looked at, newest first.
func (s *PGStore) OpenCheatFlags(ctx context.Context, limit int) ([]CheatFlag, error) {
	if limit <= 0 {
		limit = 100
	}
	var out []CheatFlag
	err := s.sel(ctx, &out, `
		SELECT f.id, f.user_id, f.info_hash, f.kind, f.detail, f.created_at,
		       coalesce(t.name, '') AS name
		  FROM cheat_flags f
		  LEFT JOIN torrents t ON t.info_hash = f.info_hash
		 WHERE f.cleared_at IS NULL
		 ORDER BY f.created_at DESC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("open cheat flags: %w", err)
	}
	return out, nil
}

// ClearCheatFlag records that somebody looked and it was fine.
//
// Cleared, never deleted: "staff reviewed this" is worth keeping, and the same
// rule firing again next sweep against a member who was already cleared once is
// a different conversation from a first reading.
func (s *PGStore) ClearCheatFlag(ctx context.Context, id, by int64) error {
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE cheat_flags SET cleared_at = now(), cleared_by = $2
			  WHERE id = $1 AND cleared_at IS NULL`, id, by)
		return err
	})
	if err != nil {
		return fmt.Errorf("clear cheat flag %d: %w", id, err)
	}
	return nil
}
