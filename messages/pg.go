package messages

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// ══ Direct messages ══════════════════════════════════════════════

// PGStore is the Postgres implementation of Store, over tables the HOST owns
// (dm_threads, dm_messages, dm_blocks, messages, message_reads).
//
// It takes a plain *sqlx.DB rather than a schema-scoped handle because these
// tables are shared: on the origin site the IRC plugin also writes DMs for
// whisper delivery. Ownership here is nominal — the schema belongs to the
// host, and schema.sql in this directory is what a host runs to provide it.
type PGStore struct {
	db *sqlx.DB
}

func NewPGStore(db *sqlx.DB) *PGStore {
	return &PGStore{db: db}
}

var _ Store = (*PGStore)(nil)

// dmBodyMax is the per-message body cap. Mirrored on the front end as
// a soft hint; this is the hard server-side limit.
const dmBodyMax = 8000

// EnsureDMThread sorts (userA, userB) into (lo, hi) so the UNIQUE
// constraint collapses both orderings to one row. ON CONFLICT keeps
// it race-safe — a concurrent inserter losing the race still gets
// back the existing id.
func (r *PGStore) EnsureDMThread(ctx context.Context, userA, userB int) (int64, bool, error) {
	if userA == userB {
		return 0, false, errors.New("dm: cannot thread with self")
	}
	lo, hi := userA, userB
	if hi < lo {
		lo, hi = hi, lo
	}
	var id int64
	err := r.db.QueryRowContext(ctx,
		`SELECT id FROM dm_threads WHERE user_lo_id = $1 AND user_hi_id = $2`, lo, hi).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	err = r.db.QueryRowContext(ctx, `
		INSERT INTO dm_threads (user_lo_id, user_hi_id)
		VALUES ($1, $2)
		ON CONFLICT (user_lo_id, user_hi_id) DO UPDATE SET user_lo_id = EXCLUDED.user_lo_id
		RETURNING id`, lo, hi).Scan(&id)
	return id, true, err
}

