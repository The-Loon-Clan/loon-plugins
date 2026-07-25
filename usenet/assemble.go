package usenet

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/xml"
	"fmt"
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
	if !p.withLease(ctx, leaseScopeJob, jobNameBuild, p.leaseTTL(p.effective(ctx)), func(ctx context.Context) {
		p.buildLocked(ctx)
	}) {
		p.buildJob.Log("build skipped — another worker holds this job")
		p.buildJob.SetIdle(p.nextCrawl(ctx))
	}
}

func (p *Plugin) buildLocked(ctx context.Context) {
	p.buildJob.SetRunning()

	// Pick up admin edits, and make sure last pass's counters are persisted even
	// if that pass died before its own flush.
	p.reloadBlacklist(ctx)
	defer p.flushFilterHits(ctx)

	// Resolve the sink ONCE for the pass (mirrors resolveHealthBackend): a
	// host-misconfigured pass fails here with one error instead of flooding the
	// error log with one per candidate.
	sink, err := p.resolveSink()
	if err != nil {
		p.buildJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/build-sink", err)
		return
	}

	keys, err := p.staging.candidateGroups(ctx, p.effective(ctx).BuildDrainPerPass)
	if err != nil {
		p.buildJob.SetError(err.Error())
		p.reportErr(ctx, "usenet/build-scan", err)
		return
	}
	built, skippedExt, skippedBL := 0, 0, 0
	for _, k := range keys {
		if ctx.Err() != nil {
			break
		}
		arts, err := p.staging.groupArticles(ctx, k.Group, k.Base)
		if err != nil {
			p.reportErr(ctx, "usenet/build-load", err)
			continue
		}
		if len(arts) == 0 || !isComplete(arts) {
			continue // not actually complete yet — leave staged for next round
		}
		// Classification runs in PROD'S order: title extraction, blocked
		// extensions, the operator blacklist, then the sized junk check — which
		// an explicit category tag bypasses, exactly as prod's assembler does.
		title, cat, junkRule, blockedExt := classifyRelease(k.Base, arts)
		if blockedExt {
			// Counted, not logged per-release: a pass drains up to 500 sets, and
			// a per-set SKIP line would evict the pass summary from the 100-line
			// job ring. The count folds into that summary instead.
			skippedExt++
			if err := p.staging.deleteStaged(ctx, k.Group, k.Base); err != nil {
				p.reportErr(ctx, "usenet/build-delete-staged", err)
			}
			continue
		}
		// Operator policy, checked at build like prod does. Deliberately NOT at
		// ingest: blacklist rules are edited far more often than junk rules, and
		// filtering at build means a new rule applies to everything already
		// staged instead of only to what arrives after it.
		if pat := whichBlacklistRule(release{
			Subject: k.Base, Title: title,
			Poster: firstPoster(arts), Group: k.Group,
		}); pat != "" {
			// Attribution is already recorded per-rule in filter_hits; the
			// per-release log line is redundant with that and floods the ring.
			p.hits.note("blacklist", pat, k.Base)
			skippedBL++
			if err := p.staging.deleteStaged(ctx, k.Group, k.Base); err != nil {
				p.reportErr(ctx, "usenet/build-delete-staged", err)
			}
			continue
		}
		if junkRule != "" {
			p.hits.note("junk", junkRule, k.Base)
			if err := p.staging.deleteStaged(ctx, k.Group, k.Base); err != nil { // drop, don't build
				p.reportErr(ctx, "usenet/build-delete-staged", err)
			}
			continue
		}
		xmlBytes, err := buildNZB(arts)
		if err != nil {
			// Malformed input the sanitising didn't cover. Leave the set staged:
			// the prune horizon clears it if it never becomes buildable.
			p.reportErr(ctx, "usenet/build-xml", fmt.Errorf("%s/%s: %w", k.Group, k.Base, err))
			continue
		}
		gz, err := gzipBytes(xmlBytes)
		if err != nil {
			p.reportErr(ctx, "usenet/build-gzip", err)
			continue
		}
		size, posted := summarize(arts)
		rel := pluginapi.AssembledRelease{
			Title: title, BaseSubject: k.Base, Group: k.Group,
			Poster:      firstPoster(arts),
			ContentHash: contentHashArticles(arts),
			SizeBytes:   size, PostedAt: posted,
			NZBGz: gz, Segments: len(arts), CategoryHint: cat,
		}
		created, err := sink.store(ctx, rel)
		if err != nil {
			// Storage failed — leave the set staged so a later pass retries.
			// A transient sink outage must never lose a release.
			p.reportErr(ctx, "usenet/build-store", fmt.Errorf("%s: %w", title, err))
			continue
		}
		// In redis mode this delete is the ONLY way an entry leaves nzb:ready —
		// a persistent failure re-builds the same set every pass forever.
		if err := p.staging.deleteStaged(ctx, k.Group, k.Base); err != nil {
			p.reportErr(ctx, "usenet/build-delete-staged", err)
		}
		if created {
			built++
			// Feed the "recently built" telemetry ring: with sink=host no
			// plugin table records this, and the host table mixes in agent
			// uploads — the ring is what the crawlers page shows.
			p.tel.noteBuilt(title, k.Group, size)
		}
	}
	p.buildJob.Log("built %d NZB file(s) from %d candidate group(s) (skipped %d blocked-ext, %d blacklisted)",
		built, len(keys), skippedExt, skippedBL)
	// Sample the incomplete sets into telemetry — the dashboard's "which
	// releases are still missing articles" card. Done here, once per pass,
	// because listing them (redis: SCAN + a pipelined read per set) is too
	// heavy for the render path.
	if sets, err := p.staging.incompleteSets(ctx, 15); err == nil {
		p.tel.setPending(sets)
	} else {
		p.reportErr(ctx, "usenet/incomplete-sample", err)
	}
	if built > 0 {
		// New releases changed the search surface — publish so a subscriber
		// (e.g. a cache invalidator in the worker) can react. Best-effort: no
		// host event bus => no-op.
		pluginapi.EmitEvent(p.core, ctx, pluginapi.EventIngested, built)
	}
	p.buildJob.SetIdle(p.nextCrawl(ctx))
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
//
// The error return exists because xml.Marshal REFUSES invalid UTF-8, and Usenet
// subjects carry arbitrary bytes. The old form returned nil on error, which
// gzipped fine and produced a "completed" release whose NZB downloads as zero
// bytes — invisible until a user tries it. Attributes are sanitised so the
// error path is nearly unreachable, but if it fires the release is skipped
// loudly rather than stored empty.
func buildNZB(arts []stagedArticle) ([]byte, error) {
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
		return nil, err
	}
	return append([]byte(xml.Header), out...), nil
}

