package tracker

import (
	"context"
	"time"
)

// Torrent is one row of the tracker's catalogue.
//
// A lift of the host's models.TrackerTorrent, with UploadedBy widened to int64
// and the users foreign key dropped — see the migration for why a plugin does not
// hard-link to host tables.
type Torrent struct {
	InfoHash    string    `db:"info_hash"`
	Name        string    `db:"name"`
	Size        int64     `db:"size"`
	PieceLength int64     `db:"piece_length"`
	FileCount   int       `db:"file_count"`
	FilesJSON   []byte    `db:"files_json"`
	InfoBytes   []byte    `db:"info_bytes"`
	UploadedBy  *int64    `db:"uploaded_by"`
	NzbID       *int64    `db:"nzb_id"`
	AddedAt     time.Time `db:"added_at"`
	Seeders     int       `db:"seeders"`
	Leechers    int       `db:"leechers"`
	Snatches    int       `db:"snatches"`
}

// UserStat is one member's counters against one torrent.
type UserStat struct {
	UserID     int64     `db:"user_id"`
	InfoHash   string    `db:"info_hash"`
	Uploaded   int64     `db:"uploaded"`
	Downloaded int64     `db:"downloaded"`
	Seedtime   int64     `db:"seedtime"`
	LeftBytes  int64     `db:"left_bytes"`
	Completed  bool      `db:"completed"`
	LastSeen   time.Time `db:"last_seen"`

	// Name and Size are join-derived for the "my stats" table, never stored.
	// One round trip instead of a torrent lookup per row.
	Name  string `db:"name"`
	TSize int64  `db:"tsize"`
}

// Totals is a member's whole-tracker position, summed over user_stats rather than
// kept as its own counter — so there is no second place to drift.
type Totals struct {
	Uploaded   int64 `db:"uploaded"`
	Downloaded int64 `db:"downloaded"`
	Seeding    int   `db:"seeding"`
	Leeching   int   `db:"leeching"`
	Snatched   int   `db:"snatched"`
}

// Ratio returns Uploaded/Downloaded, and treats an upload-only member as their
// upload figure rather than as +Inf.
//
// Lifted from the host's TrackerUserAggregate.Ratio, including the part that
// looks arbitrary and is not: both-zero returns 0 so an inactive member sorts to
// the BOTTOM of a ratio-ordered admin table. +Inf would sort them to the top,
// which is the opposite of useful.
func (t Totals) Ratio() float64 {
	if t.Downloaded == 0 {
		if t.Uploaded == 0 {
			return 0
		}
		return float64(t.Uploaded)
	}
	return float64(t.Uploaded) / float64(t.Downloaded)
}

// Aggregate is one row of the admin oversight table.
//
// No Username and no TrackerAccess, unlike the host's TrackerUserAggregate which
// joined `users` for both. This plugin cannot join another schema's table, and
// should not want to: names come from core.Users at render time, and access is an
// entitlement rather than a column. What is left here is the arithmetic, which is
// this plugin's business.
type Aggregate struct {
	UserID        int64      `db:"user_id"`
	Uploaded      int64      `db:"uploaded"`
	Downloaded    int64      `db:"downloaded"`
	ActiveCount   int        `db:"active_count"`
	TorrentCount  int        `db:"torrent_count"`
	SnatchedCount int        `db:"snatched_count"`
	LastSeen      *time.Time `db:"last_seen"`
}

// Store is the tracker's data layer.
type Store interface {
	// ── Passkeys: the tracker's whole authentication story ──────────────────

	// UserByPasskey resolves an announce's passkey to a member id. The bool is
	// false for an unknown passkey, which is not an error: an announce with a
	// stale key is an ordinary event and the caller answers it with a bencoded
	// failure rather than a 500.
	UserByPasskey(ctx context.Context, passkey string) (int64, bool, error)

	// SetPasskey mints or rotates a member's passkey. Rotation stamps
	// rotated_at, so a member can be told when their old .torrent files stopped
	// working instead of discovering it as a silent announce failure.
	SetPasskey(ctx context.Context, userID int64, passkey string) error

	// Passkey returns a member's current passkey, or false if they have none.
	Passkey(ctx context.Context, userID int64) (string, bool, error)

	// ── The catalogue ───────────────────────────────────────────────────────

	UpsertTorrent(ctx context.Context, t *Torrent) error
	Torrent(ctx context.Context, infoHash string) (*Torrent, error)
	ListTorrents(ctx context.Context, limit, offset int) ([]*Torrent, int, error)

	// ── The announce path ───────────────────────────────────────────────────

	// ApplyAnnounceDelta upserts one member's counters against one torrent.
	//
	// Deltas, not absolutes: a client reports cumulative totals per session, and
	// the difference since its last announce is what this adds. One statement,
	// because an announce happens every few minutes per peer and this is the
	// hottest write on the tracker.
	ApplyAnnounceDelta(ctx context.Context, userID int64, infoHash string, upDelta, downDelta, left int64, completed bool) error

	// IncrementSnatches records a completed download on the torrent.
	IncrementSnatches(ctx context.Context, infoHash string) error

	// SetSwarmCounts writes the denormalised seeder/leecher figures a listing
	// reads, recomputed from the peer store rather than accumulated.
	SetSwarmCounts(ctx context.Context, infoHash string, seeders, leechers int) error

	// ── Reads for the member and admin pages ────────────────────────────────

	Totals(ctx context.Context, userID int64) (Totals, error)
	ListUserStats(ctx context.Context, userID int64, limit int) ([]*UserStat, error)

	// ListAggregates returns the admin oversight rows. sortBy is an allowlisted
	// column name, never interpolated user input — see the implementation.
	ListAggregates(ctx context.Context, sortBy string, limit, offset int) ([]*Aggregate, int, error)
}
