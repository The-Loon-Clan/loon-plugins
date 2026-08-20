package comments

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/the-loon-clan/loon/core"
)

// Thanks — one member telling another that what they said was useful.
//
// WHO EARNS, AND WHY ONLY ONE OF THEM.
//
// The obvious design pays both parties a little, and the note in the gap list
// said as much. It is the wrong design: paying somebody to press thanks is how
// a site grows thanks-farming rings, and the cap that would stop it is not on
// thanks (one per comment already) but on COMMENTS, which are unlimited. Two
// accounts can generate as many comments as they like and thank each other's.
//
// So the author earns and the giver does not. A thanks is a gift rather than a
// trade, which is also what it is socially — nobody thanks a post hoping to be
// paid for it.
//
// WHY THE ROW IS NEVER DELETED. The award happens when the row is created, so
// withdrawing a thanks and giving it again finds the row already there and
// pays nothing. Deleting on withdrawal would make the button a faucet, and it
// is the kind of faucet nobody notices until a balance is absurd.

// thanksAward is what the author of a thanked comment earns.
//
// Small on purpose. It is a signal that somebody found the comment useful, not
// a wage: large enough that a helpful member accumulates something over a
// month of being helpful, small enough that nobody organises around it.
const thanksAward = 2

// thanksPath is where the button posts.
const thanksPath = "/p/comments/thanks"

// featureThanks is the switch (core.RegisterFeature). Checked in two places and
// both are needed: the view model, so the button and the counts stop being
// drawn, and the handler, because a form in somebody's browser outlives the
// page it came from.
const featureThanks = "comments.thanks"

// ThanksStore is the thanks half of the store.
type ThanksStore interface {
	// Toggle gives or withdraws a thanks and reports the state it left, plus
	// whether this was the FIRST time — the only case that pays.
	Toggle(ctx context.Context, commentID, userID int64) (thanked, first bool, err error)

	// ThanksFor returns, for a set of comments, how many live thanks each has
	// and which the viewer has given.
	//
	// Both in one call because a rendered thread needs both for every row, and
	// asking per comment would be two queries per comment on a page that
	// already has the ids.
	ThanksFor(ctx context.Context, commentIDs []int64, viewerID int64) (counts map[int64]int, mine map[int64]bool, err error)
}

var _ ThanksStore = (*PGStore)(nil)

// Toggle flips one member's thanks on one comment.
//
// The INSERT ... ON CONFLICT is the whole race argument: two clicks arriving
// together both run it, and the second finds the row and updates rather than
// inserting, so exactly one of them can ever be the first.
func (s *PGStore) Toggle(ctx context.Context, commentID, userID int64) (bool, bool, error) {
	var row struct {
		Withdrawn bool `db:"withdrawn"`
		First     bool `db:"first"`
	}
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &row, `
			INSERT INTO comment_thanks (comment_id, user_id)
			VALUES ($1, $2)
			ON CONFLICT (comment_id, user_id) DO UPDATE
			   -- Flip: withdrawn rows come back, standing rows are taken back.
			   SET withdrawn_at = CASE
			           WHEN comment_thanks.withdrawn_at IS NULL THEN now()
			           ELSE NULL
			       END
			RETURNING (withdrawn_at IS NOT NULL) AS withdrawn,
			          (xmax = 0)                 AS first`,
			commentID, userID)
	})
	if err != nil {
		return false, false, fmt.Errorf("toggle thanks: %w", err)
	}
	// xmax = 0 is Postgres's own answer to "was this an insertrather than an
	// update", read from the returned tuple. It is the only way to tell the
	// two apart in one statement, and telling them apart is what stops the
	// award running twice.
	return !row.Withdrawn, row.First, nil
}

func (s *PGStore) ThanksFor(ctx context.Context, ids []int64, viewerID int64) (map[int64]int, map[int64]bool, error) {
	counts, mine := map[int64]int{}, map[int64]bool{}
	if len(ids) == 0 {
		return counts, mine, nil
	}
	var rows []struct {
		CommentID int64 `db:"comment_id"`
		N         int   `db:"n"`
		Mine      bool  `db:"mine"`
	}
	err := s.sel(ctx, &rows, `
		SELECT comment_id,
		       count(*)                                    AS n,
		       bool_or(user_id = $2)                       AS mine
		  FROM comment_thanks
		 WHERE comment_id = ANY($1) AND withdrawn_at IS NULL
		 GROUP BY comment_id`, pq.Array(ids), viewerID)
	if err != nil {
		return nil, nil, fmt.Errorf("thanks for %d comment(s): %w", len(ids), err)
	}
	for _, r := range rows {
		counts[r.CommentID] = r.N
		if r.Mine {
			mine[r.CommentID] = true
		}
	}
	return counts, mine, nil
}

// handleThanks is the button.
func (p *Plugin) handleThanks(c *gin.Context) {
	u, ok := p.core.Auth.CurrentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}
	back := backTo(c)
	id, _ := strconv.ParseInt(c.PostForm("id"), 10, 64)
	if id <= 0 {
		c.Redirect(http.StatusSeeOther, back)
		return
	}
	ctx := c.Request.Context()

	// Switched off since the page was drawn. Silent, like the other two
	// refusals below: the button is not offered, so reaching here is a stale
	// form or a forged post rather than a member to explain something to.
	if !core.FeatureOn(p.core, featureThanks) {
		c.Redirect(http.StatusSeeOther, back)
		return
	}

	target, found, err := p.st.Get(ctx, id)
	if err != nil || !found {
		c.Redirect(http.StatusSeeOther, back)
		return
	}
	// Two refusals, both silent — the button is not offered in either case, so
	// reaching here is a forged post rather than a member to explain
	// something to.
	//
	//   own comment  — thanking yourself is the first thing anybody tries, and
	//                  it would pay you for commenting.
	//   removed      — a withheld comment must not still be earning.
	if target.UserID == u.ID || target.Deleted() {
		c.Redirect(http.StatusSeeOther, back)
		return
	}

	_, first, err := p.st.Toggle(ctx, id, u.ID)
	if err != nil {
		c.Redirect(http.StatusSeeOther, back)
		return
	}
	// Paid ONCE, on the first time only — see the file header on why the row
	// outlives a withdrawal. Best-effort: a thanks that was recorded must not
	// fail because the ledger did, and the member has already been told it
	// counted by the button changing state.
	if first && p.points != nil {
		if _, err := p.points.Award(ctx, target.UserID, thanksAward,
			"comment_thanks", "thanked for a comment", id); err != nil {
			p.logf("comments: award thanks: %v", err)
		}
	}
	c.Redirect(http.StatusSeeOther, back)
}

// pointsFor resolves the ledger at boot. Optional: without it thanks still
// work as a signal and simply pay nothing, which is a poorer feature but a
// working one — and far better than a button that errors.
func (p *Plugin) pointsFor(c *core.Core) core.PointsService { return c.Points }
