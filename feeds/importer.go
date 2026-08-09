package feeds

import (
	"context"
	"fmt"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/the-loon-clan/loon/schedule"

	lpapi "github.com/the-loon-clan/loon-plugins/pluginapi"
)

// item is one release observed on a feed, unified across sources.
type item struct {
	title    string
	link     string // page URL
	infoHash string
	seeders  int
	size     int64
	sizeStr  string
	category string // "Anime", "Anime - English-translated", etc.
	source   string // "nyaa", "anirena", "tokyotosho", "nekobt"
	tvdbID   int
	tmdbID   int
	airing   bool // matched against the calendar
	// topQuery, when non-empty, identifies the member-typed search query that
	// triggered discovery of this item. Drives a "Top searched: …" line in the
	// request notes so reviewers see why the bot brought it in; topCount
	// carries the search count for the same line.
	topQuery string
	topCount int
}

// airingInfo holds a normalized title plus catalog ids for one
// currently-airing show.
type airingInfo struct {
	anilistID int
	aid       int
	title     string // normalized
}

// reFeedYear matches a year stamp (1980 - 20xx) on a word boundary.
// Used by the old-release filter to distinguish "title carries a
// year that's clearly old" from "no year stamp / current year".
var reFeedYear = regexp.MustCompile(`\b(?:19[89]\d|20[0-9]\d)\b`)

// looksOldByYear returns true when the title carries a 4-digit year
// stamp that is more than a year older than the current year. The
// importer's goal is to default-include — only a positive old-year signal
// triggers a skip. No year stamp ⇒ false (don't skip), because we can't
// tell and missing a new release is worse than queueing an old one (the
// dedup against RecentRequestKeys catches the actual duplicates).
func looksOldByYear(title string, currentYear int) (bool, int) {
	matches := reFeedYear.FindAllString(title, -1)
	maxYear := 0
	for _, m := range matches {
		if y, err := strconv.Atoi(m); err == nil && y > maxYear {
			maxYear = y
		}
	}
	if maxYear == 0 {
		return false, 0
	}
	return maxYear < currentYear-1, maxYear
}

// decideItem is the per-item gate, in the order the statuses depend on: the
// old-year filter first (so an old duplicate reads "skipped_old", matching
// the original service), then hash dedup, then title dedup. Returns one of
// the Status* constants; StatusObserved means "proceed to create".
func decideItem(it item, existing map[string]bool, currentYear int) string {
	if old, _ := looksOldByYear(it.title, currentYear); old {
		return StatusSkippedOld
	}
	if it.infoHash != "" && existing[it.infoHash] {
		return StatusSkippedDupHash
	}
	if existing[strings.ToLower(it.title)] {
		return StatusSkippedDupTitle
	}
	return StatusObserved
}

// sortItems sorts airing items first, then by seeders descending.
func sortItems(items []item) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].airing != items[j].airing {
			return items[i].airing // airing first
		}
		return items[i].seeders > items[j].seeders
	})
}

