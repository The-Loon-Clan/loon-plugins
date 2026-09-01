// Package pluginapi holds the neutral contracts between host-data worker
// plugins in this repo and the site that hosts them. A plugin imports these
// interfaces; the host implements them with thin adapters over its existing
// repositories and injects them via each plugin's SetDeps before core.Boot.
//
// Neither side imports the other's concrete packages. This is what lets a job
// that reads and writes the host's own tables live in a separate module without
// depending on the host.
//
// The package holds two flavors of contract: the SetDeps-injected worker ports
// above (anidb/catalog/scraper/usenet), and the Core.Register/Lookup
// cross-plugin capabilities in rewards.go / notify.go / etc. (RankGranter,
// ReleaseNotifier, ...) that one plugin publishes and another consumes. Both
// depend only on the interface. (The host's own agent Dispatcher stays in
// indexer-site/pkg/pluginapi because it is coupled to the site's request
// models.)
package pluginapi

import "context"

// AnimeMetadata is the DTO for one anime-catalog row crossing the port
// boundary. It mirrors the host's models.AnimeMetadata but carries only the
// fields the scraper reads or writes. When the anime_metadata table itself
// becomes plugin-owned (see JOBS-AS-PLUGINS.md § "anime_metadata ownership"),
// the full struct moves into the plugin and this DTO collapses into it.
type AnimeMetadata struct {
	AID        int
	TitleMain  string
	CoverLarge string
	// ... trimmed: only the projection the scraper touches is modeled here.
}

// NzbRow is the minimal projection of a host `nzbs` row the scanner matches on.
type NzbRow struct {
	ID    int64
	Title string
	// Groups are the newsgroups this release was seen in (the crosspost
	// list, or the single primary group), lowercase or not — the gate
	// folds case itself. It exists for anidbscraper's newsgroup gate,
	// which refuses to guess an anime by title for a release that arrived
	// in a group whose traffic is not anime (see anidbscraper/group_gate.go
	// for the production measurement behind it).
	//
	// OPTIONAL: a host whose UntaggedBatch leaves it empty reads as "no
	// newsgroup", which the gate always allows, so the scanner behaves
	// exactly as it did before the field existed. A host that DOES
	// configure the gate must populate this — the plugin cannot tell an
	// unpopulated projection from a genuine upload, so it warns once per
	// run when a configured gate saw no groups at all rather than
	// silently doing nothing.
	Groups []string
}

// SuggestionInput mirrors storage.NzbSuggestionInput for the auto-suggest job.
type SuggestionInput struct {
	NzbID   int64
	AnimeID int
	Score   float64
}

// AnimeCatalog is the narrow slice of the host's anime_metadata / anime_aliases
// store the scraper needs. It is deliberately carved from the host's 29-method
// AnimeRepository — the scraper uses ~13 — so the plugin's data surface stays
// honest and mockable. (CLEAN dependency: this catalog is effectively
// scraper-owned; it moves with the plugin if the tables do.)
type AnimeCatalog interface {
	AllTitles(ctx context.Context) (map[string]int, error)
	AllAliases(ctx context.Context) (map[string]int, error)
	Get(ctx context.Context, aid int) (*AnimeMetadata, error)
	Save(ctx context.Context, m *AnimeMetadata) error
	IDsNeedingMetadata(ctx context.Context, limit int) ([]int, error)
	AddAlias(ctx context.Context, aid int, alias string) error
}

// NzbTagSink is the write-back port into the host's core `nzbs` table — the
// deepest coupling in the whole extraction. The scraper reads untagged rows and
// writes anime_id / category / suggestions back onto them. A standalone plugin
// cannot OWN `nzbs` (the crawler, browse, search, agent-dispatch, and download
// paths all read/write it), so the host implements this port and injects it.
// (ENTANGLED dependency: this is the seam that must exist before the scraper
// can leave the host module.)
type NzbTagSink interface {
	// UntaggedBatch returns up to limit NZB rows with id > afterID that have no
	// anime_id yet, plus the cursor to resume from.
	UntaggedBatch(ctx context.Context, afterID int64, limit int) (rows []NzbRow, nextCursor int64, err error)
	SetAnimeID(ctx context.Context, nzbID int64, animeID int) error
	SetCategoryByAID(ctx context.Context, animeID int, category string) error
	CreateSuggestion(ctx context.Context, in SuggestionInput) error
}

// TitleMatcher is the shared normalized-title -> anime-ID matcher. Second
// entanglement: normalizeTitle / stripPunctuation / TitleMatcher live in
// pkg/services today and are shared by the manga, tvmaze, and torrent-feed
// matchers. Until that helper is promoted to a shared module (loon/catalog has
// a weaker DefaultNormalize; the anime one encodes season/sequel folding), the
// host injects its live matcher through this port and the scraper rebuilds its
// index after each titles refresh.
type TitleMatcher interface {
	Find(rawTitle string) (animeID int, ok bool)
	Rebuild(index map[string]int)
}

// ExactTitleMatcher is an OPTIONAL extension of TitleMatcher: the whole
// normalized title, present in the index, with none of the fuzzy steps
// (no season-suffix strip, no prefix walk, no substring containment).
//
// It exists for one caller — anidbscraper's newsgroup gate in its "exact"
// mode, which narrows what a release from a non-anime newsgroup may be
// tagged by instead of refusing it outright. A host that never sets that
// mode never needs to implement it; the plugin type-asserts for it and
// refuses to boot in "exact" mode without it, rather than quietly running
// the fuzzy matcher the mode exists to switch off.
//
// Implement it on the same type as TitleMatcher.
type ExactTitleMatcher interface {
	FindExact(rawTitle string) (animeID int, ok bool)
}

// CoverStore abstracts the host's web/static/covers/{aid}.jpg directory. Third
// entanglement (filesystem coupling): the host maps it to its static dir; a
// future object-store backend swaps here without touching the plugin.
type CoverStore interface {
	Has(aid int) bool
	Save(aid int, jpeg []byte) error
}
