package usenet

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// Grouping observability: the instruments that answer "are we failing to
// group articles that belong together, or grouping articles that don't?"
// Everything here OBSERVES — no grouping behaviour changes until the corpus
// says a change is safe (the differential-replay discipline that vetted
// reFileOfBare against 430k titles).

// corpusSampleShift: 1 in 2^shift raw subjects is sampled into the corpus.
// At prod's ~750M overview lines/day, shift 13 (1/8192) is ~90k rows/day —
// plenty for differential replay, small enough to keep forever at the prune
// horizon. Residues sample denser (residueSampleShift, 1/32): they are the
// interesting minority, and the near-miss detector needs enough of each
// cohort to make its samples representative.
const (
	corpusSampleShift  = 13
	residueSampleShift = 5
)

// ungroupedStemCap bounds the per-pass stem counter. Stems are open-ended
// (unlike junk-rule names), so an unbounded map could balloon during a
// multi-million-article round; past the cap new stems fold into one
// "(overflow)" bucket whose size says how much the cap hid.
const ungroupedStemCap = 4096

// groupingWatch accumulates ingest-time grouping evidence in memory; the
// crawl flushes it per pass (same accumulate-drain shape as filterHits, for
// the same reason: per-article writes would cost more than the crawl).
type groupingWatch struct {
	mu      sync.Mutex
	nRaw    uint64 // raw subjects seen (drives the corpus sampling)
	nRes    uint64 // residues seen
	corpus  []corpusRow
	stems   map[string]stemVal
	stemsOv int64 // stems dropped past the cap
}

type corpusRow struct {
	group, subject string
	residue        bool
	// junkRule is the rule that discarded this subject, "" when it was kept.
	// messageID is the article the subject came from, so the body can be asked
	// what the file is really called -- a junk SUBJECT does not mean a junk
	// POSTING, and usenet obfuscation routinely scrambles one and not the
	// other. Both observe-only; nothing reads them yet.
	junkRule  string
	messageID string
}

type stemVal struct {
	count  int64
	sample string
}

func newGroupingWatch() *groupingWatch {
	return &groupingWatch{stems: make(map[string]stemVal)}
}

// noteSubject records one raw subject's parse outcome. residue means the
// parser recognised NO counter (a 1/1 singleton with no file numbering) —
// individually normal, but a large cohort of residues sharing one stem is a
// release whose counter format we cannot read. junked subjects still enter
// the corpus (differential junk testing needs them) but never the stem
// counter: digit-heavy junk normalises onto giant fake cohorts ("#####" et
// al) that would drown the real near-misses.
func (g *groupingWatch) noteSubject(group, subject string, residue, junked bool) {
	g.noteSubjectFull(group, subject, "", "", residue, junked)
}

// noteSubjectFull is noteSubject plus the evidence a drop needs to be
// second-guessed: which rule matched, and the article to read.
func (g *groupingWatch) noteSubjectFull(group, subject, junkRule, messageID string, residue, junked bool) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nRaw++
	sampled := g.nRaw&((1<<corpusSampleShift)-1) == 0
	if residue && !junked {
		g.nRes++
		if g.nRes&((1<<residueSampleShift)-1) == 0 {
			sampled = true
		}
		stem := ungroupedStem(subject)
		if stem != "" {
			if v, ok := g.stems[stem]; ok {
				v.count++
				g.stems[stem] = v
			} else if len(g.stems) < ungroupedStemCap {
				g.stems[stem] = stemVal{count: 1, sample: subject}
			} else {
				g.stemsOv++
			}
		}
	}
	if sampled && len(g.corpus) < 50000 { // hard cap: one runaway pass must not hoard memory
		g.corpus = append(g.corpus, corpusRow{
			group: group, subject: subject, residue: residue,
			junkRule: junkRule, messageID: messageID,
		})
	}
}

