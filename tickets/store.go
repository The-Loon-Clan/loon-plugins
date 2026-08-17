package tickets

import (
	"context"
)

// SupportTicketRepository owns support_tickets + ticket_replies —
// the user-side report → admin-triage flow at /support. Phase 3
// extraction. (The opt-in publicity flag and /support/public feed
// were retired 2026-08-16; the column stays behind, unread.)
type Store interface {
	CreateTicket(ctx context.Context, userID int, username, subject, body, priority string) (*SupportTicket, error)
	GetTickets(ctx context.Context, status string, limit, offset int) ([]*SupportTicket, int, error)
	GetTicketsByUser(ctx context.Context, userID int) ([]*SupportTicket, error)
	GetTicketByID(ctx context.Context, id int64) (*SupportTicket, error)
	UpdateTicketStatus(ctx context.Context, id int64, status, adminNote string) error

	// ReopenTicketOnMemberReply reopens a CLOSED ticket when its owner replies,
	// returning whether a row changed. Without it, closing a ticket is a
	// one-way door: ReplyTicket writes the reply and never touches status, so a
	// member answering a closed ticket lands in a thread nothing surfaces as
	// awaiting staff.
	ReopenTicketOnMemberReply(ctx context.Context, id int64) (bool, error)
	DeleteTicket(ctx context.Context, id int64) error
	GetTicketReplies(ctx context.Context, ticketID int64) ([]*TicketReply, error)
	CreateTicketReply(ctx context.Context, ticketID int64, userID int, username, body string, isAdmin bool) (*TicketReply, error)
}
