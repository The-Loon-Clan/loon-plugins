package backup

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"
)

// inventoryStore is the seam the index pass writes through.
type inventoryStore interface {
	startGeneration(ctx context.Context) (int64, error)
	sealGeneration(ctx context.Context, gen, files, bytes, hashed int64) error
	failGeneration(ctx context.Context, gen int64, reason string) error

	// currentStats is the stat gate's input: for every path, the identity and
	// content hash last recorded. Loaded once per pass — a per-file query
	// against 417k files would dominate the run.
	currentStats(ctx context.Context) (map[string]knownFile, error)

	upsertFiles(ctx context.Context, gen int64, rows []fileRow) error
	recordClassTotal(ctx context.Context, gen int64, class string, files, bytes int64) error
	noteSuspect(ctx context.Context, path, class, reason, detail string) error

	// classTotals returns a sealed generation's per-class counts, for the
	// shrink comparison.
	// clearSuspect removes a file's suspect row once it passes. Without it the
	// table is append-only: a file that healed — or that a corrected detector
	// no longer objects to — stays flagged forever, so the count always
	// reflects the worst historical moment rather than the current truth.
	clearSuspect(ctx context.Context, path string) error
	// suspectPaths lists what is currently flagged, so each pass can re-verify
	// them regardless of their stat and the table converges on reality.
	suspectPaths(ctx context.Context) ([]string, error)

	classTotals(ctx context.Context, gen int64) (map[string]classTotal, error)
	// lastSealedGeneration returns the newest generation that completed, or 0.
	lastSealedGeneration(ctx context.Context) (int64, error)

	// filesForGen lists one class's current files in a sealed generation, which
	// is what the packs are planned from.
	filesForGen(ctx context.Context, gen int64, class string) ([]fileRow, error)
	generationMeta(ctx context.Context, gen int64) (genMeta, error)
}

// genMeta is a sealed generation's headline numbers, for the manifest.
type genMeta struct {
	SealedAt string
	Files    int64
	Bytes    int64
}

// knownFile is one path's last recorded identity.
type knownFile struct {
	key statKey
	sha string
}

// PGStore is the inventory in the plugin's own schema.
//
// Every statement goes through SchemaDB.WithTx, which scopes search_path to the
// plugin schema — so the unqualified table names below resolve to backup.files
// rather than to anything the host happens to have named files.
type PGStore struct{ db *core.SchemaDB }

// NewPGStore wraps the schema-scoped handle from core.Storage.SchemaDB.
func NewPGStore(db *core.SchemaDB) *PGStore { return &PGStore{db: db} }

var _ inventoryStore = (*PGStore)(nil)

func (s *PGStore) startGeneration(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.QueryRowContext(ctx,
			`INSERT INTO generations DEFAULT VALUES RETURNING id`).Scan(&id)
	})
	return id, err
}

// sealGeneration marks a pass complete. Only a sealed generation may be served
// or compared against — an unsealed one is a partial walk, and a partial walk
// is indistinguishable from a corpus that shrank.
func (s *PGStore) sealGeneration(ctx context.Context, gen, files, bytes, hashed int64) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE generations
			    SET sealed_at = now(), files = $2::bigint, bytes = $3::bigint, hashed = $4::bigint
			  WHERE id = $1`, gen, files, bytes, hashed)
		return err
	})
}

func (s *PGStore) failGeneration(ctx context.Context, gen int64, reason string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE generations SET error = $2 WHERE id = $1`, gen, reason)
		return err
	})
}

func (s *PGStore) currentStats(ctx context.Context) (map[string]knownFile, error) {
	// DISTINCT ON keeps the newest row per path: a file edited in place has one
	// path with several (path, sha256) rows, and only the latest is current.
	out := make(map[string]knownFile, 1<<19)
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT DISTINCT ON (path) path, size_bytes, mtime_ns, ctime_ns, inode, sha256
			   FROM files
			  ORDER BY path, last_gen DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p, sha string
			var k statKey
			if err := rows.Scan(&p, &k.Size, &k.MtimeNS, &k.CtimeNS, &k.Inode, &sha); err != nil {
				return err
			}
			out[p] = knownFile{key: k, sha: sha}
		}
		return rows.Err()
	})
	return out, err
}

