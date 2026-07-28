//go:build integration

package usenet

import (
	"context"
	"testing"

	"github.com/jmoiron/sqlx"
)

// The hold's trigger is a SQL question — "does any CRITICAL group still have
// history on THIS backbone" — so it needs a real database. Getting it wrong in
// either direction is costly: a false positive starves every low group
// indefinitely, a false negative leaves the original starvation in place.
func TestCriticalBackfillPending(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	seed := func(name, tier, backbone string, back, low int64, done bool) {
		t.Helper()
		if err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO newsgroups (name, active, tier) VALUES ($1, TRUE, $2)
				 ON CONFLICT (name) DO UPDATE SET tier = EXCLUDED.tier`, name, tier); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx,
				`INSERT INTO newsgroup_state (group_name, backbone, back_watermark, server_low, backfill_done)
				 VALUES ($1,$2,$3,$4,$5)`, name, backbone, back, low, done)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Critical, 800M of history left, on backbone "netnews".
	seed("a.b.critical.behind", "critical", "netnews", 1_800_000_000, 1_000_000_000, false)
	// Critical but finished — must not hold anything.
	seed("a.b.critical.done", "critical", "netnews", 1_800_000_000, 1_000_000_000, true)
	// A LOW group with a vast backlog: the thing being held, never the reason.
	seed("a.b.low.huge", "low", "netnews", 9_000_000_000, 1_000, false)
	// Critical work outstanding on a DIFFERENT backbone.
	seed("a.b.critical.other", "critical", "eunews", 5_000, 1_000, false)

	got, err := s.criticalBackfillPending(ctx, "netnews")
	if err != nil {
		t.Fatal(err)
	}
	if got.Groups != 1 {
		t.Errorf("groups = %d, want 1 — only the unfinished CRITICAL group on this backbone counts", got.Groups)
	}
	if got.Articles != 800_000_000 {
		t.Errorf("articles = %d, want 800,000,000", got.Articles)
	}
	if got.Stalest != "a.b.critical.behind" {
		t.Errorf("stalest = %q", got.Stalest)
	}
	if !got.Any() {
		t.Error("outstanding critical history must hold the low tier")
	}

	// Per backbone: the other backbone has its own, smaller, answer.
	if other, err := s.criticalBackfillPending(ctx, "eunews"); err != nil {
		t.Fatal(err)
	} else if other.Groups != 1 || other.Articles != 4_000 {
		t.Errorf("eunews = %+v, want 1 group / 4,000 articles — progress is per backbone", other)
	}

	// A backbone with nothing outstanding must NOT hold the tier, or a
	// caught-up site starves its low groups forever.
	if none, err := s.criticalBackfillPending(ctx, "nosuchbackbone"); err != nil {
		t.Fatal(err)
	} else if none.Any() {
		t.Errorf("unknown backbone reported outstanding work: %+v", none)
	}
}

// server_low drifts UPWARD as articles expire off the server, so a group can
// read "negative history remaining" once retention has overtaken the back
// watermark. That is nothing left to do, not a negative amount of work — and
// summed with a real backlog a negative would understate it.
func TestPendingClampsExpiredHistory(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO newsgroups (name, active, tier) VALUES ('a.b.expired', TRUE, 'critical')`); err != nil {
			return err
		}
		// back_watermark BELOW server_low: retention overtook it.
		_, err := tx.ExecContext(ctx,
			`INSERT INTO newsgroup_state (group_name, backbone, back_watermark, server_low, backfill_done)
			 VALUES ('a.b.expired','netnews', 500, 9000, FALSE)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.criticalBackfillPending(ctx, "netnews")
	if err != nil {
		t.Fatal(err)
	}
	if got.Articles < 0 {
		t.Errorf("articles = %d — expired history must clamp to 0, not go negative", got.Articles)
	}
}
