package usenet

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/xml"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// runBuild assembles complete (group, base_subject) sets into NZB files. A set
// is complete when its distinct part count reaches the max total-parts seen.
func (p *Plugin) runBuild(ctx context.Context) {
	if ctx == nil {
		return
	}
	if !p.buildMu.TryLock() {
		p.buildJob.Log("build already running — skipping overlap")
		return
	}
	defer p.buildMu.Unlock()
	// The builder drains shared staging, so it must run once cluster-wide.
	if !p.withLease(ctx, leaseScopeJob, "NZB Builder", p.leaseTTL(p.effective(ctx)), func() {
		p.buildLocked(ctx)
	}) {
		p.buildJob.Log("build skipped — another worker holds this job")
		p.buildJob.SetIdle(p.nextCrawl())
	}
}

func (p *Plugin) buildLocked(ctx context.Context) {
	p.buildJob.SetRunning()

	// Pick up admin edits, and make sure last pass's counters are persisted even
	// if that pass died before its own flush.
	p.reloadBlacklist(ctx)
	defer p.flushFilterHits(ctx)

	keys, err := p.staging.candidateGroups(ctx, 500)
	if err != nil {
		p.buildJob.SetError(err.Error())
		p.core.Errors.Report(ctx, "usenet/build-scan", err)
		return
	}
	built := 0
	for _, k := range keys {
		if ctx.Err() != nil {
			break
		}
		arts, err := p.staging.groupArticles(ctx, k.Group, k.Base)
		if err != nil {
			p.core.Errors.Report(ctx, "usenet/build-load", err)
			continue
		}
		if len(arts) == 0 || !isComplete(arts) {
			continue // not actually complete yet — leave staged for next round
		}
		if rule := whichJunkRule(k.Base); rule != "" {
			p.hits.note("junk", rule, k.Base)
			_ = p.staging.deleteStaged(ctx, k.Group, k.Base) // drop, don't build
			continue
		}
		// Operator policy, checked at build like prod does. Deliberately NOT at
		// ingest: blacklist rules are edited far more often than junk rules, and
		// filtering at build means a new rule applies to everything already
		// staged instead of only to what arrives after it.
		if pat := whichBlacklistRule(release{
			Subject: k.Base, Title: strings.TrimSpace(k.Base),
			Poster: firstPoster(arts), Group: k.Group,
		}); pat != "" {
			p.hits.note("blacklist", pat, k.Base)
			p.buildJob.Log("SKIP blacklisted (%s): %q", pat, k.Base)
			_ = p.staging.deleteStaged(ctx, k.Group, k.Base)
			continue
		}
		gz, err := gzipBytes(buildNZB(arts))
		if err != nil {
			p.core.Errors.Report(ctx, "usenet/build-gzip", err)
			continue
		}
		title := strings.TrimSpace(k.Base)
		if title == "" {
			title = "release"
		}
		size, posted := summarize(arts)
		ins, err := p.st.insertNzb(ctx, nzbRow{
			Title: title, Filename: safeFilename(title) + ".nzb",
			Size: size, Group: k.Group, ContentHash: hashKey(k.Group, k.Base),
			Posted: posted, Data: gz, Tags: parseTags(title),
			CategoryID: p.categoryFor(k.Group, title),
		})
		if err != nil {
			p.core.Errors.Report(ctx, "usenet/build-insert", err)
			continue
		}
		_ = p.staging.deleteStaged(ctx, k.Group, k.Base)
		if ins {
			built++
		}
	}
	p.buildJob.Log("built %d NZB file(s) from %d candidate group(s)", built, len(keys))
	if built > 0 {
		// New releases changed the search surface — publish so a subscriber
		// (e.g. a cache invalidator in the worker) can react. Best-effort: no
		// host event bus => no-op.
		pluginapi.EmitEvent(p.core, ctx, pluginapi.EventIngested, built)
	}
	p.buildJob.SetIdle(p.nextCrawl())
}

