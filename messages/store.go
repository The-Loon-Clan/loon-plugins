package messages

import (
	"context"
	"time"
)

// Store is the whole data surface of the messaging plugin: one-to-one DMs and
// the broadcast announcements that share the inbox with them.
//
// One interface rather than two, because the inbox renders both halves in a
// single list and a host implementing only one of them would produce a page
// that silently omits half its content.
//
// Send-eligibility ("may this user start a conversation?") is deliberately NOT
// here. That is an access decision, answered by core.Entitlements in the
// handler, so the rule can change without schema or storage churn.
type Store interface {
	// ── Direct messages ────────────────────────────────────────────────

	// EnsureDMThread returns the thread id for the (userA, userB) pair,
	// creating it if absent. Ids are stored as (lo, hi) so the same pair maps
	// to the same thread whoever initiated. The bool reports whether the row
	// was freshly created.
	EnsureDMThread(ctx context.Context, userA, userB int) (int64, bool, error)

	// IsDMBlocked is symmetric: true if EITHER party has blocked the other.
	// A blocker cannot message the person they blocked, which keeps the model
	// simple and closes the "block then keep talking at them" hole.
	IsDMBlocked(ctx context.Context, userA, userB int) (bool, error)

	// CreateDMMessage inserts one message, bumps the thread's
	// last_message_at, and clears the RECIPIENT's soft delete so a re-opened
	// conversation reappears in their inbox. The sender's read_at is stamped
	// at insert so "unread for me" excludes their own messages.
	CreateDMMessage(ctx context.Context, threadID int64, senderID int, body string) (int64, error)

	// ListDMThreadsForUser returns the viewer's conversations newest-first,
	// skipping ones they have soft-deleted.
	ListDMThreadsForUser(ctx context.Context, userID int) ([]*DMThreadView, error)

	// GetDMThreadForUser returns the thread only if the viewer participates in
	// it. Nil+nil covers both "no such thread" and "not yours", so a 404 can
	// never be used to probe whether a conversation exists.
	GetDMThreadForUser(ctx context.Context, threadID int64, userID int) (*DMThread, error)

	// ListDMMessagesForThread returns a thread's messages oldest-first. The
	// CALLER must gate access with GetDMThreadForUser first — this does not
	// re-check participation.
	ListDMMessagesForThread(ctx context.Context, threadID int64) ([]*DMMessageView, error)

	// MarkDMThreadRead stamps read_at on every message in the thread the
	// viewer RECEIVED, returning the row count so a caller can decrement a
	// badge in one go.
	MarkDMThreadRead(ctx context.Context, threadID int64, viewerID int) (int64, error)

	// SoftDeleteDMThreadForUser hides a thread from one side only.
	SoftDeleteDMThreadForUser(ctx context.Context, threadID int64, userID int) error

	// CountUnreadDMs is the navbar badge's number.
	CountUnreadDMs(ctx context.Context, userID int) (int, error)

	// CreateDMBlock — A blocks B. Idempotent.
	CreateDMBlock(ctx context.Context, blockerID, blockedID int) error
	// RemoveDMBlock — A unblocks B.
	RemoveDMBlock(ctx context.Context, blockerID, blockedID int) error
	// ListDMBlocks is who the viewer has blocked, with names.
	ListDMBlocks(ctx context.Context, blockerID int) ([]*DMBlockView, error)

	// ── Announcements (the broadcast half of the inbox) ────────────────

	// SendMessage inserts a broadcast. target is "all", "admins" or a user id
	// as a string; expiresAt nil means it never expires.
	SendMessage(ctx context.Context, fromName, title, body, target string, expiresAt *time.Time) (*Announcement, error)

	// GetMessagesForUser returns the viewer's visible, unexpired,
	// undismissed announcements with their per-viewer read stamp.
	GetMessagesForUser(ctx context.Context, userID int, isAdmin bool) ([]*Announcement, error)

	// GetAllMessages is the admin composer's list of everything sent, paged.
	// Returns the page plus the unpaged total, which is what the composer's
	// pagination needs.
	GetAllMessages(ctx context.Context, limit, offset int) ([]*Announcement, int, error)

	// GetUnreadCount is the announcement half of the navbar badge.
	GetUnreadCount(ctx context.Context, userID int, isAdmin bool) (int, error)

	// MarkMessageRead / DismissMessage are per-viewer: read keeps the row in
	// the list, dismiss removes it from that viewer's view only.
	MarkMessageRead(ctx context.Context, messageID int64, userID int) error
	DismissMessage(ctx context.Context, messageID int64, userID int) error

	// DeleteMessage removes a broadcast for everyone. Admin-only.
	DeleteMessage(ctx context.Context, messageID int64) error
}