func (r *PGStore) IsDMBlocked(ctx context.Context, userA, userB int) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dm_blocks
		 WHERE (blocker_id = $1 AND blocked_id = $2)
		    OR (blocker_id = $2 AND blocked_id = $1)`, userA, userB).Scan(&n)
	return n > 0, err
}

// CreateDMMessage uses a writeable CTE so the insert + last_message_at
// bump + recipient soft-delete clear all land in one round-trip
// atomically — no explicit transaction needed.
//
// read_at is intentionally NULL at insert: the migration comment
// describes read_at as "per-recipient read state", and the unread
// query is `sender_id <> viewer AND read_at IS NULL`. The sender's
// own messages already get filtered out by `sender_id <> viewer`, so
// stamping read_at at insert would null out the unread count for
// every recipient. (An earlier draft of this code stamped NOW(); the
// restructure-time tests caught it.)
func (r *PGStore) CreateDMMessage(ctx context.Context, threadID int64, senderID int, body string) (int64, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return 0, errors.New("dm: empty body")
	}
	if len(body) > dmBodyMax {
		return 0, errors.New("dm: body too long (max 8000 chars)")
	}
	var id int64
	err := r.db.QueryRowContext(ctx, `
		WITH ins AS (
		    INSERT INTO dm_messages (thread_id, sender_id, body)
		    VALUES ($1, $2, $3)
		    RETURNING id
		),
		bump AS (
		    UPDATE dm_threads
		       SET last_message_at = NOW(),
		           lo_deleted_at = CASE WHEN user_lo_id = $2 THEN lo_deleted_at ELSE NULL END,
		           hi_deleted_at = CASE WHEN user_hi_id = $2 THEN hi_deleted_at ELSE NULL END
		     WHERE id = $1
		    RETURNING 1
		)
		SELECT id FROM ins`, threadID, senderID, body).Scan(&id)
	return id, err
}

func (r *PGStore) ListDMThreadsForUser(ctx context.Context, userID int) ([]*DMThreadView, error) {
	var rows []*DMThreadView
	err := r.db.SelectContext(ctx, &rows, `
		WITH last_msg AS (
		    SELECT DISTINCT ON (thread_id)
		           thread_id, sender_id, body, created_at
		      FROM dm_messages
		     ORDER BY thread_id, created_at DESC
		),
		unread AS (
		    SELECT thread_id, COUNT(*) AS cnt
		      FROM dm_messages
		     WHERE sender_id <> $1 AND read_at IS NULL
		     GROUP BY thread_id
		)
		SELECT t.id  AS thread_id,
		       CASE WHEN t.user_lo_id = $1 THEN t.user_hi_id ELSE t.user_lo_id END AS counterparty_id,
		       u.username AS counterparty_username,
		       COALESCE(u.avatar_path, '') AS counterparty_avatar_path,
		       COALESCE(lm.body, '')      AS last_message_body,
		       COALESCE(lm.sender_id, 0)  AS last_message_sender_id,
		       t.last_message_at,
		       COALESCE(ur.cnt, 0) AS unread_count
		  FROM dm_threads t
		  JOIN users u ON u.id = CASE WHEN t.user_lo_id = $1 THEN t.user_hi_id ELSE t.user_lo_id END
		  LEFT JOIN last_msg lm ON lm.thread_id = t.id
		  LEFT JOIN unread   ur ON ur.thread_id = t.id
		 WHERE (t.user_lo_id = $1 AND t.lo_deleted_at IS NULL)
		    OR (t.user_hi_id = $1 AND t.hi_deleted_at IS NULL)
		 ORDER BY t.last_message_at DESC
		 LIMIT 200`, userID)
	return rows, err
}

// GetDMThreadForUser collapses "not found" with "not a participant" so
// the caller's 404 response can't be probed for thread existence.
func (r *PGStore) GetDMThreadForUser(ctx context.Context, threadID int64, userID int) (*DMThread, error) {
	var t DMThread
	err := r.db.GetContext(ctx, &t, `
		SELECT id, user_lo_id, user_hi_id, last_message_at,
		       lo_deleted_at, hi_deleted_at, created_at
		  FROM dm_threads
		 WHERE id = $1 AND ($2 IN (user_lo_id, user_hi_id))`, threadID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &t, err
}

func (r *PGStore) ListDMMessagesForThread(ctx context.Context, threadID int64) ([]*DMMessageView, error) {
	var rows []*DMMessageView
	err := r.db.SelectContext(ctx, &rows, `
		SELECT m.id, m.thread_id, m.sender_id,
		       u.username                AS sender_username,
		       COALESCE(u.avatar_path,'') AS sender_avatar_path,
		       m.body, m.read_at, m.created_at
		  FROM dm_messages m
		  JOIN users u ON u.id = m.sender_id
		 WHERE m.thread_id = $1
		 ORDER BY m.created_at ASC`, threadID)
	return rows, err
}

func (r *PGStore) MarkDMThreadRead(ctx context.Context, threadID int64, viewerID int) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE dm_messages
		   SET read_at = NOW()
		 WHERE thread_id = $1
		   AND sender_id <> $2
		   AND read_at IS NULL`, threadID, viewerID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (r *PGStore) SoftDeleteDMThreadForUser(ctx context.Context, threadID int64, userID int) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE dm_threads
		   SET lo_deleted_at = CASE WHEN user_lo_id = $2 THEN NOW() ELSE lo_deleted_at END,
		       hi_deleted_at = CASE WHEN user_hi_id = $2 THEN NOW() ELSE hi_deleted_at END
		 WHERE id = $1 AND ($2 IN (user_lo_id, user_hi_id))`, threadID, userID)
	return err
}

func (r *PGStore) CountUnreadDMs(ctx context.Context, userID int) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM dm_messages m
		  JOIN dm_threads  t ON t.id = m.thread_id
		 WHERE m.sender_id <> $1
		   AND m.read_at IS NULL
		   AND ((t.user_lo_id = $1 AND t.lo_deleted_at IS NULL)
		     OR (t.user_hi_id = $1 AND t.hi_deleted_at IS NULL))`, userID).Scan(&n)
	return n, err
}

func (r *PGStore) CreateDMBlock(ctx context.Context, blockerID, blockedID int) error {
	if blockerID == blockedID {
		return errors.New("dm: cannot block self")
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO dm_blocks (blocker_id, blocked_id)
		VALUES ($1, $2)
		ON CONFLICT (blocker_id, blocked_id) DO NOTHING`, blockerID, blockedID)
	return err
}

func (r *PGStore) RemoveDMBlock(ctx context.Context, blockerID, blockedID int) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM dm_blocks WHERE blocker_id = $1 AND blocked_id = $2`,
		blockerID, blockedID)
	return err
}

