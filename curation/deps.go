// Package curation fills the season/episode columns crawled releases arrive
// without, and surfaces what it cannot infer.
//
// Every release the crawler indexes lands with season_num/episode_num NULL —
// the assembler never writes them by design (subject lines are the only
// evidence it has). The site's daily title cleaner backfills what the title
// itself says, but a large share of anime titles carry no season marker at
// all: fansub groups drop "S1" for a first season as convention. This plugin
// closes that gap with the metadata the site already holds — the AniDB entry
// the release is tagged to (AniDB names each season as its own entry) and the
// TMDB season structure cached in completion_buckets — and it applies every
// rule automatically: an inference the rules trust is written, only the
// genuinely uninferable remains, and that remainder is the fail-to-parse
// report on the admin page.
//
// It is a host-data worker in the feeds mould: nzbs, anime_metadata and
// completion_buckets are host tables read by host pages, so the plugin takes
// narrow function seams rather than owning a store, and the season/episode
// PARSER stays host-owned too — models.ParseSeasonEpisode is the one
// canonical title parser and this plugin calls it through a seam rather than
// growing a second copy that would drift.
package curation

import "context"

// Release is the slice of an nzbs row the sweep needs.
type Release struct {
	ID      int64
	Title   string
	AnimeID int
}

// AnimeFacts is what the site knows about one AniDB entry that bears on
// season inference. Zero values mean "unknown", and the rules treat unknown
// conservatively — they refuse to default rather than guess.
type AnimeFacts struct {
	Title       string // anime_metadata.title (AniDB main title)
	RomajiTitle string
	Format      string // AniList format: TV, MOVIE, OVA, ONA, SPECIAL; "" unknown
	Type        string // AniDB type: "TV Series", "Movie", "OVA", "Web", ...; "" unknown
	// TMDBSeasons counts the TMDB-SOURCED season buckets for this entry
	// (completion_buckets, bucket_kind='season', source='tmdb', numbered).
	// The source filter is load-bearing: the AniList fallback writes exactly
	// one bucket for EVERY entry it touches, so counting it would make every
	// unlinked sequel look like a single-season show.
	TMDBSeasons int
	// MappedSeason is which season of its mapped show this entry IS,
	// per the community anidb→tvdb/tmdb mapping (Fribb anime-lists).
	// 0 = unmapped. Authoritative when present: AniDB entries are
	// season-scoped and the mapping exists precisely to answer this.
	MappedSeason int
}

// Stats are the live numbers the admin page leads with.
type Stats struct {
	AnimeCompleted int64 // completed releases tagged to an anime
	SeasonNull     int64 // of those, season_num still NULL
	EpisodeNull    int64 // of those, episode_num still NULL
}

// Deps are the host seams. All fields are required; Provision refuses to boot
// on a partial wiring (a half-wired sweep would silently skip rules).
type Deps struct {
	// ListSeasonNull pages the sweep worklist by ascending id: completed,
	// anime-tagged releases whose season_num is NULL.
	ListSeasonNull func(ctx context.Context, afterID int64, limit int) ([]Release, error)
	// PageSeasonNull is the admin-page view of the same set, newest first,
	// with a total for pagination.
	PageSeasonNull func(ctx context.Context, limit, offset int) ([]Release, int, error)
	// SetSeasonEpisode fills season_num/episode_num COALESCE-when-NULL —
	// a manual edit always wins over an inference that arrives later.
	SetSeasonEpisode func(ctx context.Context, id int64, season, episode *int) error
	// AnimeFacts returns (nil, nil) when the aid has no metadata row.
	AnimeFacts func(ctx context.Context, aid int) (*AnimeFacts, error)
	// ParseSeasonEpisode is the host's canonical title parser
	// (models.ParseSeasonEpisode). Either return may be nil.
	ParseSeasonEpisode func(title string) (season, episode *int)
	// Stats backs the admin page header.
	Stats func(ctx context.Context) (Stats, error)
}

var deps *Deps

// SetDeps stages the host seams. Call before core.Boot in every process that
// runs the plugin (web for the page, worker for the sweep).
func SetDeps(d Deps) { deps = &d }

func (d *Deps) ok() bool {
	return d != nil &&
		d.ListSeasonNull != nil && d.PageSeasonNull != nil &&
		d.SetSeasonEpisode != nil && d.AnimeFacts != nil &&
		d.ParseSeasonEpisode != nil && d.Stats != nil
}
