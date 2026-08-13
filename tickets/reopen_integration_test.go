//go:build integration

package tickets

import (
	"context"
	"os"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

// Reopen-on-member-reply, against a real Postgres.
//
// The whole behaviour lives in one SQL guard — `WHERE id=$1 AND status='closed'`
// — so a Go-level fake would only assert what the fake was told to do. The two
// things worth proving are that the guard fires for a closed ticket and stays
// silent for an open one, and both are properties of the statement.
func testStore(t *testing.T) *PGStore {
	t.Helper()
	dsn := os.Getenv("TICKETS_TEST_DSN")
	if dsn == "" {
		dsn = os.Getenv("USENET_TEST_DSN")
	}
	if dsn == "" {
		t.Skip("set TICKETS_TEST_DSN (or USENET_TEST_DSN) to run the tickets integration tests")
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS support_tickets (
		  id BIGSERIAL PRIMARY KEY,
		  user_id INT NOT NULL DEFAULT 0,
		  subject TEXT NOT NULL DEFAULT '',
		  body TEXT NOT NULL DEFAULT '',
		  status TEXT NOT NULL DEFAULT 'open',
		  priority TEXT NOT NULL DEFAULT 'normal',
		  admin_note TEXT NOT NULL DEFAULT '',
		  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		  updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec(`TRUNCATE support_tickets RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return &PGStore{db: db}
}

func seedTicket(t *testing.T, s *PGStore, status, adminNote string) int64 {
	t.Helper()
	var id int64
	if err := s.db.QueryRow(
		`INSERT INTO support_tickets (user_id, subject, status, admin_note)
		 VALUES (1,'test',$1,$2) RETURNING id`, status, adminNote).Scan(&id); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return id
}

func statusOf(t *testing.T, s *PGStore, id int64) (string, string) {
	t.Helper()
	var status, note string
	if err := s.db.QueryRow(
		`SELECT status, admin_note FROM support_tickets WHERE id=$1`, id).Scan(&status, &note); err != nil {
		t.Fatalf("read: %v", err)
	}
	return status, note
}

func TestReopenFlipsAClosedTicket(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id := seedTicket(t, s, "closed", "")

	reopened, err := s.ReopenTicketOnMemberReply(ctx, id)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !reopened {
		t.Fatal("a closed ticket was not reopened — closing is a one-way door " +
			"and the member's reply lands where nothing lists it")
	}
	if got, _ := statusOf(t, s, id); got != "open" {
		t.Errorf("status = %q, want open", got)
	}
}

// An already-open ticket must report false, so the caller does not announce a
// reopen that did not happen.
func TestReopenIsANoOpOnAnOpenTicket(t *testing.T) {
	s := testStore(t)
	id := seedTicket(t, s, "open", "")

	reopened, err := s.ReopenTicketOnMemberReply(context.Background(), id)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened {
		t.Error("an already-open ticket reported as reopened")
	}
}

// The reason this is not UpdateTicketStatus: that one also writes admin_note,
// and a member replying must not blank a note staff left for staff.
func TestReopenPreservesTheAdminNote(t *testing.T) {
	s := testStore(t)
	const note = "escalated to the agent team, do not close"
	id := seedTicket(t, s, "closed", note)

	if _, err := s.ReopenTicketOnMemberReply(context.Background(), id); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	status, got := statusOf(t, s, id)
	if status != "open" {
		t.Errorf("status = %q, want open", status)
	}
	if got != note {
		t.Errorf("admin_note = %q, want it untouched (%q)", got, note)
	}
}

// in_progress is a real status in this model. Reopening should only act on
// 'closed' — flipping in_progress back to open would lose staff's own signal
// that somebody is already on it.
func TestReopenLeavesInProgressAlone(t *testing.T) {
	s := testStore(t)
	id := seedTicket(t, s, "in_progress", "")

	reopened, err := s.ReopenTicketOnMemberReply(context.Background(), id)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened {
		t.Error("an in_progress ticket was reopened, discarding the fact that " +
			"someone had already picked it up")
	}
	if got, _ := statusOf(t, s, id); got != "in_progress" {
		t.Errorf("status = %q, want in_progress", got)
	}
}
