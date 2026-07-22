package usenet

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/nntp"
)

// stagedArticle is one parsed overview line awaiting assembly.
type stagedArticle struct {
	MessageID   string
	Subject     string
	BaseSubject string
	Poster      string
	Bytes       int64
	Posted      time.Time
	Group       string
	PartNum     int
	TotalParts  int
	SegTotal    int
	FileNum     int
	TotalFiles  int
	FileParts   bool
}

// runCrawl fetches recent overviews from each active group into staging, then
// chains the builder. Forward-only from each group's high watermark; the first
// pass is capped at MaxArticlesPerGroup and filtered to the retention window.
func (p *Plugin) runCrawl(ctx context.Context) {
	if ctx == nil {
		return
	}
	if !p.crawlMu.TryLock() {
		p.crawlJob.Log("crawl already running — skipping overlap")
		return
	}
	defer p.crawlMu.Unlock()
	p.crawlJob.SetRunning()
	cfg := p.effective(ctx)

	srv, ok, err := p.st.getServer(ctx)
	if err != nil {
		p.crawlJob.SetError(err.Error())
		p.core.Errors.Report(ctx, "usenet/crawl-server", err)
		return
	}
	if !ok || srv.Host == "" {
		p.crawlJob.Log("no server configured — add one in the admin wizard")
		p.crawlJob.SetIdle(p.nextCrawl())
		return
	}
	groups, err := p.st.activeGroups(ctx, cfg.MaxGroups)
	if err != nil {
		p.crawlJob.SetError(err.Error())
		p.core.Errors.Report(ctx, "usenet/crawl-groups", err)
		return
	}
	if len(groups) == 0 {
		p.crawlJob.Log("no active groups — pick some in the admin wizard")
		p.crawlJob.SetIdle(p.nextCrawl())
		return
	}

	conn, err := dialServer(srv)
	if err != nil {
		p.crawlJob.SetError(err.Error())
		p.core.Errors.Report(ctx, "usenet/crawl-dial", err)
		return
	}
	defer conn.Quit()

	cutoff := time.Now().AddDate(0, 0, -cfg.RetentionDays)
	staged := 0
	for _, g := range groups {
		if ctx.Err() != nil {
			return
		}
		p.crawlJob.Log("crawling %s…", g.Name)
		n, err := p.crawlGroup(ctx, conn, g, cutoff, cfg)
		if err != nil {
			p.core.Errors.Report(ctx, "usenet/crawl", fmt.Errorf("%s: %w", g.Name, err))
			p.crawlJob.Log("%s: error — %v", g.Name, err)
			continue
		}
		staged += n
		p.crawlJob.Log("%s: staged %d new article(s)", g.Name, n)
	}
	p.crawlJob.Log("crawl complete: %d group(s), %d new articles staged", len(groups), staged)
	p.crawlJob.SetIdle(p.nextCrawl())
	go p.runBuild(ctx) // assemble what just landed
}

func (p *Plugin) nextCrawl() time.Time {
	return time.Now().Add(time.Duration(p.cfg.CrawlIntervalMin) * time.Minute)
}

// crawlGroup pulls new overviews for one group into staging and advances its
// watermark. Re-selects the group before each Overview (the connection is
// stateful and shared across groups within the run).
func (p *Plugin) crawlGroup(ctx context.Context, conn *nntp.Conn, g groupRow, cutoff time.Time, cfg Config) (int, error) {
	_, low, high, err := conn.Group(g.Name)
	if err != nil {
		return 0, err
	}
	start := int(g.HighWatermark) + 1
	if g.HighWatermark == 0 {
		start = high - cfg.MaxArticlesPerGroup + 1 // first pass: cap the volume
	}
	if start < low {
		start = low
	}

	batch := cfg.Batch
	staged, scanned, batchNum := 0, 0, 0
	var maxDate time.Time
	for i := start; i <= high; i += batch {
		if ctx.Err() != nil {
			break
		}
		end := i + batch - 1
		if end > high {
			end = high
		}
		if _, _, _, err := conn.Group(g.Name); err != nil {
			return staged, err
		}
		ovs, _, err := conn.Overview(i, end)
		if err != nil {
			return staged, err
		}
		scanned += len(ovs)
		if d := newestDate(ovs); d.After(maxDate) {
			maxDate = d
		}
		arts := parseOverviews(ovs, g.Name, cutoff)
		if len(arts) > 0 {
			n, err := p.staging.stageArticles(ctx, arts)
			if err != nil {
				return staged, err
			}
			staged += n
		}
		if batchNum++; batchNum%5 == 0 {
			p.crawlJob.Log("%s: scanned %d, staged %d (article %d of %d)", g.Name, scanned, staged, end, high)
		}
	}
	// start is the bottom of this run's forward window; it seeds back_watermark on
	// the first crawl so backfill knows where history begins below it.
	if err := p.st.updateGroupState(ctx, g.Name, int64(low), int64(high), int64(start), maxDate); err != nil {
		return staged, err
	}
	return staged, nil
}