// runImport is one full import pass: poll every source, judge each item,
// file requests, then the top-searched discovery pass. Driven by ServiceLoop
// on the schedule and by the /admin/jobs trigger on demand.
func (p *Plugin) runImport(ctx context.Context) {
	// A manual trigger must not overlap the scheduled tick (or another
	// trigger). TryLock rather than Lock: the right behaviour is to skip,
	// not to queue a second pass behind the first.
	if !p.runMu.TryLock() {
		p.job.Log("Torrent Feed Import already running — skipping overlapping trigger")
		return
	}
	defer p.runMu.Unlock()

	job := p.job
	job.SetRunning()
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("panic: %v", r)
			job.SetError(msg)
			p.status.runFinished(nil, msg)
			// This recover is load-bearing on the manual-trigger path (a bare
			// goroutine — an unrecovered panic there kills the process), but it
			// also hides the panic from ServiceLoop's PanicSink on the
			// scheduled path. So persist it here, stack included: without this
			// a recurring parser panic shows "panic: ..." on /admin/jobs while
			// /admin/errors stays empty and no stack exists anywhere.
			p.core.Errors.Report(ctx, "feeds/import-panic",
				fmt.Errorf("%s\n%s", msg, debug.Stack()))
		}
	}()

	// The request author. Resolved here rather than at Provision (which may
	// not do I/O); cached once non-zero. Matches the old service, which
	// ignored the lookup error and filed as user 0 until restart — this at
	// least retries each run.
	if p.botUserID == 0 {
		if id, err := p.deps.BotUserID(ctx); err == nil {
			p.botUserID = id
		}
	}

	totals := lpapi.FeedsTotals{}

	// Fetch from all configured feeds.
	var allItems []item
	poll := func(source, label string, fetch func(context.Context) ([]item, error)) {
		if ctx.Err() != nil {
			return // shutting down — don't start another fetch
		}
		job.SetProgress("Fetching %s...", label)
		items, err := fetch(ctx)
		if err != nil {
			if ctx.Err() != nil {
				// Shutdown cancelled the fetch mid-flight. Not a source-health
				// fact — recording it would show "context canceled" as the
				// source's last error after every deploy.
				return
			}
			job.Log("%s error: %v", label, err)
			p.status.pollFailed(source, err)
			return
		}
		job.Log("%s: fetched %d items", source, len(items))
		p.status.pollOK(source, len(items))
		allItems = append(allItems, items...)
	}
	poll("nyaa", "nyaa.si RSS", p.fetchNyaa)
	poll("anirena", "anirena RSS", p.fetchAniRena)
	// Many Tokyo Toshokan items overlap with Nyaa via shared magnet
	// info_hashes; the downstream dedup gate collapses them.
	poll("tokyotosho", "tokyotosho RSS", p.fetchTokyoTosho)
	if p.cfg.NekoBTAPIKey != "" {
		poll("nekobt", "nekoBT Torznab feed", p.fetchNekoBT)
	}
	totals.Fetched = len(allItems)

	if len(allItems) == 0 {
		job.SetIdle(time.Now().Add(defaultInterval))
		p.status.runFinished(&totals, "")
		return
	}

	// Build the airing title index from the calendar for priority matching.
	airingIndex := p.buildAiringIndex()
	if len(airingIndex) > 0 {
		job.Log("Calendar: %d currently-airing titles loaded for priority matching", len(airingIndex))
	}

	// Dedup against existing requests.
	existing, err := p.deps.RecentRequestKeys(ctx, 30)
	if err != nil {
		if ctx.Err() != nil {
			// SIGTERM landed between polling and the dedup query. Close out
			// as an interruption, not an error — otherwise every deploy whose
			// shutdown hits this window records a red "context canceled" run
			// and sends an operator chasing a DB failure that never happened.
			job.Log("Import interrupted by shutdown before dedup")
			job.SetIdle(time.Now().Add(defaultInterval))
			p.status.runFinished(&totals, "interrupted by shutdown")
			return
		}
		msg := fmt.Sprintf("get existing keys: %v", err)
		job.SetError(msg)
		p.status.runFinished(nil, msg)
		return
	}

	// Sort: airing items first, then by seeders descending.
	for i := range allItems {
		allItems[i].airing = isAiring(allItems[i], airingIndex, p.deps.NormalizeTitle)
	}
	sortItems(allItems)

	// Default-include policy: a request is auto-created for any item that
	// doesn't carry a positive "old release" signal. An earlier airing-only
	// gate was dropping new episodes whose title didn't match the calendar
	// window (sub-group rename, calendar lag, niche shows AniList doesn't
	// track) — missing a new release is worse than queueing an old one, since
	// dedup catches duplicates downstream anyway. The airing match is still
	// computed above and used below for the new_release priority boost — it
	// just no longer gates inclusion.
	currentYear := time.Now().Year()

	var created, skipped int
	for i, it := range allItems {
		// recordFeedItem captures the verdict on this row in the host's
		// feed_items so the public Feed tab can list it. Status reflects what
		// was decided AFTER the per-item filters; requestID stays nil unless
		// an actual request is created below.
		recordFeedItem := func(status string, requestID *int64) {
			fi := FeedItem{
				Source:    it.source,
				Title:     it.title,
				InfoHash:  it.infoHash,
				SourceURL: it.link,
				SizeBytes: it.size,
				Seeders:   it.seeders,
				Category:  it.category,
				Status:    status,
				RequestID: requestID,
			}
			if err := p.deps.UpsertFeedItem(ctx, fi); err != nil {
				// best-effort — the next poll re-upserts the same row
				job.Log("WARN feed_items upsert for %q: %v", it.title, err)
			}
		}

		switch decideItem(it, existing, currentYear) {
		case StatusSkippedOld:
			totals.SkippedOld++
			skipped++
			if totals.SkippedOld <= 5 {
				_, year := looksOldByYear(it.title, currentYear)
				job.Log("Skipping old release (year=%d): %q", year, it.title)
			}
			recordFeedItem(StatusSkippedOld, nil)
			continue
		case StatusSkippedDupHash:
			totals.SkippedDupHash++
			skipped++
			recordFeedItem(StatusSkippedDupHash, nil)
			continue
		case StatusSkippedDupTitle:
			totals.SkippedDupTitle++
			skipped++
			recordFeedItem(StatusSkippedDupTitle, nil)
			continue
		}

		req := p.buildRequest(ctx, it, airingIndex)
		if req == nil {
			totals.Observed++
			recordFeedItem(StatusObserved, nil)
			continue
		}

		newID, err := p.deps.CreateRequest(ctx, *req)
		if err != nil {
			if strings.Contains(err.Error(), "duplicate") {
				totals.SkippedDupHash++
				skipped++
				recordFeedItem(StatusSkippedDupHash, nil)
				continue
			}
			job.Log("Error creating request for %q: %v", it.title, err)
			totals.Observed++
			recordFeedItem(StatusObserved, nil)
			continue
		}
		created++
		totals.Created++
		recordFeedItem(StatusRequestCreated, &newID)
		if it.airing {
			totals.CreatedAiring++
			// Mark airing content with new_release priority so agents pick
			// it up faster.
			_ = p.deps.BumpPriority(ctx, newID, "new_release")
		}
		if it.infoHash != "" {
			existing[it.infoHash] = true
		}
		existing[strings.ToLower(it.title)] = true

		// Cross-write to the release-group archive cache when the title's
		// `[GroupName]` prefix maps to an existing release group. Lets the
		// per-group Archive tab stay near-realtime (within the feed cycle)
		// instead of waiting for the daily archive sweep — same data, two
		// tables, single fetch.
		p.crossWriteToGroupArchive(ctx, it)

		if (i+1)%25 == 0 {
			job.SetProgress("Processing %d/%d (created %d, skipped %d)", i+1, len(allItems), created, skipped)
		}

		// Politeness pacing between creations; also the shutdown seam — a
		// SIGTERM lands here rather than after another N items.
		if !schedule.SleepCtx(ctx, 50*time.Millisecond) {
			job.Log("Import interrupted by shutdown at %d/%d items", i+1, len(allItems))
			job.SetIdle(time.Now().Add(defaultInterval))
			p.status.runFinished(&totals, "interrupted by shutdown")
			return
		}
	}

	job.Log("Done: %d created (%d airing), %d skipped (%d old) from %d total",
		created, totals.CreatedAiring, skipped, totals.SkippedOld, len(allItems))

	// Top-searched discovery pass: ride the same infrastructure to look up
	// the most popular zero-grab member queries and pull the best-seeded
	// torrent for each. The community gets a suggestion to vote on; the notes
	// carry the original query + search count so it's clear where the find
	// came from.
	topCreated, topSkipped := p.runTopSearched(ctx, existing)
	totals.TopSearchedCreated = topCreated
	totals.TopSearchedSkipped = topSkipped
	if topCreated > 0 || topSkipped > 0 {
		job.Log("Top-searched: %d created, %d skipped", topCreated, topSkipped)
	}

	// ServiceLoop announces the true next run (with any admin interval
	// override applied) right after this returns; the default here is only a
	// placeholder for the manual-trigger path.
	job.SetIdle(time.Now().Add(defaultInterval))
	p.status.runFinished(&totals, "")
}

