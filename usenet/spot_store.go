package usenet

// Storage for the spot index.

import (
	"context"
	"database/sql"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// spotRow is one spot as XOVER describes it, before any per-article fetch.
type spotRow struct {
	MessageID  string
	GroupName  string
	ArticleNum int64
	Poster     string
	Subject    string
	PublicKey  string
	HeaderSig  string
	Category   int
	KeyID      int
	SubCats    []string
	SizeBytes  int64
	PostedAt   time.Time
	Locale     string
}

// spotGroup is a newsgroups row with kind='spots': the same watermark state the
// crawler uses, read for the spot pass.
type spotGroup struct {
	Name          string `db:"name"`
	HighWatermark int64  `db:"high_watermark"`
	BackWatermark int64  `db:"back_watermark"`
	ServerLow     int64  `db:"server_low"`
	ServerHigh    int64  `db:"server_high"`
	BackfillDone  bool   `db:"backfill_done"`
}

// spotCounts is what the Spots tab reports.
type spotCounts struct {
	Total     int64 `db:"total"`
	Fetched   int64 `db:"fetched"`
	Verified  int64 `db:"verified"`
	WeakKey   int64 `db:"weak_key"`
	Unsigned  int64 `db:"unsigned"`
	WithNZB   int64 `db:"with_nzb"`
	Unfetched int64 `db:"unfetched"`
}

// spotChunk bounds one unnest INSERT, matching stageChunk's reasoning: this
// bounds statement size and memory, not placeholder count (the array form uses
// a fixed number of parameters regardless of row count).
const spotChunk = 1000

// upsertSpots stores a batch of spot headers.
//
// ON CONFLICT DO NOTHING rather than DO UPDATE: the header half of a spot is
// immutable once posted, and the conflict case is the backfill meeting ground
// the forward pass already covered. Updating would clobber a fetched document
// with an empty one every time the two passes overlap.
func (s *PGStore) upsertSpots(ctx context.Context, rows []spotRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	n := 0
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		for start := 0; start < len(rows); start += spotChunk {
			end := start + spotChunk
			if end > len(rows) {
				end = len(rows)
			}
			chunk := rows[start:end]

			ids := make([]string, len(chunk))
			groups := make([]string, len(chunk))
			nums := make([]int64, len(chunk))
			posters := make([]string, len(chunk))
			subjects := make([]string, len(chunk))
			keys := make([]string, len(chunk))
			sigs := make([]string, len(chunk))
			cats := make([]int64, len(chunk))
			keyIDs := make([]int64, len(chunk))
			sizes := make([]int64, len(chunk))
			posted := make([]sql.NullTime, len(chunk))
			locales := make([]string, len(chunk))
			for i, r := range chunk {
				ids[i] = r.MessageID
				groups[i] = r.GroupName
				nums[i] = r.ArticleNum
				// Wire-derived text on a UTF-8 column. Poster and subject are
				// arbitrary bytes off usenet and one bad byte fails the WHOLE
				// batched statement, taking thousands of good spots with it.
				posters[i] = pgSafeText(r.Poster)
				subjects[i] = pgSafeText(r.Subject)
				keys[i] = pgSafeText(r.PublicKey)
				sigs[i] = pgSafeText(r.HeaderSig)
				cats[i] = int64(r.Category)
				keyIDs[i] = int64(r.KeyID)
				sizes[i] = r.SizeBytes
				if !r.PostedAt.IsZero() {
					posted[i] = sql.NullTime{Time: r.PostedAt, Valid: true}
				}
				locales[i] = pgSafeText(r.Locale)
			}
			// Subcategories are a text[] per row, so they cannot ride the
			// parallel-array form; they are written in a second pass below
			// only for rows that actually landed.
			res, err := tx.ExecContext(ctx,
				`INSERT INTO spots (message_id, group_name, article_num, poster, subject,
				                    public_key, header_sig, category, key_id, size_bytes,
				                    posted_at, locale)
				 SELECT * FROM unnest($1::text[], $2::text[], $3::bigint[], $4::text[], $5::text[],
				                      $6::text[], $7::text[], $8::int[], $9::int[], $10::bigint[],
				                      $11::timestamptz[], $12::text[])
				 ON CONFLICT (message_id) DO NOTHING`,
				pgTextArray(ids), pgTextArray(groups), pq.Array(nums),
				pgTextArray(posters), pgTextArray(subjects), pgTextArray(keys), pgTextArray(sigs),
				pq.Array(cats), pq.Array(keyIDs), pq.Array(sizes),
				pq.Array(posted), pgTextArray(locales))
			if err != nil {
				return err
			}
			if c, _ := res.RowsAffected(); c > 0 {
				n += int(c)
			}
			for _, r := range chunk {
				if len(r.SubCats) == 0 {
					continue
				}
				if _, err := tx.ExecContext(ctx,
					`UPDATE spots SET subcats = $2 WHERE message_id = $1 AND subcats = '{}'`,
					r.MessageID, pgTextArray(r.SubCats)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return n, err
}

// spotGroups lists the active spot groups.
func (s *PGStore) spotGroups(ctx context.Context) ([]spotGroup, error) {
	var out []spotGroup
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out,
			`SELECT name, high_watermark, back_watermark, server_low, server_high, backfill_done
			   FROM newsgroups WHERE active = TRUE AND kind = 'spots' ORDER BY name`)
	})
	return out, err
}

// setSpotGroupExtent records what the server currently reports for a group.
//
// BOTH watermarks seed from server_high on first sight, and that is the whole
// division of labour between the two halves of the pass. Leaving high_watermark
// at 0 would make the forward pass treat the entire 5.9M-article history as
// "new", walking it from the bottom — while the backfill walked the same
// history downward at the same time. Every article read twice, the newest spots
// arriving last, and nothing about it visible except a pass that never seems to
// finish.
//
// Seeded this way, forward has nothing to do until real new articles arrive and
// backfill owns everything below, which is the intended split.
//
// server_low is never a seed: it moves UP as articles expire, so seeding a
// watermark from it would mark the whole history walked without reading any.
func (s *PGStore) setSpotGroupExtent(ctx context.Context, name string, low, high int64) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE newsgroups
			    SET server_low = $2, server_high = $3, last_crawl = now(),
			        back_watermark = CASE WHEN back_watermark = 0 THEN $3 ELSE back_watermark END,
			        high_watermark = CASE WHEN high_watermark = 0 THEN $3 ELSE high_watermark END
			  WHERE name = $1`, name, low, high)
		return err
	})
}

// advanceSpotHigh moves the forward watermark. Never backwards: a pass that
// read a short range must not rewind a watermark another pass already advanced.
func (s *PGStore) advanceSpotHigh(ctx context.Context, name string, high int64) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE newsgroups SET high_watermark = GREATEST(high_watermark, $2), last_crawl = now()
			  WHERE name = $1`, name, high)
		return err
	})
}

