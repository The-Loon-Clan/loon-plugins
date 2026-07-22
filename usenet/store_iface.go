package usenet

import (
	"context"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Store is usenet's persistence contract. It's segmented into concern-based
// interfaces (interface-segregation) so a consumer can depend on only the slice
// it uses — e.g. the assembler needs AssemblerStore, the backfiller
// BackfillStore — and the read tier could one day bind ReleaseReader to a
// replica. The plugin field holds the union; PGStore is the Postgres impl.
//
// The methods are package-private on purpose: this is an internal contract, so
// only an in-package impl (PGStore) or test double can satisfy it.
type Store interface {
	ReleaseReader
	GroupStore
	ServerStore
	SettingStore
	BackfillStore
	AssemblerStore
	MaintenanceStore
	JunkStore
	HealthStore
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
}

// GroupStore manages the newsgroup catalog.
type GroupStore interface {
	groups(ctx context.Context) ([]pluginapi.GroupInfo, error)
	allGroups(ctx context.Context, query string, limit int) ([]pluginapi.GroupInfo, error)
	activeGroups(ctx context.Context, limit int) ([]groupRow, error)
	groupCount(ctx context.Context) (int, error)
	setGroupActive(ctx context.Context, name string, active bool) error
	upsertGroups(ctx context.Context, names []string) (int, error)
	seedNewsgroups(ctx context.Context, groups []seedGroup) (int, error)
	updateGroupState(ctx context.Context, name string, serverLow, serverHigh, watermark, backSeed int64, hwDate time.Time) error
}

// ServerStore holds the single NNTP server row.
type ServerStore interface {
	getServer(ctx context.Context) (pluginapi.Server, bool, error)
	saveServer(ctx context.Context, srv pluginapi.Server) error
}

// SettingStore is the plugin's key/value settings.
type SettingStore interface {
	getSettings(ctx context.Context) (map[string]string, error)
	setSetting(ctx context.Context, key, value string) error
}

// BackfillStore drives the backward crawl + its builder view.
type BackfillStore interface {
	groupsNeedingBackfill(ctx context.Context, limit int) ([]backfillRow, error)
	updateBackWatermark(ctx context.Context, name string, back int64, oldest time.Time) error
	markBackfillDone(ctx context.Context, name string) error
	resetBackfill(ctx context.Context, name string) error
	builderInfo(ctx context.Context, limit int) (BuilderInfo, error)
	recordFetchedRange(ctx context.Context, group string, start, end int64) error
	coveredRanges(ctx context.Context, group string) ([]articleRange, error)
	backfillGaps(ctx context.Context, group string, low, high int64) ([]articleRange, error)
}

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