// pickFailedSearches filters the zero-grab queries to the ones worth chasing,
// preserving the search-count DESC ordering FailedSearches returns.
func pickFailedSearches(failed []FailedSearch, minCount, batch int) []FailedSearch {
	var picked []FailedSearch
	for _, q := range failed {
		if q.Count < minCount {
			continue
		}
		if q.Query == "" {
			continue
		}
		picked = append(picked, q)
		if len(picked) >= batch {
			break
		}
	}
	return picked
}

// runTopSearched is the third trigger source for the importer (alongside the
// RSS firehose and the host's resurrector): read the most popular search
// queries that returned zero grabs, search Nyaa for each, and create a
// community request for the top-seeded hit. Reuses the run's dedup map so it
// can't double-add an item the firehose just imported.
//
// Returns (created, skipped) for the run-level summary line.
func (p *Plugin) runTopSearched(ctx context.Context, existing map[string]bool) (int, int) {
	const (
		// How many failed queries to investigate per run. Each adds one Nyaa
		// search HTTP hit so we keep this modest.
		topSearchedBatch = 15
		// Minimum search count to bother — below this, the query is probably
		// a one-off typo and not worth queueing a request.
		topSearchedMinCount = 5
		// Politeness delay between Nyaa search hits.
		topSearchedDelay = 1500 * time.Millisecond
	)
	job := p.job
	failed, err := p.deps.FailedSearches(ctx, topSearchedBatch*3)
	if err != nil {
		job.Log("Top-searched: failed searches: %v", err)
		return 0, 0
	}
	picked := pickFailedSearches(failed, topSearchedMinCount, topSearchedBatch)
	if len(picked) == 0 {
		return 0, 0
	}
	job.SetProgress("Top-searched: investigating %d queries", len(picked))

	var created, skipped int
	for i, q := range picked {
		hits, err := p.fetchNyaaSearch(ctx, q.Query, 1)
		if err != nil {
			job.Log("Top-searched %q: Nyaa search error: %v", q.Query, err)
			continue
		}
		if len(hits) == 0 {
			continue
		}
		hit := hits[0]
		if hit.infoHash != "" && existing[hit.infoHash] {
			skipped++
			continue
		}
		if existing[strings.ToLower(hit.title)] {
			skipped++
			continue
		}
		hit.topQuery = q.Query
		hit.topCount = q.Count
		req := p.buildRequest(ctx, hit, nil) // airing index irrelevant — top-searched isn't calendar-driven
		if req == nil {
			continue
		}
		newID, err := p.deps.CreateRequest(ctx, *req)
		if err != nil {
			if strings.Contains(err.Error(), "duplicate") {
				skipped++
				continue
			}
			job.Log("Top-searched %q: create error: %v", q.Query, err)
			continue
		}
		// Tag with the top_searched priority badge so the request listing
		// distinguishes search-driven discoveries from RSS firehose imports
		// and dead-release resurrections.
		_ = p.deps.BumpPriority(ctx, newID, "top_searched")
		created++
		if hit.infoHash != "" {
			existing[hit.infoHash] = true
		}
		existing[strings.ToLower(hit.title)] = true
		job.Log("Top-searched %q (%d searches) → %q (%d seeders)",
			q.Query, q.Count, hit.title, hit.seeders)
		if (i+1)%5 == 0 {
			job.SetProgress("Top-searched: %d/%d (created %d)", i+1, len(picked), created)
		}
		if !schedule.SleepCtx(ctx, topSearchedDelay) {
			return created, skipped
		}
	}
	return created, skipped
}

