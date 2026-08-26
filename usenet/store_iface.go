package usenet

import (
	"context"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Store is usenet's persistence contract. It's segmented into concern-based
// interfaces (interface-segregation) so a consumer can depend on only the slice
// it uses — internalHealth (health.go) takes just HealthStore, and the read tier
// could one day bind ReleaseReader to a replica. The plugin field holds the
// union; PGStore is the Postgres impl.
//
// The methods are package-private on purpose: this is an internal contract, so
// only an in-package impl (PGStore) or test double can satisfy it.
type Store interface {
	ReleaseReader
	GroupStore
	ServerStore
	SettingStore
	BackfillStore
	BlacklistStore
	AssemblerStore
	MaintenanceStore
	JunkStore
	HealthStore
	LeaseStore
	WorkerStore
}

// ReleaseReader is the read side: search, browse, feed, detail, raw NZB, stats.
type ReleaseReader interface {
	searchNzbs(ctx context.Context, q string, limit int) ([]pluginapi.Release, error)
	browseNzbs(ctx context.Context, group string, limit int) ([]pluginapi.Release, error)
	queryReleases(ctx context.Context, cond string, args []any, limit int) ([]pluginapi.Release, error)
	feedReleases(ctx context.Context, f feedFilter, limit, offset int) ([]pluginapi.Release, int, error)
	releaseByID(ctx context.Context, id int64) (*detailRow, error)
	nzbData(ctx context.Context, id int64) ([]byte, string, error)
	stats(ctx context.Context) (pluginapi.IndexStats, error)
	// statsTotals is the poll-safe scalar subset of stats — see store.go.
	statsTotals(ctx context.Context) (indexTotals, error)
	// statsTotalsExact is the same three scalars with the row counts
	// COUNTED rather than estimated. For the hourly snapshot, not for the
	// 5-second poll -- see both implementations.
	statsTotalsExact(ctx context.Context) (indexTotals, error)
	// forwardBacklog is the total articles the servers hold past our forward
	// watermarks across active groups — the crawl catch-up loop's signal.
	forwardBacklog(ctx context.Context, holdLow bool) (int64, error)
}

// GroupStore manages the newsgroup catalog.
type GroupStore interface {
	groups(ctx context.Context) ([]pluginapi.GroupInfo, error)
	allGroups(ctx context.Context, query, backbone string, limit int) ([]pluginapi.GroupInfo, error)
	activeGroups(ctx context.Context, limit int) ([]groupRow, error)
	activeGroupsForBackbone(ctx context.Context, backbone string, limit int, holdLow bool) ([]groupRow, error)

	// Spotnet index (spot_store.go).
	upsertSpots(ctx context.Context, rows []spotRow) (int, error)
	spotGroups(ctx context.Context) ([]spotGroup, error)
	setSpotGroupExtent(ctx context.Context, name string, low, high int64) error
	advanceSpotHigh(ctx context.Context, name string, high int64) error
	lowerSpotBack(ctx context.Context, name string, back int64, done bool) error
	countSpots(ctx context.Context) (spotCounts, error)
	setGroupKind(ctx context.Context, name, kind string) error
	unfetchedSpots(ctx context.Context, limit int) ([]spotWork, error)
	markSpotFetched(ctx context.Context, messageID string, d spotDocument) error

	// Outcome counters (opstats.go).
	recordOpStats(ctx context.Context, stats map[opStatKey]int64) error
	recentOpStats(ctx context.Context, days int) ([]opStatRow, error)
	// criticalBackfillPending reports whether any CRITICAL group still has
	// history to pull on this backbone — the condition that holds the low tier.
	criticalBackfillPending(ctx context.Context, backbone string) (backfillPending, error)
	updateGroupStateForBackbone(ctx context.Context, backbone, name string, serverLow, serverHigh, watermark, backSeed int64, hwDate time.Time) error
	groupCount(ctx context.Context) (int, error)
	setGroupActive(ctx context.Context, name string, active bool) error
	activeGroupNames(ctx context.Context) ([]string, error)
	setGroupTuning(ctx context.Context, name string, retentionDays, throttleMs int, tier Tier) error
	resetWatermark(ctx context.Context, backbone, group string, scope resetScope) (watermarkReset, error)

	// Poster watch: per-poster attribution of why releases do or do not
	// appear (poster_watch.go).
	posterWatchPatterns(ctx context.Context) ([]string, error)
	setPosterWatch(ctx context.Context, pattern, note string, enabled bool) error
	deletePosterWatch(ctx context.Context, pattern string) error
	posterHitRows(ctx context.Context, limit int) ([]posterHitRow, error)
	// posterWatchRows is the full read: patterns alone cannot round-trip a
	// watch, and disabling one has to be exactly undoable.
	posterWatchRows(ctx context.Context) ([]posterWatchRow, error)
	recordPosterHits(ctx context.Context, hits map[posterHitKey]*posterHitVal) error
	moveGroup(ctx context.Context, name string, delta int) error
	deleteGroup(ctx context.Context, name string) error
	deleteInactiveGroups(ctx context.Context) (int64, error)
	upsertGroups(ctx context.Context, names []string) (int, error)
	seedNewsgroups(ctx context.Context, groups []seedGroup) (int, error)
}

// ServerStore holds the single NNTP server row.
type ServerStore interface {
	getServer(ctx context.Context) (pluginapi.Server, bool, error)
	providers(ctx context.Context) ([]provider, error)
	listServers(ctx context.Context) ([]provider, error)
	upsertServer(ctx context.Context, pr provider) error
	serverPassword(ctx context.Context, id int) (string, error)
	deleteServer(ctx context.Context, id int) error
	toggleServer(ctx context.Context, id int) error
	saveServer(ctx context.Context, srv pluginapi.Server) error
}

// SettingStore is the plugin's key/value settings.
type SettingStore interface {
	getSettings(ctx context.Context) (map[string]string, error)
	setSetting(ctx context.Context, key, value string) error
	adoptFromHost(ctx context.Context, backbone string) (groups, state, blacklist int64, hostFound bool, err error)
}

// BackfillStore drives the backward crawl + its builder view.
type BackfillStore interface {
	groupsNeedingBackfillForBackbone(ctx context.Context, backbone string, limit int) ([]backfillRow, error)
	updateBackWatermarkForBackbone(ctx context.Context, backbone, name string, back int64, oldest time.Time) error
	markBackfillDoneForBackbone(ctx context.Context, backbone, name string) error
	anyBackfillPending(ctx context.Context) (bool, error)
	resetBackfillForGroup(ctx context.Context, group string) error
	builderInfo(ctx context.Context, limit int) (BuilderInfo, error)
	recordFetchedRangeFor(ctx context.Context, backbone, group string, start, end int64) error
	backfillGapsFor(ctx context.Context, backbone, group string, low, high int64) ([]articleRange, error)
	coveredRangesFor(ctx context.Context, backbone, group string) ([]articleRange, error)
	allCoveredRanges(ctx context.Context) (map[coverKey][]articleRange, error)
}

// BlacklistStore is the operator blacklist + the per-rule filter-hit counters
// (blacklist_store.go).
type BlacklistStore interface {
	blacklistRules(ctx context.Context) ([]blacklistRule, error)
	addBlacklistRule(ctx context.Context, pattern, field string) error
	deleteBlacklistRule(ctx context.Context, id int64) error
	toggleBlacklistRule(ctx context.Context, id int64) error
	recordFilterHits(ctx context.Context, hits map[filterHitKey]*filterHitVal) error

	// recordBuildOutcomes folds one build pass's per-reason counts into today's
	// rows. Same accumulate-then-upsert discipline as recordFilterHits, for the
	// same reason: a write per candidate set would cost more than the assembly.
	recordBuildOutcomes(ctx context.Context, out map[buildOutcome]*outcomeVal) error
	// titleRejectableTotals prices the poster watch: how many sets a build pass
	// dropped for a reason the title alone could have decided, and therefore
	// paid a staged-article load for while the fast path was disabled.
	titleRejectableTotals(ctx context.Context, days int) (int64, error)
	// The counters split by population, not by table: rules are bounded and
	// read whole; instrument observations are unbounded and read a page at a
	// time. See blacklist_store.go for why they cannot share one read.
	ruleHitRows(ctx context.Context) ([]filterHitRow, error)
	diagnosticHits(ctx context.Context, kind string, limit, offset int) (diagPage, error)
	pruneFilterDiagnostics(ctx context.Context, keepDays int) (int64, error)
	resetFilterHits(ctx context.Context) error
}

// (The old ActivityStore — recentArticles/recentNZBs — is gone: the crawlers
// page's liveness readout now comes from the telemetry ring, which is correct
// in every staging/sink mode. See telemetry.go noteBuilt.)

// AssemblerStore is the staging area the NZB assembler reads + drains.
type AssemblerStore interface {
	candidateGroups(ctx context.Context, limit int) ([]groupKey, candidateStats, error)

	// Staging census — the time series that makes the gap between "staged" and
	// "built" observable. See staging_census.go.
	recordStagingCensus(ctx context.Context, c stagingCensus) error
	stagingCensusRows(ctx context.Context, limit int) ([]censusRow, error)
	pruneStagingCensus(ctx context.Context, keepDays int) (int64, error)
	// The subject corpus (grouping_watch.go): a rolling sample of raw
	// subjects for differential parser testing, plus its prune.
	insertSubjectCorpus(ctx context.Context, rows []corpusRow) error
	pruneSubjectCorpus(ctx context.Context, keepDays int) (int64, error)
	junkDropsToProbe(ctx context.Context, limit int) ([]junkProbeRow, error)
	recordJunkProbe(ctx context.Context, id int64, name string) error
	junkDropsReport(ctx context.Context, limit int) (junkDropReport, error)
	// Completion-distance instrumentation (resolutions.go): the measured
	// basis for the position-based staging window.
	groupWatermarks(ctx context.Context, groups []string) (map[string]groupMarks, error)
	insertSetResolutions(ctx context.Context, rows []setResolution, marks map[string]groupMarks) error
	pruneSetResolutions(ctx context.Context, keepDays int) (int64, error)
	groupArticles(ctx context.Context, group, base string) ([]stagedArticle, error)
	deleteStaged(ctx context.Context, group, base string) error

	// insertNzb returns the new row's id (0 on a content_hash duplicate) —
	// the salvage path hands it to the health backend for its verdict.
	insertNzb(ctx context.Context, n nzbRow) (int64, bool, error)
	stageArticles(ctx context.Context, arts []stagedArticle) (int, error)
}

// JunkStore is the tunable junk-rule set (seeded from the embedded TSV, loaded
// into memory — see junk.go).
type JunkStore interface {
	seedJunkRules(ctx context.Context, specs []junkRuleSpec) (int, error)
	junkRules(ctx context.Context) ([]junkRuleSpec, error)
	// The order editor: rules in EVALUATION order with their lifetime hit
	// counts, and the writes that reorder or retire one. Order is operator
	// state, not a code constant — `match` returns on the first hit, so where
	// a rule sits decides how much CPU every article above it costs.
	junkRuleStats(ctx context.Context) ([]junkRuleStat, error)
	setJunkRulePositions(ctx context.Context, order map[string]int) error
	setJunkRuleEnabled(ctx context.Context, name string, enabled bool) error
}

// HealthStore is the NZB health surface (health.go).
type HealthStore interface {
	nzbsNeedingHealthCheck(ctx context.Context, limit, recheckDays, minAgeHours int) ([]healthRow, error)
	updateNzbHealth(ctx context.Context, id int64, status string, total, missing, par2 int) error
	touchHealthChecked(ctx context.Context, id int64) error
	healthBreakdown(ctx context.Context) (map[string]int, error)
	// catalogTotals is the internal-sink Index Stats read: count + size of the
	// plugin's own catalogue. That table is small by construction (internal
	// mode is the standalone/demo path), so the SUM is fine — host mode reads
	// the host's CACHED stats through the CatalogStats capability instead.
	catalogTotals(ctx context.Context) (count, size int64, err error)
}

// WorkerStore is crawler presence, used to split groups between hosts (assign.go).
type WorkerStore interface {
	heartbeat(ctx context.Context, worker string) error
	eligibleWorkers(ctx context.Context, termStart time.Time, staleAfter time.Duration) ([]string, error)
	reapWorkers(ctx context.Context, staleAfter time.Duration) error
}

// LeaseStore is cross-host coordination (lease.go): who crawls which backbone,
// and which worker owns the cluster-wide jobs.
type LeaseStore interface {
	claimLease(ctx context.Context, scope, key, worker string, ttl time.Duration) (bool, error)
	releaseLease(ctx context.Context, scope, key, worker string) error
	leaseHolders(ctx context.Context, scope string) (map[string]string, error)
}

// MaintenanceStore is the nzbs cleanup / retagging surface (off-peak jobs). The
// staging-side cleanup (prune) moved to stagingStore (staging.go)
// so it swaps with the backend.
type MaintenanceStore interface {
	retagUntagged(ctx context.Context, limit int) (int, error)
	// fillEpisodes reads series/season/episode out of titles never read
	// before. Returns how many were FILED and how many were read — two
	// thirds of an index is films and software, so the pair is the honest
	// report and the first number alone would read as a poor hit rate.
	fillEpisodes(ctx context.Context, limit int) (parsed, seen int, err error)

	// The series reads (series_store.go) — shows rather than releases.
	seriesList(ctx context.Context, query string, limit, offset int) ([]pluginapi.SeriesRow, int, error)
	seriesName(ctx context.Context, key string) (string, bool, error)
	seriesSeasons(ctx context.Context, key string) ([]pluginapi.SeriesSeason, error)
	seriesReleases(ctx context.Context, key string, season, episode, limit int) ([]pluginapi.Release, error)
	seasonPresence(ctx context.Context, key string, season int) (map[int]bool, bool, error)
	recategorizeSweep(ctx context.Context, fn func(group, title string) int, limit int) (int, error)
	pruneNzbs(ctx context.Context, days int) (int64, error)
	deleteJunkNzbs(ctx context.Context) (int, error)
}
