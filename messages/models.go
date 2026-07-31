package messages

import "time"

// The messaging domain's own row shapes.
//
// Lifted from the host's models package rather than imported: a portable
// plugin cannot depend on one site's model package, and these six types are
// the entire data surface. The `db` tags are kept identical to the host's so
// the SQL in pg_*.go is a verbatim lift too — the point of this port is that
// prod's behaviour comes across unchanged, not that it gets redesigned on the
// way.

// DMThread is the parent row for a one-to-one private conversation.
//
// user_lo_id / user_hi_id are stored in canonical order (LEAST, GREATEST) so
// there is exactly one row per pair regardless of who initiated. The
// viewer-aware list/get methods shuffle these back into "me / them".
//
// lo_deleted_at / hi_deleted_at are per-side soft deletes: setting the side
// matching the viewer removes the thread from their inbox without touching
// the other party's copy, and a new message clears the recipient's.
type DMThread struct {
	ID            int64      `db:"id"`
	UserLoID      int        `db:"user_lo_id"`
	UserHiID      int        `db:"user_hi_id"`
	LastMessageAt time.Time  `db:"last_message_at"`
	LoDeletedAt   *time.Time `db:"lo_deleted_at"`
	HiDeletedAt   *time.Time `db:"hi_deleted_at"`
	CreatedAt     time.Time  `db:"created_at"`
}

// DMMessage is one message in a thread.
//
// read_at is per-recipient: the sender's row is stamped at insert time, so
// "unread for me" is just `sender_id != $1 AND read_at IS NULL`.
type DMMessage struct {
	ID        int64      `db:"id"`
	ThreadID  int64      `db:"thread_id"`
	SenderID  int        `db:"sender_id"`
	Body      string     `db:"body"`
	ReadAt    *time.Time `db:"read_at"`
	CreatedAt time.Time  `db:"created_at"`
}

// DMThreadView is the conversation-list row: thread fields plus the
// counterparty's identity joined in, plus the per-viewer unread count.
//
// CounterpartyAvatarPath may be empty; the template falls back to a coloured
// initial-letter circle.
type DMThreadView struct {
	ThreadID               int64     `db:"thread_id"`
	CounterpartyID         int       `db:"counterparty_id"`
	CounterpartyUsername   string    `db:"counterparty_username"`
	CounterpartyAvatarPath string    `db:"counterparty_avatar_path"`
	LastMessageBody        string    `db:"last_message_body"`
	LastMessageSenderID    int       `db:"last_message_sender_id"`
	LastMessageAt          time.Time `db:"last_message_at"`
	UnreadCount            int       `db:"unread_count"`
}

// DMMessageView is the per-message row rendered in a thread, with the
// sender's identity joined so the template does not look it up per row.
type DMMessageView struct {
	ID             int64      `db:"id"`
	ThreadID       int64      `db:"thread_id"`
	SenderID       int        `db:"sender_id"`
	SenderUsername string     `db:"sender_username"`
	SenderAvatar   string     `db:"sender_avatar_path"`
	Body           string     `db:"body"`
	ReadAt         *time.Time `db:"read_at"`
	CreatedAt      time.Time  `db:"created_at"`
}

// DMBlockView is one blocked user, joined with their name so a settings page
// renders names rather than ids.
type DMBlockView struct {
	BlockedID       int       `db:"blocked_id"`
	BlockedUsername string    `db:"blocked_username"`
	CreatedAt       time.Time `db:"created_at"`
}

// Announcement is a broadcast message in the unified inbox — the system half,
// as opposed to the DM half.
//
// Named Announcement rather than the host's Message: in this package "message"
// already means a DM, and two things called Message in one inbox is how a
// reader ends up asking which one a function returns.
type Announcement struct {
	ID        int64      `db:"id"`
	FromName  string     `db:"from_name"`
	Title     string     `db:"title"`
	Body      string     `db:"body"`
	Target    string     `db:"target"`
	ExpiresAt *time.Time `db:"expires_at"`
	CreatedAt time.Time  `db:"created_at"`
	ReadAt    *time.Time `db:"read_at"`
}