// ── Build a request from a feed item ────────────────────────────────────────

var (
	reFeedResolution = regexp.MustCompile(`(?i)\b(2160|1080|720|480)p\b`)
	reFeedSources    = []struct {
		re    *regexp.Regexp
		label string
	}{
		{regexp.MustCompile(`(?i)\bBlu-?Ray\s*Remux\b`), "BluRay Remux"},
		{regexp.MustCompile(`(?i)\bBDRemux\b`), "BluRay Remux"},
		{regexp.MustCompile(`(?i)\bBDRip\b`), "BDRip"},
		{regexp.MustCompile(`(?i)\bBlu-?Ray\b`), "BluRay"},
		{regexp.MustCompile(`(?i)\bWEB-DL\b`), "WEB-DL"},
		{regexp.MustCompile(`(?i)\bWEBDL\b`), "WEB-DL"},
		{regexp.MustCompile(`(?i)\bWEBRip\b`), "WEBRip"},
		{regexp.MustCompile(`(?i)\bHDTV\b`), "HDTV"},
		{regexp.MustCompile(`(?i)\bDVDRip\b`), "DVDRip"},
		{regexp.MustCompile(`(?i)\bDVD\b`), "DVD"},
	}
	reFeedSeasonEp   = regexp.MustCompile(`(?i)S(\d{1,2})\s*E(\d{1,4})`)
	reFeedSeasonOnly = regexp.MustCompile(`(?i)(?:Season|S)(\d{1,2})`)
	reFeedEpOnly     = regexp.MustCompile(`(?i)\b(?:E|EP)(\d{1,4})\b`)
)

