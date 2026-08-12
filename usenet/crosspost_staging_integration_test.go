//go:build integration

package usenet

import (
	"context"
	"fmt"
	"testing"
)

// A crossposted article must stage in EVERY group it appears in.
//
// RFC 5536 s3.1.3 makes the Message-ID the article's identity, and a crosspost
// is one article filed under several groups carrying that same id in each. So
// crawling N groups yields the id N times, legitimately.
//
// 001_init keyed the staging table on message_id ALONE, with
// ON CONFLICT (message_id) DO NOTHING. That reads as correct dedup and is
// scoped one level too wide: the staged SET is (group_name, base_subject), so
// the first group to reach an article claimed it globally and every later
// group's copy was silently swallowed. The second group's set stayed short by
// exactly those articles, never satisfied isComplete, never built, never
// salvaged (pg mode runs no walk-past sweep), and expired unlogged — a release
// that simply never appeared, with nothing recording why.
//
// Migration 031 scopes the key to (group_name, message_id), which keeps the
// original intent (one copy per group) and lets a crosspost populate them all.
func TestPGCrosspostStagesInEveryGroup(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	const (
		base   = "Call.Of.The.Night.S02.Crosspost"
		nParts = 12
	)
	groups := []string{"alt.binaries.teevee", "alt.binaries.mom", "alt.binaries.misc"}

	// The SAME message-ids offered under each group, as a real crosspost is.
	var arts []stagedArticle
	for _, g := range groups {
		for i := 0; i < nParts; i++ {
			arts = append(arts, stagedArticle{
				Group: g, BaseSubject: base,
				Subject:   fmt.Sprintf("%s (%d/%d)", base, i+1, nParts),
				MessageID: fmt.Sprintf("<xpost-%d@news.example>", i),
				Poster:    "iVy", Bytes: 700000,
				PartNum: i + 1, TotalParts: nParts, SegTotal: nParts,
			})
		}
	}

	if _, err := s.stageArticles(ctx, arts); err != nil {
		t.Fatalf("stage crosspost: %v", err)
	}

	// Every group must hold the full set. Under the old global key the first
	// group held 12 and the other two held 0.
	for _, g := range groups {
		var n int
		if err := s.db.DB().QueryRow(
			`SELECT count(*) FROM `+s.db.Schema()+`.articles WHERE group_name = $1 AND base_subject = $2`,
			g, base).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", g, err)
		}
		if n != nParts {
			t.Errorf("group %s staged %d of %d articles — a crossposted article must stage "+
				"in every group; its set is otherwise permanently short and never builds", g, n, nParts)
		}
	}

	// And the original intent still holds: re-offering the same article to the
	// same group is still a no-op, not a duplicate row.
	staged, err := s.stageArticles(ctx, arts)
	if err != nil {
		t.Fatalf("restage: %v", err)
	}
	if staged != 0 {
		t.Errorf("restaging the identical articles staged %d rows, want 0 — the per-group "+
			"dedup the ON CONFLICT exists for must survive the key change", staged)
	}
	var total int
	if err := s.db.DB().QueryRow(
		`SELECT count(*) FROM `+s.db.Schema()+`.articles WHERE base_subject = $1`,
		base).Scan(&total); err != nil {
		t.Fatalf("count total: %v", err)
	}
	if want := nParts * len(groups); total != want {
		t.Errorf("total staged rows = %d, want %d", total, want)
	}
}