// lowerSpotBack moves the backfill watermark down, and marks the group done
// when it reaches what the server still holds.
func (s *PGStore) lowerSpotBack(ctx context.Context, name string, back int64, done bool) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE newsgroups
			    SET back_watermark = LEAST(NULLIF(back_watermark, 0), $2),
			        backfill_done  = backfill_done OR $3
			  WHERE name = $1`, name, back, done)
		return err
	})
}

// countSpots is the Spots tab's readout.
//
// FILTERED aggregates in one pass rather than six queries: the table reaches
// millions of rows and each separate count would be its own scan.
func (s *PGStore) countSpots(ctx context.Context) (spotCounts, error) {
	var c spotCounts
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &c,
			`SELECT count(*)                                          AS total,
			        count(*) FILTER (WHERE fetched_at IS NOT NULL)    AS fetched,
			        count(*) FILTER (WHERE trust = 'verified')        AS verified,
			        count(*) FILTER (WHERE trust = 'weak-key')        AS weak_key,
			        count(*) FILTER (WHERE trust = 'unsigned')        AS unsigned,
			        count(*) FILTER (WHERE nzb_segment <> '')         AS with_nzb,
			        count(*) FILTER (WHERE fetched_at IS NULL)        AS unfetched
			   FROM spots`)
	})
	return c, err
}

// setGroupKind marks a newsgroup as a spot index (or back to a normal group).
func (s *PGStore) setGroupKind(ctx context.Context, name, kind string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO newsgroups (name, kind, active) VALUES ($1, $2, TRUE)
			 ON CONFLICT (name) DO UPDATE SET kind = EXCLUDED.kind`, name, kind)
		return err
	})
}