func (p *Plugin) buildRequest(ctx context.Context, it item, airingIndex []airingInfo) *ReleaseRequest {
	title := strings.TrimSpace(it.title)
	if title == "" {
		return nil
	}

	// Reject blocked extensions (executables, ISOs, scripts, etc.) at
	// ingest time — the host's request handler gates here, its assembler
	// gates at NZB-build time, and the agent strips blocked files
	// post-download. Without this guard the importer can file a request for
	// "Movie.iso" and the agent will dutifully fetch + delete the .iso, then
	// fail with a confusing "expected file not found" prepare error.
	if p.deps.BlockedExtension(title) {
		return nil
	}

	// Parse metadata from the title.
	var resolution, source, season, episodes string
	if m := reFeedResolution.FindStringSubmatch(title); m != nil {
		resolution = m[1] + "p"
	}
	for _, rs := range reFeedSources {
		if rs.re.MatchString(title) {
			source = rs.label
			break
		}
	}
	if m := reFeedSeasonEp.FindStringSubmatch(title); m != nil {
		season = m[1]
		episodes = m[2]
	} else {
		if m := reFeedSeasonOnly.FindStringSubmatch(title); m != nil {
			season = m[1]
		}
		if m := reFeedEpOnly.FindStringSubmatch(title); m != nil {
			episodes = m[1]
		}
	}

	// Cross-lookup external ids to fill AniDB/MAL/AniList.
	var animeID, malID, anilistID *int

	// First try: match against the airing calendar by title.
	if animeID == nil {
		normTitle := p.deps.NormalizeTitle(title)
		for _, ai := range airingIndex {
			if strings.Contains(normTitle, ai.title) || strings.Contains(ai.title, normTitle) {
				if ai.aid > 0 {
					if meta, _ := p.deps.AnimeByID(ctx, ai.aid); meta != nil {
						animeID = intPtr(meta.AID)
						malID = meta.MalID
						anilistID = meta.AnilistID
					}
				} else if ai.anilistID > 0 {
					if meta, _ := p.deps.AnimeByAnilistID(ctx, ai.anilistID); meta != nil {
						animeID = intPtr(meta.AID)
						malID = meta.MalID
						anilistID = meta.AnilistID
					}
				}
				break
			}
		}
	}

	// Second try: TVDB/TMDB from feed attrs.
	if animeID == nil && it.tvdbID > 0 {
		if meta, _ := p.deps.AnimeByTvdbID(ctx, it.tvdbID); meta != nil {
			animeID = intPtr(meta.AID)
			malID = meta.MalID
			anilistID = meta.AnilistID
		}
	}
	if animeID == nil && it.tmdbID > 0 {
		if meta, _ := p.deps.AnimeByTmdbID(ctx, it.tmdbID); meta != nil {
			animeID = intPtr(meta.AID)
			malID = meta.MalID
			anilistID = meta.AnilistID
		}
	}

	// Notes.
	var notes []string
	if it.sizeStr != "" {
		notes = append(notes, "Size: "+it.sizeStr)
	} else if it.size > 0 {
		notes = append(notes, fmt.Sprintf("Size: %.2f GiB", float64(it.size)/float64(1<<30)))
	}
	if it.airing {
		notes = append(notes, "Currently airing")
	}
	if it.topQuery != "" {
		if it.topCount > 0 {
			notes = append(notes, fmt.Sprintf("Top searched (%d): %q", it.topCount, it.topQuery))
		} else {
			notes = append(notes, fmt.Sprintf("Top searched: %q", it.topQuery))
		}
	}
	notes = append(notes, fmt.Sprintf("Auto-imported from %s", it.source))

	// Persist the feed-reported size so the host's agent-poll dispatcher can
	// filter on it. Items without a parseable size fall through with nil,
	// which means "unknown — ship it to any agent."
	var sizeBytes *int64
	if it.size > 0 {
		sz := it.size
		sizeBytes = &sz
	}

	// nekoBT Torznab attrs already give us tvdbid/tmdbid. Persist them so the
	// host's cross-ID resolution pass can find the anime when title-matching
	// fails.
	var tvdbPtr, tmdbPtr *int
	if it.tvdbID > 0 {
		v := it.tvdbID
		tvdbPtr = &v
	}
	if it.tmdbID > 0 {
		v := it.tmdbID
		tmdbPtr = &v
	}

	return &ReleaseRequest{
		UserID:     p.botUserID,
		Username:   it.source + "-bot",
		Title:      title,
		Category:   "Anime",
		Resolution: resolution,
		Source:     source,
		Season:     season,
		Episodes:   episodes,
		PageURL:    it.link,
		InfoHash:   it.infoHash,
		Seeders:    it.seeders,
		AnimeID:    animeID,
		MalID:      malID,
		AnilistID:  anilistID,
		TvdbID:     tvdbPtr,
		TmdbID:     tmdbPtr,
		Notes:      strings.Join(notes, " | "),
		SizeBytes:  sizeBytes,
	}
}

