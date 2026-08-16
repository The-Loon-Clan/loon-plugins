package hitrun

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"
)

// Storage for the hit-and-run framework.
//
// Reads the tracker's accounting and writes only its own tables. The tracker
// schema is referenced by qualified name — search_path is scoped to this
// plugin's schema, so "tracker.user_stats" is deliberate and "user_stats" would
// silently resolve to nothing.
//
// An interface with a Postgres implementation, matching every other plugin
// here, so the job can be exercised against a fake without a database.

// Store is the persistence this plugin needs.
type Store interface {
	// Candidates returns snatches worth evaluating, newest announce first,
	// along with when each was pre-warned (zero if never).
	Candidates(ctx context.Context, limit int) ([]Candidate, error)
	// RecordPrewarning notes that the courtesy notice went out.
	RecordPrewarning(ctx context.Context, userID int64, infoHash string) error
	// IssueWarning records a warning. Idempotent on (user, torrent) — a member
	// is warned once per torrent, whatever the job schedule does.
	IssueWarning(ctx context.Context, w Warning) error
	// ActiveWarnings counts a member's warnings that have neither expired nor
	// been cleared.
	ActiveWarnings(ctx context.Context, userID int64) (int, error)
	// ExpireWarnings clears warnings past their expiry and returns how many.
	ExpireWarnings(ctx context.Context, now time.Time) (int, error)
	// ClearWarning lifts one warning early — a moderator's decision, or the
	// member having since satisfied the requirement.
	ClearWarning(ctx context.Context, userID int64, infoHash string) error
	// Standing is what a member is shown about themselves.
	Standing(ctx context.Context, userID int64) (Standing, error)
	// UserSnatches returns everything one member has taken, for their own
	// page. Unfiltered by policy: the page decides what to show, because the
	// answer depends on rules the store does not know.
	UserSnatches(ctx context.Context, userID int64) ([]Candidate, error)
}

// Candidate is one snatch plus the prewarning state the evaluator needs.
type Candidate struct {
	Snatch      Snatch
	TorrentName string
	PrewarnedAt time.Time
}

// Warning is an issued warning.
type Warning struct {
	UserID      int64
	InfoHash    string
	TorrentName string
	Reason      string
	IssuedAt    time.Time
	ExpiresAt   time.Time
}

// Standing is a member's own view: what they owe, and what it has cost them.
type Standing struct {
	ActiveWarnings int
	Warnings       []Warning
	// NO AtRisk field, and there deliberately was one. At-risk is a POLICY
	// judgment — snatches evaluated against the live rules — and the store
	// does not know the policy, so the field could only ever be empty. It was:
	// nothing populated it, the widget read it anyway, and its count was
	// permanently zero. Callers compute at-risk from UserSnatches + AtRisk()
	// the way /hitrun and the widget both do now; a field that cannot be
	// filled honestly is better not offered.
}

// activeWindow is how recently a member must have announced to count as still
// seeding.
//
// The tracker records last_seen per announce and has no "currently connected"
// column, so presence is inferred. Two hours is deliberately generous against a
// typical 30-minute announce interval: the cost of guessing "still here" is a
// warning delayed by one job run, and the cost of guessing "gone" is a warning
// somebody did not earn.
const activeWindow = 2 * time.Hour

// PGStore is the Postgres implementation.
type PGStore struct{ db *core.SchemaDB }

func NewPGStore(db *core.SchemaDB) *PGStore { return &PGStore{db: db} }

var _ Store = (*PGStore)(nil)

// candidateQuery is the join at the heart of this plugin.
//
// tracker.user_stats is per (member, torrent) and already holds everything the
// policy needs — downloaded, uploaded, seedtime, last_seen — so there is no
// second accounting to keep in step. torrents supplies the size the buffer is
// measured against.
//
// Rows already warned are excluded here rather than in Go: a warned torrent has
// nothing left to decide, and the count of rows the job walks is the thing that
// grows with the site.
const candidateQuery = `
	SELECT s.user_id, s.info_hash, t.name AS torrent_name, t.size AS torrent_size,
	       s.downloaded, s.uploaded, s.seedtime, s.last_seen,
	       COALESCE(p.sent_at, 'epoch'::timestamptz) AS prewarned_at
	  FROM tracker.user_stats s
	  JOIN tracker.torrents  t ON t.info_hash = s.info_hash
	  LEFT JOIN prewarnings  p ON p.user_id = s.user_id AND p.info_hash = s.info_hash
	  LEFT JOIN warnings     w ON w.user_id = s.user_id AND w.info_hash = s.info_hash
	                          AND w.cleared_at IS NULL
	 WHERE s.downloaded > 0
	   AND w.user_id IS NULL
	 ORDER BY s.last_seen DESC
	 LIMIT $1`

