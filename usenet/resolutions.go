package usenet

import (
	"context"
	"sync"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// Completion-distance instrumentation: every set the pipeline RESOLVES —
// built, salvaged, or judged beyond salvage — records its article span next
// to the group's watermarks at that moment. The position-based staging
// window is derived from this series (how far behind the walk head do sets
// actually resolve?), which is what keeps its eventual threshold measured
// rather than guessed. Observe-only; nothing reads it on a hot path.

// setResolution is one resolved set, pending flush.
type setResolution struct {
	group        string
	kind         string
	artLo, artHi int64
	held         int
}

// resolutionLog accumulates a round's resolutions; buildLocked flushes it.
// Bounded implicitly: a round resolves at most the draw plus the salvage cap.
type resolutionLog struct {
	mu   sync.Mutex
	rows []setResolution
}

func newResolutionLog() *resolutionLog { return &resolutionLog{} }

// note records one resolution. The span comes from the set's staged META
// (staging.setSpan) — the loaded articles do not carry their numbers, only
// the meta fold does. A set staged before span tracking records 0s and the
// analysis filters those out.
func (l *resolutionLog) note(group, kind string, held int, lo, hi int64) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.rows = append(l.rows, setResolution{group: group, kind: kind, artLo: lo, artHi: hi, held: held})
	l.mu.Unlock()
}

func (l *resolutionLog) drain() []setResolution {
	l.mu.Lock()
	defer l.mu.Unlock()
	rows := l.rows
	l.rows = nil
	return rows
}

// flushResolutions persists a round's resolutions with the groups' current
// watermarks attached. Watermarks are read once per flush per group — they
// lag each set's actual resolution by less than a round, which is noise
// against distances measured in hundreds of batch windows.
func (p *Plugin) flushResolutions(ctx context.Context) {
	if p.resolutions == nil {
		return
	}
	rows := p.resolutions.drain()
	if len(rows) == 0 {
		return
	}
	groups := map[string]bool{}
	for _, r := range rows {
		groups[r.group] = true
	}
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	marks, err := p.st.groupWatermarks(ctx, names)
	if err != nil {
		p.reportErr(ctx, "usenet/resolutions-marks", err)
		marks = nil // record with zero watermarks rather than losing the spans
	}
	if err := p.st.insertSetResolutions(ctx, rows, marks); err != nil {
		p.reportErr(ctx, "usenet/resolutions-flush", err)
	}
}

// groupMarks is one group's watermark pair.
type groupMarks struct {
	Back, High int64
}

// groupWatermarks reads the named groups' watermarks. Aggregated MIN(back) /
// MAX(high) across backbones: prod runs one backbone, and for measurement
// the conservative envelope is fine on installs that run more.
func (s *PGStore) groupWatermarks(ctx context.Context, groups []string) (map[string]groupMarks, error) {
	if len(groups) == 0 {
		return nil, nil
	}
	type row struct {
		Name string `db:"group_name"`
		Back int64  `db:"back"`
		High int64  `db:"high"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT group_name,
			        COALESCE(MIN(COALESCE(back_watermark, high_watermark, 0)), 0) AS back,
			        COALESCE(MAX(COALESCE(high_watermark, 0)), 0) AS high
			   FROM newsgroup_state
			  WHERE group_name = ANY($1)
			  GROUP BY group_name`, pq.Array(groups))
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]groupMarks, len(rows))
	for _, r := range rows {
		out[r.Name] = groupMarks{Back: r.Back, High: r.High}
	}
	return out, nil
}

// insertSetResolutions batch-inserts one flush.
func (s *PGStore) insertSetResolutions(ctx context.Context, rows []setResolution, marks map[string]groupMarks) error {
	n := len(rows)
	groups := make([]string, n)
	kinds := make([]string, n)
	los := make([]int64, n)
	his := make([]int64, n)
	helds := make([]int64, n)
	backs := make([]int64, n)
	highs := make([]int64, n)
	for i, r := range rows {
		groups[i], kinds[i] = r.group, r.kind
		los[i], his[i], helds[i] = r.artLo, r.artHi, int64(r.held)
		m := marks[r.group]
		backs[i], highs[i] = m.Back, m.High
	}
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO set_resolutions (group_name, kind, art_lo, art_hi, held, back_watermark, high_watermark)
			 SELECT * FROM unnest($1::text[], $2::text[], $3::bigint[], $4::bigint[], $5::int[], $6::bigint[], $7::bigint[])`,
			pq.Array(groups), pq.Array(kinds), pq.Array(los), pq.Array(his),
			pq.Array(helds), pq.Array(backs), pq.Array(highs))
		return err
	})
}

// pruneSetResolutions trims the series.
func (s *PGStore) pruneSetResolutions(ctx context.Context, keepDays int) (int64, error) {
	if keepDays <= 0 {
		keepDays = 14
	}
	var n int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM set_resolutions WHERE at < now() - make_interval(days => $1)`, keepDays)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}