// isComplete decides whether a staged (group, base_subject) set is ready to
// assemble. Multi-file releases (file_parts) are complete when every file has
// all its segments and the release has all its files; single-file releases when
// the distinct part count reaches total_parts.
func isComplete(arts []stagedArticle) bool {
	multi := false
	totalFiles := 0
	for _, a := range arts {
		if a.FileParts && a.TotalFiles > 0 {
			multi = true
			if a.TotalFiles > totalFiles {
				totalFiles = a.TotalFiles
			}
		}
	}
	if multi {
		type fileState struct {
			parts    map[int]bool
			segTotal int
		}
		files := map[int]*fileState{}
		for _, a := range arts {
			f := files[a.FileNum]
			if f == nil {
				f = &fileState{parts: map[int]bool{}}
				files[a.FileNum] = f
			}
			f.parts[a.PartNum] = true
			if a.SegTotal > f.segTotal {
				f.segTotal = a.SegTotal
			}
		}
		complete := 0
		for _, f := range files {
			if f.segTotal > 0 && len(f.parts) >= f.segTotal {
				complete++
			}
		}
		return complete >= totalFiles
	}
	parts := map[int]bool{}
	total := 0
	for _, a := range arts {
		parts[a.PartNum] = true
		if a.TotalParts > total {
			total = a.TotalParts
		}
	}
	return total > 0 && len(parts) >= total
}

// buildNZB serializes a complete set into NZB XML. A multi-file release gets one
// <file> element per file number; a single-file release gets one <file>.
func buildNZB(arts []stagedArticle) []byte {
	multi := false
	for _, a := range arts {
		if a.FileParts {
			multi = true
			break
		}
	}
	doc := nzbDoc{Xmlns: "http://www.newzbin.com/DTD/2003/nzb"}
	if multi {
		byFile := map[int][]stagedArticle{}
		order := []int{}
		for _, a := range arts {
			if _, ok := byFile[a.FileNum]; !ok {
				order = append(order, a.FileNum)
			}
			byFile[a.FileNum] = append(byFile[a.FileNum], a)
		}
		sort.Ints(order)
		for _, fn := range order {
			doc.Files = append(doc.Files, makeFile(byFile[fn]))
		}
	} else {
		doc.Files = []nzbFile{makeFile(arts)}
	}
	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil
	}
	return append([]byte(xml.Header), out...)
}

// makeFile builds one <file> from the articles of a single file, segments
// ordered as loaded and de-duped by part number.
func makeFile(arts []stagedArticle) nzbFile {
	first := arts[0]
	f := nzbFile{
		Poster:  first.Poster,
		Date:    first.Posted.Unix(),
		Subject: first.Subject,
		Groups:  nzbGroups{Group: []string{first.Group}},
	}
	seen := make(map[int]bool, len(arts))
	for _, a := range arts {
		if seen[a.PartNum] {
			continue
		}
		seen[a.PartNum] = true
		f.Segments.Segment = append(f.Segments.Segment, nzbSegment{
			Bytes: a.Bytes, Number: a.PartNum, Value: strings.Trim(a.MessageID, "<>"),
		})
	}
	return f
}

type nzbDoc struct {
	XMLName xml.Name  `xml:"nzb"`
	Xmlns   string    `xml:"xmlns,attr"`
	Files   []nzbFile `xml:"file"`
}

type nzbFile struct {
	Poster   string      `xml:"poster,attr"`
	Date     int64       `xml:"date,attr"`
	Subject  string      `xml:"subject,attr"`
	Groups   nzbGroups   `xml:"groups"`
	Segments nzbSegments `xml:"segments"`
}

type nzbGroups struct {
	Group []string `xml:"group"`
}

type nzbSegments struct {
	Segment []nzbSegment `xml:"segment"`
}

type nzbSegment struct {
	Bytes  int64  `xml:"bytes,attr"`
	Number int    `xml:"number,attr"`
	Value  string `xml:",chardata"`
}

func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gunzipBytes(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func summarize(arts []stagedArticle) (size int64, posted time.Time) {
	for _, a := range arts {
		size += a.Bytes
		if !a.Posted.IsZero() && (posted.IsZero() || a.Posted.Before(posted)) {
			posted = a.Posted
		}
	}
	return size, posted
}

func hashKey(group, base string) string {
	sum := sha256.Sum256([]byte(group + "|" + base))
	return hex.EncodeToString(sum[:])
}

func safeFilename(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', '\n', '\r', '\t':
			return '_'
		}
		return r
	}, s)
	if len(s) > 180 {
		s = s[:180]
	}
	return strings.TrimSpace(s)
}

// ── store methods for assembly ──────────────────────────────────────

type groupKey struct {
	Group string
	Base  string
}