// makeFile builds one <file> from the articles of a single file, segments
// ordered as loaded and de-duped by part number.
func makeFile(arts []stagedArticle) nzbFile {
	first := arts[0]
	f := nzbFile{
		Poster:  strings.ToValidUTF8(first.Poster, "\uFFFD"),
		Date:    first.Posted.Unix(),
		Subject: strings.ToValidUTF8(first.Subject, "\uFFFD"),
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

// maxNZBBytes bounds NZB decompression. NZBs are text; even a 4000-file
// release's XML stays far under this — but in sink=host mode the health sweep
// gunzips the HOST catalogue's blobs, which include agent uploads, and an
// unbounded ReadAll turns one crafted tiny gzip into an OOM'd worker.
const maxNZBBytes = 128 << 20

func gunzipBytes(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, maxNZBBytes+1))
	if err != nil {
		return nil, err
	}
	if len(out) > maxNZBBytes {
		return nil, fmt.Errorf("nzb decompresses past %d bytes — refusing", maxNZBBytes)
	}
	return out, nil
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

// contentHashArticles is prod's content identity: sha256 over the SORTED
// segment message-ids, first 16 bytes as hex. It identifies the ARTICLES, not
// the name — a re-post of the same title with fresh articles hashes new (and
// can be indexed), while the same articles always collide (and dedup). The old
// hash-of-(group|base) meant two different releases sharing a subject collided
// forever and a re-post could never be indexed again.
func contentHashArticles(arts []stagedArticle) string {
	ids := make([]string, 0, len(arts))
	for _, a := range arts {
		if a.MessageID != "" {
			ids = append(ids, a.MessageID)
		}
	}
	sort.Strings(ids)
	h := sha256.New()
	for _, id := range ids {
		h.Write([]byte(id))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
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

// totalBytes sums a staged set's payload — the release size the sized junk
// rules are banded on.
func totalBytes(arts []stagedArticle) int64 {
	var n int64
	for _, a := range arts {
		n += a.Bytes
	}
	return n
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

// classifyRelease runs the title-domain checks in prod's order and returns the
// decision inputs the build loop acts on: the extracted sanitised title, the
// category hint (explicit tag, else comic-archive sniff), the junk rule that
// fired (empty when clean OR when an explicit category tag vouched for the
// release — prod's bypass), and whether the title names a blocked file type.
func classifyRelease(base string, arts []stagedArticle) (title, cat, junkRule string, blockedExt bool) {
	title = strings.TrimSpace(strings.ToValidUTF8(extractTitle(base), "\uFFFD"))
	if title == "" {
		title = "release"
	}
	if hasBlockedExtension(title) {
		return title, "", "", true
	}
	cat = parseCategoryTag(title)
	if cat == "" {
		if junkRule = whichJunkRuleSized(title, totalBytes(arts)); junkRule != "" {
			return title, "", junkRule, false
		}
		if articlesContainComicArchive(arts) {
			cat = "Manga"
		}
	}
	return title, cat, "", false
}

// releaseSink stores one assembled release. Internal mode is the plugin's own
// minimal nzbs table (standalone installs, the demo); host mode is the
// ReleaseSink capability, so a rich host owns storage. Sibling of healthBackend
// — same two-implementation shape, and resolved ONCE per build pass (see
// resolveSink) rather than per release, so a host-misconfigured pass fails with
// a single error instead of one per candidate.
type releaseSink interface {
	store(ctx context.Context, rel pluginapi.AssembledRelease) (created bool, err error)
}

type internalSink struct{ p *Plugin }

func (s internalSink) store(ctx context.Context, rel pluginapi.AssembledRelease) (bool, error) {
	return s.p.st.insertNzb(ctx, nzbRow{
		Title: rel.Title, Filename: safeFilename(rel.Title) + ".nzb",
		Size: rel.SizeBytes, Group: rel.Group, ContentHash: rel.ContentHash,
		Posted: rel.PostedAt, Data: rel.NZBGz, Tags: parseTags(rel.Title),
		CategoryID: s.p.categoryFor(rel.Group, rel.Title),
	})
}

type hostSink struct{ sink pluginapi.ReleaseSink }

func (s hostSink) store(ctx context.Context, rel pluginapi.AssembledRelease) (bool, error) {
	_, created, err := s.sink.IngestAssembled(ctx, rel)
	return created, err
}

// resolveSink mirrors resolveHealthBackend: host mode without the capability
// refuses loudly — silently self-storing splits the catalogue across two tables,
// far worse than a visible stall that retries once the host build is deployed.
func (p *Plugin) resolveSink() (releaseSink, error) {
	if p.cfg.Sink == SinkHost {
		sink, ok := pluginapi.LookupReleaseSink(p.core)
		if !ok {
			return nil, fmt.Errorf(
				"sink=host but this host registered no ReleaseSink — deploy a host build that wires the release sink, or set plugins.usenet.sink=internal for a standalone catalogue")
		}
		return hostSink{sink: sink}, nil
	}
	return internalSink{p: p}, nil
}

// BuilderInfo is the NZB Builder's view of staging: how many articles are
// staged, how many distinct releases they form, how many are ready to assemble,
// and the largest still-incomplete releases (with unit progress) — so an admin
// can see WHY nothing is building (usually huge multi-file releases only
// partly crawled).
type BuilderInfo struct {
	StagedArticles int
	Releases       int
	Ready          int
	Pending        []PendingRelease
}

// PendingRelease is one incomplete staged release. Units are files for
// multi-file releases, else segments.
type PendingRelease struct {
	Base     string
	Have     int
	Need     int
	Segments int
	Multi    bool
}

// Pct is the unit-completion percentage (0-100).
func (p PendingRelease) Pct() int {
	if p.Need <= 0 {
		return 0
	}
	v := p.Have * 100 / p.Need
	if v > 100 {
		v = 100
	}
	return v
}

func (s *PGStore) builderInfo(ctx context.Context, limit int) (BuilderInfo, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var bi BuilderInfo
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &bi.StagedArticles, `SELECT COUNT(*) FROM articles`); err != nil {
			return err
		}
		// One GROUP BY over staging; the derived per-release "have/need units"
		// mirrors candidateGroups (files for multi-file, parts otherwise).
		const setsCTE = `
			WITH sets AS (
			  SELECT bool_or(file_parts) AS multi,
			         CASE WHEN bool_or(file_parts) THEN COUNT(DISTINCT file_num) ELSE COUNT(DISTINCT part_num) END AS have,
			         CASE WHEN bool_or(file_parts) THEN MAX(total_files)          ELSE MAX(total_parts)          END AS need,
			         base_subject, COUNT(*) AS segs
			  FROM articles GROUP BY group_name, base_subject
			)`
		// sqllint:allow setsCTE is a const CTE concatenated with a literal tail; no interpolation
		if err := tx.GetContext(ctx, &bi.Releases, setsCTE+` SELECT COUNT(*) FROM sets`); err != nil {
			return err
		}
		// sqllint:allow setsCTE is a const CTE concatenated with a literal tail; no interpolation
		if err := tx.GetContext(ctx, &bi.Ready, setsCTE+` SELECT COUNT(*) FROM sets WHERE need > 0 AND have >= need`); err != nil {
			return err
		}
		var rows []struct {
			Base  string `db:"base_subject"`
			Have  int    `db:"have"`
			Need  int    `db:"need"`
			Segs  int    `db:"segs"`
			Multi bool   `db:"multi"`
		}
		// sqllint:allow setsCTE is a const CTE concatenated with a literal tail; no interpolation
		if err := tx.SelectContext(ctx, &rows, setsCTE+`
			SELECT base_subject, have, need, segs, multi FROM sets
			WHERE NOT (need > 0 AND have >= need)
			ORDER BY segs DESC LIMIT $1`, limit); err != nil {
			return err
		}
		for _, r := range rows {
			bi.Pending = append(bi.Pending, PendingRelease{
				Base: r.Base, Have: r.Have, Need: r.Need, Segments: r.Segs, Multi: r.Multi,
			})
		}
		return nil
	})
	return bi, err
}
