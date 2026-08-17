package tickets

// PGStore is the Postgres-backed implementation of
// PGStore. Extracted from *Storage in Phase 3.

import (
	"context"

	"github.com/jmoiron/sqlx"
)

type PGStore struct {
	db *sqlx.DB
}

func NewPGStore(db *sqlx.DB) *PGStore {
	return &PGStore{db: db}
}

func (r *PGStore) CreateTicket(ctx context.Context, userID int, username, subject, body, priority string) (*SupportTicket, error) {
	var t SupportTicket
	err := r.db.QueryRowxContext(ctx,
		`INSERT INTO support_tickets (user_id, username, subject, body, priority)
		 VALUES ($1,$2,$3,$4,$5)
		 RETURNING id, user_id, username, subject, body, priority, status, admin_note, created_at, updated_at`,
		userID, username, subject, body, priority,
	).StructScan(&t)
	return &t, err
}

func (r *PGStore) GetTickets(ctx context.Context, status string, limit, offset int) ([]*SupportTicket, int, error) {
	var total int
	var rows []*SupportTicket
	if status != "" {
		if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM support_tickets WHERE status=$1", status).Scan(&total); err != nil {
			return nil, 0, err
		}
		err := r.db.SelectContext(ctx, &rows,
			`SELECT id, user_id, username, subject, body, priority, status, admin_note, created_at, updated_at
			 FROM support_tickets WHERE status=$1 ORDER BY updated_at DESC LIMIT $2 OFFSET $3`,
			status, limit, offset)
		return rows, total, err
	}
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM support_tickets").Scan(&total); err != nil {
		return nil, 0, err
	}
	err := r.db.SelectContext(ctx, &rows,
		`SELECT id, user_id, username, subject, body, priority, status, admin_note, created_at, updated_at
		 FROM support_tickets ORDER BY updated_at DESC LIMIT $1 OFFSET $2`,
		limit, offset)
	return rows, total, err
}

func (r *PGStore) GetTicketsByUser(ctx context.Context, userID int) ([]*SupportTicket, error) {
	var rows []*SupportTicket
	err := r.db.SelectContext(ctx, &rows,
		`SELECT id, user_id, username, subject, body, priority, status, admin_note, created_at, updated_at
		 FROM support_tickets WHERE user_id=$1 ORDER BY updated_at DESC`,
		userID)
	return rows, err
}

func (r *PGStore) GetTicketByID(ctx context.Context, id int64) (*SupportTicket, error) {
	var t SupportTicket
	err := r.db.QueryRowxContext(ctx,
		`SELECT id, user_id, username, subject, body, priority, status, admin_note, created_at, updated_at
		 FROM support_tickets WHERE id=$1`, id).StructScan(&t)
	return &t, err
}

func (r *PGStore) UpdateTicketStatus(ctx context.Context, id int64, status, adminNote string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE support_tickets SET status=$2, admin_note=$3, updated_at=NOW() WHERE id=$1`,
		id, status, adminNote)
	return err
}

// ReopenTicketOnMemberReply flips a closed ticket back to open, and reports
// whether it actually did.
//
// Separate from UpdateTicketStatus because that one also rewrites admin_note,
// and a member replying must not blank a note staff left for staff. It is also
// guarded on status='closed' in SQL rather than by the caller reading first:
// the read-then-write version races two replies arriving together, and the loser
// reopens a ticket staff closed in between.
//
// The bool is the point. The caller only announces a reopen when a row actually
// changed, so a reply to an already-open ticket says nothing.
func (r *PGStore) ReopenTicketOnMemberReply(ctx context.Context, id int64) (bool, error) {
	res, err := r.db.ExecContext(ctx,
		`UPDATE support_tickets SET status='open', updated_at=NOW()
		  WHERE id=$1 AND status='closed'`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r *PGStore) DeleteTicket(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM support_tickets WHERE id=$1", id)
	return err
}

func (r *PGStore) GetTicketReplies(ctx context.Context, ticketID int64) ([]*TicketReply, error) {
	var rows []*TicketReply
	err := r.db.SelectContext(ctx, &rows,
		`SELECT id, ticket_id, user_id, username, body, is_admin, created_at
		 FROM ticket_replies WHERE ticket_id=$1 ORDER BY created_at ASC`, ticketID)
	return rows, err
}

// CreateTicketReply inserts a reply and bumps the parent ticket's
// updated_at so it surfaces in the most-recent-first ordering.
func (r *PGStore) CreateTicketReply(ctx context.Context, ticketID int64, userID int, username, body string, isAdmin bool) (*TicketReply, error) {
	var reply TicketReply
	err := r.db.QueryRowxContext(ctx,
		`INSERT INTO ticket_replies (ticket_id, user_id, username, body, is_admin)
		 VALUES ($1,$2,$3,$4,$5)
		 RETURNING id, ticket_id, user_id, username, body, is_admin, created_at`,
		ticketID, userID, username, body, isAdmin).StructScan(&reply)
	if err != nil {
		return nil, err
	}
	_, _ = r.db.ExecContext(ctx, "UPDATE support_tickets SET updated_at=NOW() WHERE id=$1", ticketID)
	return &reply, nil
}