// drain returns and resets the accumulated state.
func (g *groupingWatch) drain() (rows []corpusRow, stems map[string]stemVal, overflow int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rows, stems, overflow = g.corpus, g.stems, g.stemsOv
	g.corpus, g.stems, g.stemsOv = nil, make(map[string]stemVal), 0
	return
}

// ungroupedStem normalises a residue subject so that members of one
// unrecognised-counter cohort collide: digit runs become '#', whitespace
// collapses, and the result is case-folded and capped. "{ 1 | 100 }" and
// "{ 2 | 100 }" share a stem; unrelated singletons do not.
func ungroupedStem(subject string) string {
	var b strings.Builder
	b.Grow(len(subject))
	lastDigit, lastSpace := false, false
	for _, r := range subject {
		switch {
		case r >= '0' && r <= '9':
			if !lastDigit {
				b.WriteByte('#')
			}
			lastDigit, lastSpace = true, false
		case r == ' ' || r == '\t':
			if !lastSpace {
				b.WriteByte(' ')
			}
			lastDigit, lastSpace = false, true
		default:
			if r >= 'A' && r <= 'Z' {
				r += 'a' - 'A'
			}
			b.WriteRune(r)
			lastDigit, lastSpace = false, false
		}
	}
	s := strings.TrimSpace(b.String())
	if len(s) > 96 {
		s = s[:96]
	}
	return s
}

// flushGroupingWatch persists one pass's evidence: corpus rows to the
// rolling table, stem counts into filter_hits under kind 'ungrouped' (which
// surfaces them on the existing Filters tab, ranked with everything else).
func (p *Plugin) flushGroupingWatch(ctx context.Context) {
	if p.grouping == nil {
		return
	}
	rows, stems, overflow := p.grouping.drain()
	if len(rows) > 0 {
		if err := p.st.insertSubjectCorpus(ctx, rows); err != nil {
			p.reportErr(ctx, "usenet/corpus-flush", err)
		}
	}
	for stem, v := range stems {
		// Only cohorts: a lone unparsed singleton is normal posting, not a
		// format we failed to read. The threshold trades noise for recall;
		// cohorts worth chasing have dozens of members.
		if v.count >= 8 {
			p.hits.noteN("ungrouped", stem, v.count, v.sample)
		}
	}
	if overflow > 0 {
		p.hits.noteN("ungrouped", "(overflow past stem cap)", overflow, "")
	}
}