// upsertFiles records a batch.
//
// One multi-row statement rather than a statement per file: at 417k files the
// per-round-trip cost is the whole run. ON CONFLICT advances last_gen so an
// unchanged file is carried forward without rewriting its hash or its
// hashed_at, which is what lets the rolling re-hash mean something.
func (s *PGStore) upsertFiles(ctx context.Context, gen int64, rows []fileRow) error {
	if len(rows) == 0 {
		return nil
	}
	var (
		sb   strings.Builder
		args []any
	)
	now := time.Now()
	sb.WriteString(`INSERT INTO files
		(path, class, sha256, size_bytes, mtime_ns, ctime_ns, inode, first_gen, last_gen, hashed_at)
		VALUES `)
	for i, r := range rows {
		if i > 0 {
			sb.WriteByte(',')
		}
		n := i * 10
		fmt.Fprintf(&sb, "($%d,$%d,$%d,$%d::bigint,$%d::bigint,$%d::bigint,$%d::bigint,$%d::bigint,$%d::bigint,$%d)",
			n+1, n+2, n+3, n+4, n+5, n+6, n+7, n+8, n+9, n+10)
		// A carried-forward row keeps its old hashed_at; only a genuine re-read
		// advances it.
		verified := time.Time{}
		if r.Rehashed {
			verified = now
		}
		args = append(args, r.Path, r.Class, r.SHA256, r.Size, r.MtimeNS, r.CtimeNS, r.Inode, gen, gen, verified)
	}
	sb.WriteString(`
		ON CONFLICT (path, sha256) DO UPDATE
		   SET last_gen   = EXCLUDED.last_gen,
		       mtime_ns   = EXCLUDED.mtime_ns,
		       ctime_ns   = EXCLUDED.ctime_ns,
		       inode      = EXCLUDED.inode,
		       size_bytes = EXCLUDED.size_bytes,
		       -- Only when the content was actually re-READ this pass. A row
		       -- carried forward on an unchanged stat has verified nothing, so
		       -- advancing hashed_at for it would turn "last verified" into
		       -- "last seen" and quietly retire the rolling re-hash's whole
		       -- purpose. The caller passes rehashed for exactly this.
		       hashed_at  = CASE WHEN EXCLUDED.hashed_at > files.hashed_at
		                         THEN EXCLUDED.hashed_at ELSE files.hashed_at END`)
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// sqllint:allow placeholders generated positionally; every value is a $N argument
		_, err := tx.ExecContext(ctx, sb.String(), args...)
		return err
	})
}

func (s *PGStore) recordClassTotal(ctx context.Context, gen int64, class string, files, bytes int64) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO class_stats (gen, class, files, bytes)
			 VALUES ($1, $2, $3::bigint, $4::bigint)
			 ON CONFLICT (gen, class) DO UPDATE
			    SET files = EXCLUDED.files, bytes = EXCLUDED.bytes`,
			gen, class, files, bytes)
		return err
	})
}

func (s *PGStore) noteSuspect(ctx context.Context, path, class, reason, detail string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO suspect (path, class, reason, detail)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (path) DO UPDATE
			    SET reason = EXCLUDED.reason, detail = EXCLUDED.detail,
			        last_seen = now(), seen_count = suspect.seen_count + 1`,
			path, class, reason, detail)
		return err
	})
}

func (s *PGStore) classTotals(ctx context.Context, gen int64) (map[string]classTotal, error) {
	out := map[string]classTotal{}
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT class, files, bytes FROM class_stats WHERE gen = $1`, gen)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c string
			var t classTotal
			if err := rows.Scan(&c, &t.Files, &t.Bytes); err != nil {
				return err
			}
			out[c] = t
		}
		return rows.Err()
	})
	return out, err
}

func (s *PGStore) lastSealedGeneration(ctx context.Context) (int64, error) {
	var id sql.NullInt64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT max(id) FROM generations WHERE sealed_at IS NOT NULL`).Scan(&id)
	})
	if err != nil {
		return 0, err
	}
	return id.Int64, nil
}

func (s *PGStore) clearSuspect(ctx context.Context, path string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM suspect WHERE path = $1`, path)
		return err
	})
}

func (s *PGStore) suspectPaths(ctx context.Context) ([]string, error) {
	var out []string
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `SELECT path FROM suspect`)
	})
	return out, err
}

// filesForGen returns one class's files as they stood at a sealed generation.
//
// The predicate is an interval test, not equality, and the difference is a real
// bug rather than a refinement. `last_gen = $1` looks right — the pass stamps
// every file it saw — but the NEXT pass re-stamps those same rows as it walks,
// one class at a time. A manifest requested while pass N+1 is running would
// therefore lose exactly the classes N+1 had already reached: the numbers in
// the header still read 418k because they come from generations.files, while
// the pack list silently omits every avatar, mascot and cover. Worse, that
// truncated result would then be cached as the answer for generation N.
//
// first_gen <= gen <= last_gen asks the question actually meant: which row was
// current at that generation. A superseded revision is excluded because its
// last_gen froze when its content changed; the replacement is excluded from
// older generations because its first_gen is newer; and a file deleted from
// disk after the generation is still included, which is correct — it was there.
func (s *PGStore) filesForGen(ctx context.Context, gen int64, class string) ([]fileRow, error) {
	var out []fileRow
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT path, sha256, size_bytes
			   FROM files
			  WHERE class = $2
			    AND first_gen <= $1::bigint
			    AND last_gen  >= $1::bigint
			  ORDER BY path`, gen, class)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r := fileRow{Class: class}
			if err := rows.Scan(&r.Path, &r.SHA256, &r.Size); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

func (s *PGStore) generationMeta(ctx context.Context, gen int64) (genMeta, error) {
	var m genMeta
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT coalesce(to_char(sealed_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), ''),
			        coalesce(files,0), coalesce(bytes,0)
			   FROM generations WHERE id = $1`, gen).Scan(&m.SealedAt, &m.Files, &m.Bytes)
	})
	return m, err
}