func (r *PGStore) ListDMBlocks(ctx context.Context, blockerID int) ([]*DMBlockView, error) {
	var rows []*DMBlockView
	err := r.db.SelectContext(ctx, &rows, `
		SELECT b.blocked_id, u.username AS blocked_username, b.created_at
		  FROM dm_blocks b
		  JOIN users u ON u.id = b.blocked_id
		 WHERE b.blocker_id = $1
		 ORDER BY b.created_at DESC`, blockerID)
	return rows, err
}

// ══ Announcements ══════════════════════════════════════════════

func (r *PGStore) SendMessage(ctx context.Context, fromName, title, body, target string, expiresAt *time.Time) (*Announcement, error) {
	msg := &Announcement{}
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO messages (from_name, title, body, target, expires_at)
		 VALUES ($1,$2,$3,$4,$5)
		 RETURNING id, from_name, title, body, target, expires_at, created_at`,
		fromName, title, body, target, expiresAt,
	).Scan(&msg.ID, &msg.FromName, &msg.Title, &msg.Body, &msg.Target, &msg.ExpiresAt, &msg.CreatedAt)
	return msg, err
}

// GetMessagesForUser filters by target (broadcast / admin / specific
// user), expiry, and the per-user dismissed flag. The LEFT JOIN on
// message_reads pulls in read_at for the "(new)" indicator.
func (r *PGStore) GetMessagesForUser(ctx context.Context, userID int, isAdmin bool) ([]*Announcement, error) {
	userTarget := fmt.Sprintf("user:%d", userID)
	var msgs []*Announcement
	err := r.db.SelectContext(ctx, &msgs, `
		SELECT m.id, m.from_name, m.title, m.body, m.target, m.expires_at, m.created_at,
		       mr.read_at
		FROM messages m
		LEFT JOIN message_reads mr ON mr.message_id = m.id AND mr.user_id = $1
		WHERE (
		    m.target = 'all'
		    OR m.target = $2
		    OR ($3 AND m.target = 'admin')
		)
		AND (m.expires_at IS NULL OR m.expires_at > NOW())
		AND COALESCE(mr.dismissed, false) = false
		ORDER BY m.created_at DESC`,
		userID, userTarget, isAdmin,
	)
	return msgs, err
}

func (r *PGStore) DismissMessage(ctx context.Context, messageID int64, userID int) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO message_reads (message_id, user_id, dismissed)
		 VALUES ($1, $2, true)
		 ON CONFLICT (message_id, user_id) DO UPDATE SET dismissed = true`,
		messageID, userID,
	)
	return err
}

func (r *PGStore) GetUnreadCount(ctx context.Context, userID int, isAdmin bool) (int, error) {
	userTarget := fmt.Sprintf("user:%d", userID)
	var count int
	err := r.db.GetContext(ctx, &count, `
		SELECT COUNT(*)
		FROM messages m
		LEFT JOIN message_reads mr ON mr.message_id = m.id AND mr.user_id = $1
		WHERE (
		    m.target = 'all'
		    OR m.target = $2
		    OR ($3 AND m.target = 'admin')
		)
		AND (m.expires_at IS NULL OR m.expires_at > NOW())
		AND mr.read_at IS NULL`,
		userID, userTarget, isAdmin,
	)
	return count, err
}

func (r *PGStore) MarkMessageRead(ctx context.Context, messageID int64, userID int) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO message_reads (message_id, user_id) VALUES ($1, $2)
		 ON CONFLICT DO NOTHING`,
		messageID, userID,
	)
	return err
}

// GetAllMessages is the admin paginated view — returns every row,
// expired or not, dismissed or not. read_at is null in the response
// (admins look at system state, not their own state).
func (r *PGStore) GetAllMessages(ctx context.Context, limit, offset int) ([]*Announcement, int, error) {
	var msgs []*Announcement
	if err := r.db.SelectContext(ctx, &msgs, `
		SELECT id, from_name, title, body, target, expires_at, created_at, NULL::TIMESTAMPTZ AS read_at
		FROM messages ORDER BY created_at DESC LIMIT $1 OFFSET $2`,
		limit, offset); err != nil {
		return nil, 0, err
	}
	var total int
	if err := r.db.GetContext(ctx, &total, `SELECT COUNT(*) FROM messages`); err != nil {
		return nil, 0, err
	}
	return msgs, total, nil
}

func (r *PGStore) DeleteMessage(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM messages WHERE id = $1`, id)
	return err
}
