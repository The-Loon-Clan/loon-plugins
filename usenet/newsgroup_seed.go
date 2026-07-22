package usenet

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
)

const newsgroupSeedPath = "seed/newsgroups.tsv"

// seedGroup is one curated newsgroup from the shipped pack.
type seedGroup struct {
	Name  string
	Pack  string // anime | movies | tv | music | books | misc
	Notes string
}

var seedPacks = map[string]bool{
	"anime": true, "movies": true, "tv": true,
	"music": true, "books": true, "misc": true,
}

// embeddedNewsgroups returns the shipped curated groups. An empty pack file is
// legal — the mechanism ships before the data does.
func embeddedNewsgroups() ([]seedGroup, error) {
	recs, err := seedRecords(seedData, newsgroupSeedPath, 1)
	if err != nil {
		return nil, err
	}
	out := make([]seedGroup, 0, len(recs))
	for _, rec := range recs {
		g := seedGroup{Name: col(rec, 0), Pack: col(rec, 1), Notes: col(rec, 2)}
		if g.Name == "" {
			continue
		}
		if g.Pack == "" {
			g.Pack = "misc"
		}
		if !seedPacks[g.Pack] {
			return nil, fmt.Errorf("%s: group %q: unknown pack %q", newsgroupSeedPath, g.Name, g.Pack)
		}
		out = append(out, g)
	}
	return out, nil
}

// seedNewsgroups adds curated groups that aren't already present.
//
// INSERT-ONLY by design: the shipped data is just the group's existence, so
// there is nothing to update. That makes the seed incapable of changing a row
// it did not create — a group you enabled stays enabled, one you disabled stays
// disabled.
//
// One consequence to be aware of: a curated group you DELETE reappears (as
// inactive) on the next boot, because the row is gone and the seed re-inserts
// it. To keep a group out of the picker for good, disable it rather than delete
// it. Suppressing that properly would need a "dismissed" marker; it isn't worth
// a column until someone actually wants it.
//
// Seeded rows arrive inactive (the table's default): this is a curated picker
// list, not an instruction to start crawling everything on first boot.
func (s *PGStore) seedNewsgroups(ctx context.Context, groups []seedGroup) (int, error) {
	if len(groups) == 0 {
		return 0, nil
	}
	n := 0
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		for _, g := range groups {
			res, err := tx.ExecContext(ctx,
				`INSERT INTO newsgroups (name) VALUES ($1) ON CONFLICT (name) DO NOTHING`, g.Name)
			if err != nil {
				return fmt.Errorf("group %q: %w", g.Name, err)
			}
			if c, _ := res.RowsAffected(); c > 0 {
				n++
			}
		}
		return nil
	})
	return n, err
}

// seedCuratedNewsgroups loads the shipped pack into the group catalog. Failure
// is non-fatal: a missing curated list only costs the operator a nicer picker,
// it never stops crawling.
func (p *Plugin) seedCuratedNewsgroups(ctx context.Context) {
	groups, err := embeddedNewsgroups()
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/newsgroup-seed-parse", err)
		return
	}
	if len(groups) == 0 {
		return // no pack shipped yet
	}
	added, err := p.st.seedNewsgroups(ctx, groups)
	if err != nil {
		p.core.Errors.Report(ctx, "usenet/newsgroup-seed", err)
		return
	}
	if added > 0 {
		p.crawlJob.Log("curated newsgroups: added %d of %d shipped group(s) (inactive — enable what you want)",
			added, len(groups))
	}
}
