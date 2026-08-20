package applications

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"
)

// Status values. Three, and no more: an application is a question with a yes,
// a no, and not-yet.
const (
	StatusPending  = "pending"
	StatusAccepted = "accepted"
	StatusRejected = "rejected"
)

// Application is one request to join.
type Application struct {
	ID       int64  `db:"id"`
	Email    string `db:"email"`
	Username string `db:"username"`
	Body     string `db:"body"`
	Status   string `db:"status"`

	DecidedAt  *time.Time `db:"decided_at"`
	DecidedBy  *int64     `db:"decided_by"`
	Note       string     `db:"note"`
	InviteCode string     `db:"invite_code"`

	CreatedAt time.Time `db:"created_at"`
	IPHash    string    `db:"ip_hash"`
}

// Pending reports whether this one is still waiting — what the decide controls
// key on.
func (a Application) Pending() bool { return a.Status == StatusPending }

// Counts is the queue's summary.
type Counts struct {
	Pending  int `db:"pending"`
	Accepted int `db:"accepted"`
	Rejected int `db:"rejected"`
}

type Store interface {
	// Create records a new application.
	Create(ctx context.Context, a Application) error

	// PendingFor reports whether an address already has an application waiting.
	//
	// Only PENDING counts. Somebody rejected six months ago may reasonably
	// apply again, and a site that refused them forever would have built a
	// permanent ban out of a queue rule — which is a decision staff should
	// make deliberately, not one the schema makes for them.
	PendingFor(ctx context.Context, email string) (bool, error)

	// List returns applications by status, oldest first for the pending queue
	// and newest first for the decided ones.
	//
	// Oldest-first is not cosmetic: an application queue IS a queue, and the
	// person who has waited longest goes next. Newest-first on decided rows is
	// the opposite for the same reason — nobody reviews old decisions in order.
	List(ctx context.Context, status string, limit int) ([]Application, error)

	// Get loads one.
	Get(ctx context.Context, id int64) (Application, bool, error)

	// Decide records an outcome, and refuses to record a second one.
	//
	// The status check is in the STATEMENT: two moderators opening the same
	// queue and both clicking accept would otherwise issue two invites for one
	// applicant. false means somebody got there first.
	Decide(ctx context.Context, id int64, status string, by int64, note, inviteCode string) (bool, error)

	Counts(ctx context.Context) (Counts, error)
}

type PGStore struct{ db *core.SchemaDB }

func NewPGStore(db *core.SchemaDB) *PGStore { return &PGStore{db: db} }

var _ Store = (*PGStore)(nil)

func (s *PGStore) sel(ctx context.Context, dest any, q string, args ...any) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error { return tx.SelectContext(ctx, dest, q, args...) })
}

func (s *PGStore) get(ctx context.Context, dest any, q string, args ...any) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error { return tx.GetContext(ctx, dest, q, args...) })
}

func (s *PGStore) Create(ctx context.Context, a Application) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO applications (email, username, body, ip_hash)
			VALUES ($1, $2, $3, $4)`,
			strings.ToLower(strings.TrimSpace(a.Email)), a.Username, a.Body, a.IPHash)
		return err
	})
}

func (s *PGStore) PendingFor(ctx context.Context, email string) (bool, error) {
	var n int
	if err := s.get(ctx, &n, `
		SELECT count(*) FROM applications
		 WHERE email = $1 AND status = 'pending'`,
		strings.ToLower(strings.TrimSpace(email))); err != nil {
		return false, fmt.Errorf("pending application: %w", err)
	}
	return n > 0, nil
}

const appCols = `id, email, username, body, status, decided_at, decided_by,
	note, invite_code, created_at, ip_hash`

func (s *PGStore) List(ctx context.Context, status string, limit int) ([]Application, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []Application
	// The ordering flips with the status — see the interface. Two constant
	// statements rather than an interpolated ORDER BY, because a sort
	// direction assembled from a parameter is the shape sqllint exists to
	// refuse.
	q := `SELECT ` + appCols + ` FROM applications WHERE status = $1 ORDER BY created_at ASC LIMIT $2`
	if status != StatusPending {
		q = `SELECT ` + appCols + ` FROM applications WHERE status = $1 ORDER BY COALESCE(decided_at, created_at) DESC LIMIT $2`
	}
	if err := s.sel(ctx, &rows, q, status, limit); err != nil {
		return nil, fmt.Errorf("list applications: %w", err)
	}
	return rows, nil
}

func (s *PGStore) Get(ctx context.Context, id int64) (Application, bool, error) {
	var a Application
	if err := s.get(ctx, &a, `SELECT `+appCols+` FROM applications WHERE id = $1`, id); err != nil {
		// Absence is not an error: an id arrives from a form, and a stale one
		// is an ordinary event the caller answers with "already handled".
		return Application{}, false, nil
	}
	return a, true, nil
}

func (s *PGStore) Decide(ctx context.Context, id int64, status string, by int64, note, inviteCode string) (bool, error) {
	var n int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE applications
			   SET status = $1, decided_at = now(), decided_by = $2,
			       note = $3, invite_code = $4
			 WHERE id = $5 AND status = 'pending'`,
			status, by, note, inviteCode, id)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return false, fmt.Errorf("decide application: %w", err)
	}
	return n == 1, nil
}

func (s *PGStore) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	if err := s.get(ctx, &c, `
		SELECT count(*) FILTER (WHERE status = 'pending')  AS pending,
		       count(*) FILTER (WHERE status = 'accepted') AS accepted,
		       count(*) FILTER (WHERE status = 'rejected') AS rejected
		  FROM applications`); err != nil {
		return Counts{}, fmt.Errorf("application counts: %w", err)
	}
	return c, nil
}