// parseOverviews turns overview lines into staged articles, dropping ones with
// no message-id and ones posted before the retention cutoff.
func parseOverviews(ovs []nntp.MessageOverview, group string, cutoff time.Time) []stagedArticle {
	out := make([]stagedArticle, 0, len(ovs))
	for _, ov := range ovs {
		if ov.MessageId == "" {
			continue
		}
		if !ov.Date.IsZero() && ov.Date.Before(cutoff) {
			continue
		}
		base, pn, tp, seg, fn, tf, fp := parseSubject(ov.Subject)
		if isJunkTitle(base) {
			continue // obfuscated random-token post — never index it
		}
		out = append(out, stagedArticle{
			MessageID: ov.MessageId, Subject: ov.Subject, BaseSubject: base,
			Poster: ov.From, Bytes: int64(ov.Bytes), Posted: ov.Date, Group: group,
			PartNum: pn, TotalParts: tp, SegTotal: seg, FileNum: fn, TotalFiles: tf, FileParts: fp,
		})
	}
	return out
}

// ── store methods for crawling ──────────────────────────────────────

type groupRow struct {
	Name          string
	HighWatermark int64
}

func (s *PGStore) activeGroups(ctx context.Context, limit int) ([]groupRow, error) {
	if limit <= 0 {
		limit = 20
	}
	type row struct {
		Name string `db:"name"`
		HW   int64  `db:"high_watermark"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT name, high_watermark FROM newsgroups WHERE active = TRUE ORDER BY name LIMIT $1`, limit)
	})
	if err != nil {
		return nil, err
	}
	out := make([]groupRow, len(rows))
	for i, r := range rows {
		out[i] = groupRow{Name: r.Name, HighWatermark: r.HW}
	}
	return out, nil
}

func (s *PGStore) stageArticles(ctx context.Context, arts []stagedArticle) (int, error) {
	n := 0
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		for _, a := range arts {
			var posted sql.NullTime
			if !a.Posted.IsZero() {
				posted = sql.NullTime{Time: a.Posted, Valid: true}
			}
			res, err := tx.ExecContext(ctx,
				`INSERT INTO articles
				   (message_id, subject, base_subject, poster, bytes, posted, group_name,
				    part_num, total_parts, seg_total, file_num, total_files, file_parts)
				 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
				 ON CONFLICT (message_id) DO NOTHING`,
				a.MessageID, a.Subject, a.BaseSubject, a.Poster, a.Bytes, posted, a.Group,
				a.PartNum, a.TotalParts, a.SegTotal, a.FileNum, a.TotalFiles, a.FileParts)
			if err != nil {
				return err
			}
			if c, _ := res.RowsAffected(); c > 0 {
				n++
			}
		}
		return nil
	})
	return n, err
}

func (s *PGStore) updateGroupState(ctx context.Context, name string, low, high, start int64, hwDate time.Time) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var hw sql.NullTime
		if !hwDate.IsZero() {
			hw = sql.NullTime{Time: hwDate, Valid: true}
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE newsgroups
			   SET high_watermark = GREATEST(high_watermark, $2),
			       server_low = $3, server_high = $4, last_crawl = now(),
			       back_watermark = COALESCE(back_watermark, $5),
			       high_watermark_date = COALESCE($6, high_watermark_date)
			 WHERE name = $1`, name, high, low, high, start, hw)
		return err
	})
}

// newestDate / oldestDate scan an overview batch for its date bounds (used to
// stamp watermarks and to detect the retention horizon during backfill).
func newestDate(ovs []nntp.MessageOverview) time.Time {
	var t time.Time
	for _, ov := range ovs {
		if ov.Date.After(t) {
			t = ov.Date
		}
	}
	return t
}

func oldestDate(ovs []nntp.MessageOverview) time.Time {
	var t time.Time
	for _, ov := range ovs {
		if ov.Date.IsZero() {
			continue
		}
		if t.IsZero() || ov.Date.Before(t) {
			t = ov.Date
		}
	}
	return t
}