func (s *PGStore) Candidates(ctx context.Context, limit int) ([]Candidate, error) {
	if limit <= 0 {
		limit = 1000
	}
	var out []Candidate
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		rows, err := tx.QueryContext(ctx, candidateQuery, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		now := time.Now()
		for rows.Next() {
			var c Candidate
			var prewarned time.Time
			if err := rows.Scan(&c.Snatch.UserID, &c.Snatch.InfoHash, &c.TorrentName,
				&c.Snatch.TorrentSize, &c.Snatch.Downloaded, &c.Snatch.Uploaded,
				&c.Snatch.Seedtime, &c.Snatch.LastSeen, &prewarned); err != nil {
				return err
			}
			// Presence is inferred from the last announce — see activeWindow.
			c.Snatch.Seeding = now.Sub(c.Snatch.LastSeen) < activeWindow
			// 'epoch' stands in for "never pre-warned", because a LEFT JOIN
			// gives NULL and the evaluator wants a zero time. Anything before
			// 1971 is that sentinel rather than a real notice.
			if prewarned.Year() > 1971 {
				c.PrewarnedAt = prewarned
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

func (s *PGStore) RecordPrewarning(ctx context.Context, userID int64, infoHash string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO prewarnings (user_id, info_hash) VALUES ($1,$2)
			 ON CONFLICT (user_id, info_hash) DO NOTHING`, userID, infoHash)
		return err
	})
}

// IssueWarning is idempotent on (user, torrent). DO NOTHING rather than DO
// UPDATE: re-running the job must not push the expiry out, or a warning would
// last as long as the job keeps noticing it.
func (s *PGStore) IssueWarning(ctx context.Context, w Warning) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO warnings (user_id, info_hash, reason, issued_at, expires_at)
			 VALUES ($1,$2,$3,$4,$5)
			 ON CONFLICT (user_id, info_hash) DO NOTHING`,
			w.UserID, w.InfoHash, w.Reason, w.IssuedAt, w.ExpiresAt)
		return err
	})
}

func (s *PGStore) ActiveWarnings(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &n,
			`SELECT count(*) FROM warnings
			  WHERE user_id = $1 AND cleared_at IS NULL AND expires_at > now()`, userID)
	})
	return n, err
}

// ExpireWarnings retires warnings past their expiry.
//
// cleared_at is SET rather than the row deleted, so a member's history survives
// and a moderator can still see what happened. The count that blocks downloads
// already excludes them.
func (s *PGStore) ExpireWarnings(ctx context.Context, now time.Time) (int, error) {
	var n int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE warnings SET cleared_at = $1
			  WHERE cleared_at IS NULL AND expires_at <= $1`, now)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return int(n), err
}

func (s *PGStore) ClearWarning(ctx context.Context, userID int64, infoHash string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE warnings SET cleared_at = now()
			  WHERE user_id = $1 AND info_hash = $2 AND cleared_at IS NULL`, userID, infoHash)
		return err
	})
}

// UserSnatches is Candidates narrowed to one member, and WITHOUT the
// already-warned exclusion — a member should still see a torrent they were
// warned for, since reseeding it is how the warning stops mattering.
func (s *PGStore) UserSnatches(ctx context.Context, userID int64) ([]Candidate, error) {
	var out []Candidate
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT s.info_hash, t.name, t.size, s.downloaded, s.uploaded,
			       s.seedtime, s.last_seen,
			       COALESCE(p.sent_at, 'epoch'::timestamptz)
			  FROM tracker.user_stats s
			  JOIN tracker.torrents  t ON t.info_hash = s.info_hash
			  LEFT JOIN prewarnings  p ON p.user_id = s.user_id AND p.info_hash = s.info_hash
			 WHERE s.user_id = $1 AND s.downloaded > 0
			 ORDER BY s.last_seen DESC`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		now := time.Now()
		for rows.Next() {
			c := Candidate{Snatch: Snatch{UserID: userID}}
			var prewarned time.Time
			if err := rows.Scan(&c.Snatch.InfoHash, &c.TorrentName, &c.Snatch.TorrentSize,
				&c.Snatch.Downloaded, &c.Snatch.Uploaded, &c.Snatch.Seedtime,
				&c.Snatch.LastSeen, &prewarned); err != nil {
				return err
			}
			c.Snatch.Seeding = now.Sub(c.Snatch.LastSeen) < activeWindow
			if prewarned.Year() > 1971 {
				c.PrewarnedAt = prewarned
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

func (s *PGStore) Standing(ctx context.Context, userID int64) (Standing, error) {
	var st Standing
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT w.info_hash, COALESCE(t.name,''), w.reason, w.issued_at, w.expires_at
			   FROM warnings w
			   LEFT JOIN tracker.torrents t ON t.info_hash = w.info_hash
			  WHERE w.user_id = $1 AND w.cleared_at IS NULL AND w.expires_at > now()
			  ORDER BY w.issued_at DESC`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			w := Warning{UserID: userID}
			if err := rows.Scan(&w.InfoHash, &w.TorrentName, &w.Reason, &w.IssuedAt, &w.ExpiresAt); err != nil {
				return err
			}
			st.Warnings = append(st.Warnings, w)
		}
		st.ActiveWarnings = len(st.Warnings)
		return rows.Err()
	})
	return st, err
}
