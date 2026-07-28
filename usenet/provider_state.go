package usenet

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/jmoiron/sqlx"
)

// Per-(backbone, group) crawl state. Every number here — watermarks, server
// bounds, coverage — is meaningful only within the backbone that produced it,
// because NNTP article numbers are assigned per backbone. Two accounts on the
// same backbone therefore SHARE this state (the second is extra connections, not
// extra coverage); two different backbones must never share it.

// crawlGroupSel is one active group's crawl-selection row: its name + this
// backbone's watermark/last-crawl plus the per-group knobs the crawler needs.
type crawlGroupSel struct {
	Name      string        `db:"name"`
	HW        int64         `db:"high_watermark"`
	LastCrawl sql.NullTime  `db:"last_crawl"`
	Retention sql.NullInt64 `db:"retention_days"`
	Throttle  int           `db:"throttle_ms"`
	TierRaw   string        `db:"tier"`
}

// orderCrawlGroups is the pure selection order for a crawl pass: CRITICAL
// groups before NORMAL before LOW, STALEST-FIRST within each tier
// (never-crawled first, then oldest last_crawl), then capped at limit
// (limit <= 0 = no cap).
// Callers pass rows in the SQL (tier, sort_order, name) order;
// SliceStable preserves it as the tiebreak among equally-stale groups, so
// sort_order still decides ties. Extracted so the ordering is unit-testable
// without a database.
func orderCrawlGroups(rows []crawlGroupSel, limit int) []crawlGroupSel {
	// One bucket per tier, indexed by tierRank so adding a tier is a change to
	// tier.go alone.
	buckets := make([][]crawlGroupSel, 3)
	for _, r := range rows {
		rank := tierRank(normalizeTier(r.TierRaw))
		buckets[rank] = append(buckets[rank], r)
	}
	stalestFirst := func(g []crawlGroupSel) {
		sort.SliceStable(g, func(i, j int) bool {
			a, b := g[i].LastCrawl, g[j].LastCrawl
			if a.Valid != b.Valid {
				return !a.Valid // never-crawled first
			}
			if a.Valid && !a.Time.Equal(b.Time) {
				return a.Time.Before(b.Time)
			}
			return false
		})
	}
	pick := make([]crawlGroupSel, 0, len(rows))
	for i := range buckets {
		stalestFirst(buckets[i])
		pick = append(pick, buckets[i]...)
	}
	// Higher tiers always ahead of lower ones, so the cap (if any) falls on the
	// low tail first and the stalest-first order rotates whoever it cuts.
	if limit > 0 && len(pick) > limit {
		pick = pick[:limit]
	}
	return pick
}

// activeGroupsForBackbone lists the groups to crawl along with THIS backbone's
// progress. A group with no state row yet reports watermark 0, which the crawler
// reads as "first pass" and caps accordingly.
//
// Ordering (the operator's rule): CRITICAL groups come before NORMAL, which come
// before LOW — "low" must never be crawled ahead of a normal group, only in
// whatever capacity is left after them, and "critical" always takes a slot. Within each tier, STALEST-FIRST (never-crawled,
// then oldest last_crawl): the group that has waited longest takes the next
// slot, so under a cap the slots ROTATE and no group starves. The old static
// "ORDER BY low_priority, sort_order LIMIT n" both (a) let low-pri jump ahead of
// normal whenever the normal tier was momentarily caught up, and (b) handed the
// same top-n every pass, starving the tail FOREVER once n or more groups were
// active. limit <= 0 means "no cap — every active group each pass" (max_groups=0).
func (s *PGStore) activeGroupsForBackbone(ctx context.Context, backbone string, limit int) ([]groupRow, error) {
	var rows []crawlGroupSel
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// All active rows (a curated catalog, not a firehose); the tier split,
		// stalest-first ordering, and cap happen in orderCrawlGroups. The ORDER
		// BY only fixes its SliceStable tiebreak (sort_order then name).
		// sqllint:allow tierOrderSQL is a compile-time constant expression, no input
		return tx.SelectContext(ctx, &rows,
			`SELECT g.name, COALESCE(st.high_watermark, 0) AS high_watermark,
			        st.last_crawl, g.retention_days, g.throttle_ms, g.tier
			   FROM newsgroups g
			   LEFT JOIN newsgroup_state st
			     ON st.group_name = g.name AND st.backbone = $1
			  WHERE g.active = TRUE
			  ORDER BY `+tierOrderSQL+`, g.sort_order, g.name`, backbone) // sqllint:allow constant tier-rank expression, no input
	})
	if err != nil {
		return nil, err
	}

	pick := orderCrawlGroups(rows, limit)
	out := make([]groupRow, len(pick))
	for i, r := range pick {
		out[i] = groupRow{
			Name: r.Name, HighWatermark: r.HW,
			RetentionDays: int(r.Retention.Int64), ThrottleMs: r.Throttle,
			Tier: normalizeTier(r.TierRaw),
		}
	}
	return out, nil
}

