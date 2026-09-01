package anidbscraper

import "github.com/the-loon-clan/loon-plugins/pluginapi"

// Deps are the host-owned collaborators the scraper needs injected before boot.
// The host builds thin adapters over its existing repositories and calls
// SetDeps in the worker block of cmd/main.go, BEFORE core.Boot — exactly the
// pattern plugins/offers already uses (offers.SetJobDeps).
//
// Everything here is either a CLEAN dependency the plugin would ideally own
// (Catalog) or an ENTANGLED one that must stay host-side (Nzbs, Covers, Matcher
// — see pluginapi/anidb.go and JOBS-AS-PLUGINS.md for why).
type Deps struct {
	// Catalog is the anime_metadata / anime_aliases store (scraper-owned data).
	Catalog pluginapi.AnimeCatalog
	// Nzbs is the write-back port into the host `nzbs` table (the deep seam).
	Nzbs pluginapi.NzbTagSink
	// Matcher is the shared title matcher, rebuilt after each titles refresh.
	Matcher pluginapi.TitleMatcher
	// Covers maps to web/static/covers/{aid}.jpg.
	Covers pluginapi.CoverStore

	// AllowTitleGuess is the OPTIONAL jurisdiction test for the newsgroup
	// gate (group_gate.go): given an untagged row, may the scanner guess
	// its anime from the title at all?
	//
	// nil — the default, and what every existing host gets — means the gate
	// falls back to the plugins.anidbscraper.group_allowlist patterns, which
	// ship EMPTY, so an unconfigured host's scanner behaves exactly as it did
	// before the gate existed.
	//
	// Wire it when "is this release in scope" is not a question about
	// newsgroups on your site: a tracker category, a source flag, an origin
	// column. It REPLACES the allowlist (setting both is a Provision error,
	// because the plugin would have to ignore one); group_gate_mode still
	// decides what an out-of-scope row may be tagged by.
	AllowTitleGuess func(row pluginapi.NzbRow) bool
}

// deps is package-scoped because RegisterPlugin captures a zero-value factory at
// init() time; the host fills the collaborators in later, before Boot. Mirrors
// offers.deps / offers.jobDeps.
var deps *Deps

// SetDeps stages the host collaborators. Call exactly once, in the worker
// process, before core.Boot. A nil field is caught in Provision (fail-fast)
// rather than nil-panicking mid-scan.
func SetDeps(d Deps) { deps = &d }