// insertSubjectCorpus batch-inserts sampled subjects.
func (s *PGStore) insertSubjectCorpus(ctx context.Context, rows []corpusRow) error {
	groups := make([]string, len(rows))
	subjects := make([]string, len(rows))
	residues := make([]bool, len(rows))
	// pgTextArray, not pq.Array: these are wire-derived and a plain []string
	// bound to a text column fails the WHOLE batch on one invalid byte
	// (CLAUDE.md, "Wire-derived text needs sanitising at the BIND").
	rules := make([]string, len(rows))
	msgids := make([]string, len(rows))
	for i, r := range rows {
		// Sanitised here rather than at capture: the corpus exists to hold RAW
		// subjects for differential parser testing, so the in-memory copy the
		// parser sees must stay untouched. Postgres will not take invalid
		// UTF-8 at all, and one bad byte fails the whole batch — so the choice
		// is a sanitised sample or no sample, and a corpus row with a
		// replacement char still tells you the grammar it came from.
		groups[i], subjects[i], residues[i] = r.group, pgSafeText(r.subject), r.residue
		rules[i], msgids[i] = r.junkRule, r.messageID
	}
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO subject_corpus (group_name, subject, residue, junk_rule, message_id)
			 SELECT * FROM unnest($1::text[], $2::text[], $3::boolean[], $4::text[], $5::text[])`,
			pq.Array(groups), pq.Array(subjects), pq.Array(residues),
			pgTextArray(rules), pgTextArray(msgids))
		return err
	})
}

// junkDropsToProbe returns sampled drops whose body has never been read.
//
// junk_rule IS NOT NULL is exactly the dropped set (migration 037), so no other
// filter finds them; newest first, because how a rule behaves on TODAY's feed
// is the question, not how it behaved on 2023's.
func (s *PGStore) junkDropsToProbe(ctx context.Context, limit int) ([]junkProbeRow, error) {
	var out []junkProbeRow
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, group_name, subject, junk_rule, message_id
			  FROM subject_corpus
			 WHERE junk_rule IS NOT NULL
			   AND message_id IS NOT NULL AND message_id <> ''
			   AND probed_at IS NULL
			 ORDER BY seen_at DESC
			 LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r junkProbeRow
			if err := rows.Scan(&r.id, &r.group, &r.subject, &r.junkRule, &r.messageID); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// recordJunkProbe stores what the body called the file.
//
// probed_at is set on EVERY outcome, the empty one included, so an article with
// no yEnc header is not re-fetched forever. "probed and found nothing" and
// "not yet probed" are then told apart by probed_at, not by the name.
func (s *PGStore) recordJunkProbe(ctx context.Context, id int64, name string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// Sanitised at the bind: a yEnc filename is wire-derived bytes and one
		// invalid sequence fails the statement (CLAUDE.md).
		_, err := tx.ExecContext(ctx,
			`UPDATE subject_corpus SET recovered_name = $2, probed_at = now() WHERE id = $1`,
			id, pgSafeText(name))
		return err
	})
}

// junkDropReport is the debug list: what the rules threw away, and what the
// bodies say those postings actually were.
type junkDropReport struct {
	Sampled  int // dropped subjects sampled by the corpus
	Probed   int // of those, bodies read
	Real     int // ... whose yEnc name is NOT junk -- a release we discarded
	Junk     int // ... whose yEnc name is junk too -- the drop was right
	NoHeader int // ... with no yEnc header, or the article was gone
	Rows     []junkDropRow
}

type junkDropRow struct {
	Group     string
	Subject   string
	Rule      string
	Recovered string
	Probed    bool
	Seen      time.Time
}

// junkDropsReport reads the counts over ALL sampled drops and the newest few
// rows to show under them.
//
// The counts and the rows are deliberately different populations: a list of
// twenty tells you what the drops look like, and only the totals tell you
// whether twenty is representative. Showing rows without a denominator is how
// the grouping card was once misread as a rule list.
func (s *PGStore) junkDropsReport(ctx context.Context, limit int) (junkDropReport, error) {
	var rep junkDropReport
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// recovered_name '' means asked-and-nothing-there; NULL with a
		// probed_at means the same for a gone article. Both are "no name".
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*),
			       count(*) FILTER (WHERE probed_at IS NOT NULL),
			       count(*) FILTER (WHERE probed_at IS NOT NULL AND coalesce(recovered_name,'') = '')
			  FROM subject_corpus
			 WHERE junk_rule IS NOT NULL`).Scan(&rep.Sampled, &rep.Probed, &rep.NoHeader); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT group_name, subject, junk_rule, coalesce(recovered_name,''),
			       probed_at IS NOT NULL, seen_at
			  FROM subject_corpus
			 WHERE junk_rule IS NOT NULL
			 ORDER BY probed_at IS NULL, seen_at DESC
			 LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r junkDropRow
			if err := rows.Scan(&r.Group, &r.Subject, &r.Rule, &r.Recovered, &r.Probed, &r.Seen); err != nil {
				return err
			}
			rep.Rows = append(rep.Rows, r)
		}
		return rows.Err()
	})
	if err != nil {
		return junkDropReport{}, err
	}
	// Real-vs-junk is decided by the junk rules in Go rather than in SQL, so
	// the classification is the SAME code the crawler drops on. A SQL mirror of
	// those rules is exactly the divergence that starved the title-cleaner
	// worklist for a year (CLAUDE.md, Postgres has no ).
	named, err := s.recoveredNames(ctx)
	if err != nil {
		return rep, err
	}
	for _, n := range named {
		if whichJunkRule(n) != "" {
			rep.Junk++
		} else {
			rep.Real++
		}
	}
	return rep, nil
}