// ── Calendar cross-reference ────────────────────────────────────────────────

// buildAiringIndex creates a normalized-title index from the host's calendar,
// deduplicated by AniList id.
func (p *Plugin) buildAiringIndex() []airingInfo {
	entries := p.deps.AiringEntries()
	seen := make(map[int]bool)
	var index []airingInfo
	for _, e := range entries {
		if seen[e.AnilistID] {
			continue
		}
		seen[e.AnilistID] = true
		norm := p.deps.NormalizeTitle(e.Title)
		if norm == "" {
			continue
		}
		index = append(index, airingInfo{
			anilistID: e.AnilistID,
			aid:       e.AID,
			title:     norm,
		})
	}
	return index
}

// isAiring returns true if the item's title matches a currently-airing show.
func isAiring(it item, index []airingInfo, normalize func(string) string) bool {
	if len(index) == 0 {
		return false
	}
	norm := normalize(it.title)
	for _, ai := range index {
		if strings.Contains(norm, ai.title) || strings.Contains(ai.title, norm) {
			return true
		}
	}
	return false
}

func intPtr(v int) *int {
	return &v
}

// ── Release-group archive cross-write ───────────────────────────────────────

// reFeedGroupPrefix captures the bracketed group name at the start of
// a feed item's title. Matches `[SubsPlease]`, `[Erai-raws]`, etc., as
// well as the fullwidth `【…】` form used by some Japanese groups.
// Anchored with ^ so we don't match a bracket later in the title
// (e.g. `(01) [crc32]` at the end).
var reFeedGroupPrefix = regexp.MustCompile(`^[\s]*[\[【]([^\]】]+)[\]】]`)