// updateGroupStateForBackbone records this backbone's view of a group: its
// server bounds, and (when watermark > 0) an advance. GREATEST keeps the
// watermark monotonic; back_watermark is seeded once, on the first crawl, so
// backfill knows where this provider's history begins.
func (s *PGStore) updateGroupStateForBackbone(ctx context.Context, backbone, name string, serverLow, serverHigh, watermark, backSeed int64, hwDate time.Time) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var hw sql.NullTime
		if !hwDate.IsZero() {
			hw = sql.NullTime{Time: hwDate, Valid: true}
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO newsgroup_state
			   (backbone, group_name, high_watermark, high_watermark_date,
			    back_watermark, server_low, server_high, last_crawl)
			 VALUES ($1,$2,$3,$4,$5,$6,$7, now())
			 ON CONFLICT (backbone, group_name) DO UPDATE SET
			   high_watermark      = GREATEST(newsgroup_state.high_watermark, EXCLUDED.high_watermark),
			   high_watermark_date = COALESCE(EXCLUDED.high_watermark_date, newsgroup_state.high_watermark_date),
			   back_watermark      = COALESCE(newsgroup_state.back_watermark, EXCLUDED.back_watermark),
			   server_low          = EXCLUDED.server_low,
			   server_high         = EXCLUDED.server_high,
			   last_crawl          = now()`,
			backbone, name, watermark, hw, backSeed, serverLow, serverHigh)
		return err
	})
}

// groupsNeedingBackfillForBackbone lists groups with history left below this
// backbone's back watermark.
func (s *PGStore) groupsNeedingBackfillForBackbone(ctx context.Context, backbone string, limit int) ([]backfillRow, error) {
	if limit < 0 {
		limit = 0 // 0 => LIMIT NULLIF(0,0) => no cap (max_groups=0 means "all")
	}
	type row struct {
		Name      string        `db:"name"`
		Back      int64         `db:"back_watermark"`
		Low       int64         `db:"server_low"`
		Retention sql.NullInt64 `db:"retention_days"`
		Throttle  int           `db:"throttle_ms"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// sqllint:allow tierOrderSQL is a compile-time constant expression, no input
		return tx.SelectContext(ctx, &rows,
			`SELECT g.name, st.back_watermark, st.server_low,
			        g.retention_days, g.throttle_ms
			   FROM newsgroups g
			   JOIN newsgroup_state st
			     ON st.group_name = g.name AND st.backbone = $1
			  WHERE g.active = TRUE
			    AND st.backfill_done = FALSE
			    AND st.back_watermark IS NOT NULL
			    AND st.back_watermark > st.server_low
			  ORDER BY `+tierOrderSQL+`, g.sort_order, g.name
			  LIMIT NULLIF($2, 0)`, backbone, limit)
	})
	if err != nil {
		return nil, err
	}
	out := make([]backfillRow, len(rows))
	for i, r := range rows {
		out[i] = backfillRow{
			Name: r.Name, BackWatermark: r.Back, ServerLow: r.Low,
			RetentionDays: int(r.Retention.Int64), ThrottleMs: r.Throttle,
		}
	}
	return out, nil
}

func (s *PGStore) updateBackWatermarkForBackbone(ctx context.Context, backbone, name string, back int64, oldest time.Time) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var od sql.NullTime
		if !oldest.IsZero() {
			od = sql.NullTime{Time: oldest, Valid: true}
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE newsgroup_state
			    SET back_watermark = $3,
			        back_watermark_date = COALESCE($4, back_watermark_date)
			  WHERE backbone = $1 AND group_name = $2`, backbone, name, back, od)
		return err
	})
}

// anyBackfillPending reports whether ANY backbone still has history to pull for
// an active group. This is what "idle" means for the health job: checking per
// backbone matters because the legacy per-group columns on newsgroups are dead
// (nothing writes them since the state moved to newsgroup_state) — the old
// query against them made a fresh install look permanently caught-up and an
// upgraded one permanently busy.
func (s *PGStore) anyBackfillPending(ctx context.Context) (bool, error) {
	var pending bool
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &pending,
			`SELECT EXISTS (
			    SELECT 1 FROM newsgroup_state st
			      JOIN newsgroups g ON g.name = st.group_name AND g.active = TRUE
			     WHERE st.backfill_done = FALSE
			       AND st.back_watermark IS NOT NULL
			       AND st.back_watermark > st.server_low)`)
	})
	return pending, err
}

func (s *PGStore) markBackfillDoneForBackbone(ctx context.Context, backbone, name string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE newsgroup_state SET backfill_done = TRUE
			  WHERE backbone = $1 AND group_name = $2`, backbone, name)
		return err
	})
}