// recoveredNames returns every non-empty name a probe recovered. Bounded by the
// corpus's own retention window (diag_keep_days), which is what keeps this from
// becoming an unbounded read.
func (s *PGStore) recoveredNames(ctx context.Context) ([]string, error) {
	var out []string
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT recovered_name FROM subject_corpus
			 WHERE junk_rule IS NOT NULL AND coalesce(recovered_name,'') <> ''`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return err
			}
			out = append(out, n)
		}
		return rows.Err()
	})
	return out, err
}

// pruneSubjectCorpus trims the rolling window.
func (s *PGStore) pruneSubjectCorpus(ctx context.Context, keepDays int) (int64, error) {
	if keepDays <= 0 {
		keepDays = 14
	}
	var n int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM subject_corpus WHERE seen_at < now() - make_interval(days => $1)`, keepDays)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

// mergeSuspect reports whether one staged set's member subjects carry two or
// more DISTINCT explicit episode markers — the "Show EP1" + "Show EP2"
// false-merge signature. Deliberately conservative: only unambiguous
// E##/EP##/S##E## tokens count, because loose forms (" - 01 - ", years,
// resolutions) would flag legitimate sets. Detection only — the flag is
// counted and surfaced; nothing splits or drops on it.
func mergeSuspect(arts []stagedArticle) (bool, string) {
	seen := map[string]bool{}
	var first string
	for _, a := range arts {
		tok := episodeToken(a.Subject)
		if tok == "" {
			continue
		}
		if !seen[tok] {
			seen[tok] = true
			if first == "" {
				first = tok
			} else if len(seen) >= 2 {
				return true, first + "+" + tok
			}
		}
	}
	return false, ""
}

// episodeToken extracts an explicit episode marker (E01 / EP01 / S01E01),
// normalised (upper-cased, zero-padding stripped), or "".
func episodeToken(subject string) string {
	up := strings.ToUpper(subject)
	for i := 0; i+1 < len(up); i++ {
		if up[i] != 'E' {
			continue
		}
		// Require a word-ish boundary before E: start of string, separator
		// punctuation, or an S## season prefix (any digit width — "S01E04",
		// "S2E4" — walked back to the S, which itself needs a boundary so
		// "PES2024"-style words never qualify).
		if i > 0 {
			prev := up[i-1]
			ok := prev == ' ' || prev == '[' || prev == '(' || prev == '.' || prev == '-' || prev == '_'
			if !ok && prev >= '0' && prev <= '9' {
				j := i - 1
				for j >= 0 && up[j] >= '0' && up[j] <= '9' {
					j--
				}
				if j >= 0 && up[j] == 'S' {
					ok = j == 0 || up[j-1] == ' ' || up[j-1] == '[' || up[j-1] == '(' ||
						up[j-1] == '.' || up[j-1] == '-' || up[j-1] == '_'
				}
			}
			if !ok {
				continue
			}
		}
		j := i + 1
		if j < len(up) && up[j] == 'P' {
			j++
		}
		start := j
		for j < len(up) && up[j] >= '0' && up[j] <= '9' && j-start < 4 {
			j++
		}
		if d := j - start; d >= 2 && d <= 3 {
			// Two-or-three digit episode numbers only: "E1" is too ambiguous
			// (part of countless words), four digits is a year.
			num := strings.TrimLeft(up[start:j], "0")
			if num == "" {
				num = "0"
			}
			// A digit right after ends the token cleanly; a letter means we
			// matched inside a word (e.g. "PES2024") — reject.
			if j < len(up) && up[j] >= 'A' && up[j] <= 'Z' {
				continue
			}
			return "E" + num
		}
	}
	return ""
}