// reNyaaViewID pulls the numeric view id out of a Nyaa torrent URL —
// the .torrent download URL `https://nyaa.si/download/<id>.torrent`
// shares its id with the view-page URL. Used to build the archive
// table's external-id key.
var reNyaaViewID = regexp.MustCompile(`/(?:download|view)/(\d+)`)

// reNekobtTorrentID extracts the torrent snowflake from a nekoBT URL
// shape like https://nekobt.to/torrents/<id>(/anything). nekoBT uses
// stringly-typed snowflakes; we don't parse to int.
var reNekobtTorrentID = regexp.MustCompile(`/torrents/(\d+)`)

// groupArchiveKey builds the source-specific external-id key for the archive
// upsert: "nyaa-<view id>" for Nyaa, the snowflake for nekoBT, "" when no
// stable key can be built (other sources included).
func groupArchiveKey(source, link string) string {
	switch source {
	case "nyaa":
		if m := reNyaaViewID.FindStringSubmatch(link); len(m) == 2 {
			return "nyaa-" + m[1]
		}
	case "nekobt":
		if m := reNekobtTorrentID.FindStringSubmatch(link); len(m) == 2 {
			return m[1]
		}
	}
	return ""
}

// crossWriteToGroupArchive is the feed → archive-table bridge. When an item's
// title carries a `[GroupName]` prefix that maps to an existing release
// group, the torrent is mirrored into the group's archive so its Archive tab
// catches it on the next page load instead of waiting for the daily
// per-group sweep. Skips when:
//   - no prefix could be extracted
//   - the slug doesn't match an existing group (no auto-creation —
//     admin-curated grouping is preserved)
//   - no info_hash or no stable external id (the upsert keys)
//
// Best-effort: silent skip on any missing prerequisite; one reported error
// per actual write failure so the feed loop isn't drowned in noise.
func (p *Plugin) crossWriteToGroupArchive(ctx context.Context, it item) {
	if it.infoHash == "" {
		return
	}
	m := reFeedGroupPrefix.FindStringSubmatch(it.title)
	if len(m) != 2 {
		return // no [Group] prefix → can't attribute without an extra HTTP call
	}
	slug := p.deps.SlugifyGroup(strings.TrimSpace(m[1]))
	if slug == "" {
		return
	}
	groupID, ok, err := p.deps.GroupIDBySlug(ctx, slug)
	if err != nil || !ok {
		return
	}

	externalID := groupArchiveKey(it.source, it.link)
	if externalID == "" {
		return
	}

	row := GroupTorrent{
		ExternalID: externalID,
		GroupID:    groupID,
		Title:      strings.TrimSpace(it.title),
		InfoHash:   strings.ToLower(strings.TrimSpace(it.infoHash)),
		SizeBytes:  it.size,
		Seeders:    it.seeders,
	}
	if err := p.deps.UpsertGroupTorrent(ctx, row); err != nil {
		p.core.Errors.Report(ctx, "feeds/cross-write-archive", err)
	}
}