// recordFetchedRangeFor marks a span covered for ONE BACKBONE. Coverage cannot
// be global: another backbone's fetched ranges would mark these gaps as done and
// the crawler would skip content it never fetched.
func (s *PGStore) recordFetchedRangeFor(ctx context.Context, backbone, group string, start, end int64) error {
	if start > end {
		start, end = end, start
	}
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`WITH absorbed AS (
			     DELETE FROM newsgroup_ranges
			      WHERE backbone = $1 AND group_name = $2
			        -- $3/$4 are article numbers (int64). Their FIRST use in this
			        -- statement is inside "+ 1"/"- 1" arithmetic, and an untyped
			        -- param next to an integer literal is inferred int4 — so without
			        -- the casts, any group whose article numbers exceed 2^31 (every
			        -- large binaries group on a major backbone) fails here with a
			        -- 22003 range error on EVERY batch: coverage is never recorded
			        -- and backfill refetches the same spans forever. Same trap as
			        -- the size_bytes bug; reproduced against PG 13 before fixing.
			        AND range_start <= $4::bigint + 1
			        AND range_end   >= $3::bigint - 1
			  RETURNING range_start, range_end
			 )
			 INSERT INTO newsgroup_ranges (backbone, group_name, range_start, range_end)
			 SELECT $1, $2,
			        LEAST($3, COALESCE(MIN(range_start), $3)),
			        GREATEST($4, COALESCE(MAX(range_end), $4))
			   FROM absorbed`,
			backbone, group, start, end)
		return err
	})
}

// coveredRangesFor returns this backbone's merged runs for a group, ascending.
func (s *PGStore) coveredRangesFor(ctx context.Context, backbone, group string) ([]articleRange, error) {
	var rows []articleRange
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT range_start AS start, range_end AS end
			   FROM newsgroup_ranges
			  WHERE backbone = $1 AND group_name = $2
			  ORDER BY range_start`, backbone, group)
	})
	return rows, err
}

// backfillGapsFor returns this backbone's uncovered spans, newest first.
func (s *PGStore) backfillGapsFor(ctx context.Context, backbone, group string, low, high int64) ([]articleRange, error) {
	var rows []articleRange
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT range_start AS start, range_end AS end
			   FROM newsgroup_ranges
			  WHERE backbone = $1 AND group_name = $2
			  ORDER BY range_start`, backbone, group)
	})
	if err != nil {
		return nil, err
	}
	return gapsBetween(rows, low, high), nil
}

// resetBackfillForGroup re-arms backfill on EVERY backbone for a group and drops
// the fetched-range record, which is what "re-scan history" has to mean: gaps are
// the complement of the recorded ranges, so leaving them behind would compute an
// empty gap list and mark the group complete again on the very next pass.
//
// Dropping the ranges is safe — re-fetched articles collide on message-id and
// are ignored — and it is the only way an operator can recover from ranges that
// were recorded against content that was never really staged.
func (s *PGStore) resetBackfillForGroup(ctx context.Context, group string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE newsgroup_state
			    SET back_watermark = GREATEST(high_watermark - 1, server_low),
			        back_watermark_date = NULL, backfill_done = FALSE
			  WHERE group_name = $1`, group); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM newsgroup_ranges WHERE group_name = $1`, group)
		return err
	})
}

// allCoveredRanges returns every backbone's merged runs keyed by coverKey. One query for the whole page: the crawlers view needs
// coverage for every group it renders, and a query per row is a needless N+1 on
// a page that already refreshes on a timer while a crawl is running.
func (s *PGStore) allCoveredRanges(ctx context.Context) (map[coverKey][]articleRange, error) {
	type row struct {
		Backbone string `db:"backbone"`
		Group    string `db:"group_name"`
		Start    int64  `db:"start"`
		End      int64  `db:"end"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT r.backbone, r.group_name, r.range_start AS start, r.range_end AS end
			   FROM newsgroup_ranges r
			   JOIN newsgroups g ON g.name = r.group_name AND g.active = TRUE
			  ORDER BY r.backbone, r.group_name, r.range_start`)
	})
	if err != nil {
		return nil, err
	}
	out := make(map[coverKey][]articleRange)
	for _, r := range rows {
		k := coverKey{r.Backbone, r.Group}
		out[k] = append(out[k], articleRange{Start: r.Start, End: r.End})
	}
	return out, nil
}
