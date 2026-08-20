package comments

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"
)

// Comment is one thing somebody said.
type Comment struct {
	ID          int64      `db:"id"`
	SubjectKind string     `db:"subject_kind"`
	SubjectID   int64      `db:"subject_id"`
	UserID      int64      `db:"user_id"`
	Body        string     `db:"body"`
	CreatedAt   time.Time  `db:"created_at"`
	EditedAt    *time.Time `db:"edited_at"`
	DeletedAt   *time.Time `db:"deleted_at"`
	DeletedBy   *int64     `db:"deleted_by"`
}

// Deleted reports whether the body should be withheld.
func (c Comment) Deleted() bool { return c.DeletedAt != nil }

// Edited reports whether it changed after posting.
func (c Comment) Edited() bool { return c.EditedAt != nil }

type Store interface {
	// Add posts a comment and returns its id.
	Add(ctx context.Context, c Comment) (int64, error)

	// List returns one subject's comments, oldest first — a conversation is
	// read in the order it happened.
	//
	// Deleted rows are INCLUDED, with their bodies still in them: withholding
	// is the caller's job because staff may see what an ordinary member may
	// not, and a store that stripped the body would make that impossible
	// without a second query.
	List(ctx context.Context, kind string, id int64, limit int) ([]Comment, error)

	// Get loads one, for the edit and delete paths that must check ownership.
	Get(ctx context.Context, id int64) (Comment, bool, error)

	// Edit replaces a body. Only the author, enforced in the statement.
	Edit(ctx context.Context, id, userID int64, body string) (bool, error)

	// Delete withholds one. byUser must be the author unless staff is true.
	Delete(ctx context.Context, id, byUser int64, staff bool) (bool, error)
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

const cols = `id, subject_kind, subject_id, user_id, body, created_at, edited_at, deleted_at, deleted_by`

func (s *PGStore) Add(ctx context.Context, c Comment) (int64, error) {
	var id int64
	if err := s.get(ctx, &id, `
		INSERT INTO comments (subject_kind, subject_id, user_id, body)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		c.SubjectKind, c.SubjectID, c.UserID, strings.TrimSpace(c.Body)); err != nil {
		return 0, fmt.Errorf("add comment: %w", err)
	}
	return id, nil
}

func (s *PGStore) List(ctx context.Context, kind string, id int64, limit int) ([]Comment, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var rows []Comment
	if err := s.sel(ctx, &rows, `
		SELECT `+cols+` FROM comments
		 WHERE subject_kind = $1 AND subject_id = $2
		 ORDER BY created_at ASC
		 LIMIT $3`, kind, id, limit); err != nil {
		return nil, fmt.Errorf("list comments: %w", err)
	}
	return rows, nil
}

func (s *PGStore) Get(ctx context.Context, id int64) (Comment, bool, error) {
	var c Comment
	if err := s.get(ctx, &c, `SELECT `+cols+` FROM comments WHERE id = $1`, id); err != nil {
		// Absence is not an error: an id arrives from a form, and a stale one
		// is ordinary.
		return Comment{}, false, nil
	}
	return c, true, nil
}

// Edit rewrites a body, and only the author's own.
//
// The user_id check is IN the statement rather than a read beforehand, for the
// reason every authorisation here is: a check that happens in a separate query
// is a check with a gap in it. deleted_at IS NULL too — editing a removed
// comment back into existence would undo a moderator without saying so.
func (s *PGStore) Edit(ctx context.Context, id, userID int64, body string) (bool, error) {
	var n int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE comments SET body = $1, edited_at = now()
			 WHERE id = $2 AND user_id = $3 AND deleted_at IS NULL`,
			strings.TrimSpace(body), id, userID)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return false, fmt.Errorf("edit comment: %w", err)
	}
	return n == 1, nil
}

// Delete withholds a comment.
//
// staff widens the ownership check rather than skipping it, so there is one
// statement that decides what deleting means. deleted_by records WHO, which is
// the difference between an author changing their mind and a moderator acting
// — and the two need telling apart when somebody asks why their comment is
// gone.
func (s *PGStore) Delete(ctx context.Context, id, byUser int64, staff bool) (bool, error) {
	owner := byUser
	if staff {
		owner = 0 // 0 matches no user_id, so the OR below becomes "any author"
	}
	var n int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE comments SET deleted_at = now(), deleted_by = $1
			 WHERE id = $2 AND deleted_at IS NULL
			   AND ($3 = 0 OR user_id = $3)`, byUser, id, owner)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return false, fmt.Errorf("delete comment: %w", err)
	}
	return n == 1, nil
}