// candidateGroups pre-filters likely-complete releases in SQL: single-file when
// distinct parts reach total_parts, multi-file when all file numbers are
// present. runBuild re-verifies each with isComplete (which checks per-file
// segment counts the SQL can't cheaply express).
func (s *PGStore) candidateGroups(ctx context.Context, limit int) ([]groupKey, error) {
	type row struct {
		Group string `db:"group_name"`
		Base  string `db:"base_subject"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT group_name, base_subject FROM articles
			 GROUP BY group_name, base_subject
			 HAVING (bool_or(file_parts) = FALSE AND COUNT(DISTINCT part_num) >= MAX(total_parts))
			     OR (bool_or(file_parts) = TRUE  AND COUNT(DISTINCT file_num) >= MAX(total_files))
			 LIMIT $1`, limit)
	})
	if err != nil {
		return nil, err
	}
	out := make([]groupKey, len(rows))
	for i, r := range rows {
		out[i] = groupKey{Group: r.Group, Base: r.Base}
	}
	return out, nil
}

func (s *PGStore) groupArticles(ctx context.Context, group, base string) ([]stagedArticle, error) {
	type row struct {
		MessageID  string       `db:"message_id"`
		Subject    string       `db:"subject"`
		Poster     string       `db:"poster"`
		Bytes      int64        `db:"bytes"`
		Posted     sql.NullTime `db:"posted"`
		Group      string       `db:"group_name"`
		PartNum    int          `db:"part_num"`
		TotalParts int          `db:"total_parts"`
		SegTotal   int          `db:"seg_total"`
		FileNum    int          `db:"file_num"`
		TotalFiles int          `db:"total_files"`
		FileParts  bool         `db:"file_parts"`
	}
	var rows []row
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows,
			`SELECT message_id, subject, poster, bytes, posted, group_name, part_num,
			        total_parts, seg_total, file_num, total_files, file_parts
			 FROM articles WHERE group_name = $1 AND base_subject = $2
			 ORDER BY file_num, part_num`, group, base)
	})
	if err != nil {
		return nil, err
	}
	out := make([]stagedArticle, len(rows))
	for i, r := range rows {
		out[i] = stagedArticle{
			MessageID: r.MessageID, Subject: r.Subject, Poster: r.Poster,
			Bytes: r.Bytes, Group: r.Group, PartNum: r.PartNum,
			TotalParts: r.TotalParts, SegTotal: r.SegTotal,
			FileNum: r.FileNum, TotalFiles: r.TotalFiles, FileParts: r.FileParts,
		}
		if r.Posted.Valid {
			out[i].Posted = r.Posted.Time
		}
	}
	return out, nil
}

type nzbRow struct {
	Title       string
	Filename    string
	Size        int64
	Group       string
	ContentHash string
	Posted      time.Time
	Data        []byte
	Tags        Tags
	CategoryID  int
}

func (s *PGStore) insertNzb(ctx context.Context, n nzbRow) (bool, error) {
	inserted := false
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var posted sql.NullTime
		if !n.Posted.IsZero() {
			posted = sql.NullTime{Time: n.Posted, Valid: true}
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO nzbs (title, filename, size, status, group_name, content_hash, posted_at,
			                   nzb_data, nzb_data_bytes, resolution, source, video_codec, audio, language, category_id)
			 VALUES ($1,$2,$3,'completed',$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
			 ON CONFLICT (content_hash) DO NOTHING`,
			n.Title, n.Filename, n.Size, n.Group, n.ContentHash, posted, n.Data, len(n.Data),
			n.Tags.Resolution, n.Tags.Source, n.Tags.Codec, n.Tags.Audio, n.Tags.Language, n.CategoryID)
		if err != nil {
			return err
		}
		c, _ := res.RowsAffected()
		inserted = c > 0
		return nil
	})
	return inserted, err
}

func (s *PGStore) deleteStaged(ctx context.Context, group, base string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM articles WHERE group_name = $1 AND base_subject = $2`, group, base)
		return err
	})
}

// firstPoster returns the poster of the first article in a set. Every part of a
// release comes from one poster in practice, and prod matches on the same single
// value; scanning them all would let one spoofed part veto a whole release.
func firstPoster(arts []stagedArticle) string {
	if len(arts) == 0 {
		return ""
	}
	return arts[0].Poster
}
