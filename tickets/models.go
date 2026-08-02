package tickets

import "time"

type SupportTicket struct {
	ID        int64  `db:"id"`
	UserID    int    `db:"user_id"`
	Username  string `db:"username"`
	Subject   string `db:"subject"`
	Body      string `db:"body"`
	Priority  string `db:"priority"` // low | normal | high
	Status    string `db:"status"`   // open | in_progress | closed
	AdminNote string `db:"admin_note"`
	// Public is the owner-controlled opt-in flag (migration 207).
	// Default false; flipping true exposes the ticket on
	// /support/public for any visitor to read. Body + replies
	// become visible; the owner can flip back to private anytime.
	Public    bool      `db:"public"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

type TicketReply struct {
	ID        int64     `db:"id"`
	TicketID  int64     `db:"ticket_id"`
	UserID    int       `db:"user_id"`
	Username  string    `db:"username"`
	Body      string    `db:"body"`
	IsAdmin   bool      `db:"is_admin"`
	CreatedAt time.Time `db:"created_at"`
}
