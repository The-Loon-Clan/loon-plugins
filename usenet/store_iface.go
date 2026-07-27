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
	queryReleases(ctx context.Context, cond, arg string, limit int) ([]pluginapi.Release, error)
	feedReleases(ctx context.Context, query string, cats []int, limit, offset int) ([]pluginapi.Release, int, error)
	releaseByID(ctx context.Context, id int64) (*detailRow, error)
	nzbData(ctx context.Context, id int64) ([]byte, string, error)
	stats(ctx context.Context) (pluginapi.IndexStats, error)
	// forwardBacklog is the total articles the servers hold past our forward
	// watermarks across active groups — the crawl catch-up loop's signal.
	forwardBacklog(ctx context.Context) (int64, error)
}

// GroupStore manages the newsgroup catalog.
type GroupStore interface {
	groups(ctx context.Context) ([]pluginapi.GroupInfo, error)
	allGroups(ctx context.Context, query string, limit int) ([]pluginapi.GroupInfo, error)
	activeGroups(ctx context.Context, limit int) ([]groupRow, error)
	activeGroupsForBackbone(ctx context.Context, backbone string, limit int) ([]groupRow, error)
	updateGroupStateForBackbone(ctx context.Context, backbone, name string, serverLow, serverHigh, watermark, backSeed int64, hwDate time.Time) error
	groupCount(ctx context.Context) (int, error)
	setGroupActive(ctx context.Context, name string, active bool) error
	setGroupTuning(ctx context.Context, name string, retentionDays, throttleMs int, tier Tier) error
	resetWatermark(ctx context.Context, backbone, group string) (watermarkReset, error)
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
	filterHitRows(ctx context.Context) ([]filterHitRow, error)
	resetFilterHits(ctx context.Context) error
}

// (The old ActivityStore — recentArticles/recentNZBs — is gone: the crawlers
// page's liveness readout now comes from the telemetry ring, which is correct
// in every staging/sink mode. See telemetry.go noteBuilt.)

// AssemblerStore is the staging area the NZB assembler reads + drains.
type AssemblerStore interface {
	candidateGroups(ctx context.Context, limit int) ([]groupKey, error)
	groupArticles(ctx context.Context, group, base string) ([]stagedArticle, error)
	deleteStaged(ctx context.Context, group, base string) error
	insertNzb(ctx context.Context, n nzbRow) (bool, error)
	stageArticles(ctx context.Context, arts []stagedArticle) (int, error)
}

// JunkStore is the tunable junk-rule set (seeded from the embedded TSV, loaded
// into memory — see junk.go).
type JunkStore interface {
	seedJunkRules(ctx context.Context, specs []junkRuleSpec) (int, error)
	junkRules(ctx context.Context) ([]junkRuleSpec, error)
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
// staging-side cleanup (deleteJunkStaged / prune) moved to stagingStore (staging.go)
// so it swaps with the backend.
type MaintenanceStore interface {
	retagUntagged(ctx context.Context, limit int) (int, error)
	recategorizeDefaults(ctx context.Context, fn func(group, title string) int, limit int) (int, error)
	pruneNzbs(ctx context.Context, days int) (int64, error)
	deleteJunkNzbs(ctx context.Context) (int, error)
}
