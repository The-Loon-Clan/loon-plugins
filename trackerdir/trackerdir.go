// Package trackerdir is the directory of external torrent trackers: who
// exists, what they carry, how precisely they can be asked, and what they
// demand before answering.
//
// WHY THIS EXISTS. The content pipeline's next step -- an episode aired and
// the index has nothing -- needs somewhere to go looking. Running Prowlarr for
// that means operating a second service to hold what is, for our purposes, a
// table. So the table is here instead: facts extracted from the Prowlarr
// community's ~545 Cardigann definitions by scripts/gen_trackerdir.py,
// reshaped into our own schema and checked in as directory.json.
//
// FACTS, NOT IMPLEMENTATIONS. The upstream definitions also carry scraping
// templates -- request paths, HTML selectors, login flows. None of that is
// taken: the upstream repo has no license, and while a domain list is a fact,
// a hand-written selector chain is somebody's expression. Our future search
// client speaks to trackers with clean interfaces rather than scraping, so
// the facts here are the entire requirement.
//
// A LIBRARY, NOT A PLUGIN. Nothing here touches a database, registers a view,
// or holds per-site state -- which tracker an operator ENABLES, and with what
// credentials, is site configuration and belongs to whatever wires search up
// later. A static dataset with accessors does not need a Provision.
//
// TWO CORPORA. The community YAML covers the long tail; Prowlarr's native
// C# definitions cover the majors that have no YAML at all -- BTN,
// AnimeBytes, HDBits, IPTorrents, MyAnonamouse and their peers, exactly the
// trackers an anime-heavy pipeline wants most. Both are read by the same
// generator; rows carry Origin so a refresh diff can be read per corpus.
//
// Refreshing: clone github.com/Prowlarr/Indexers, run the generator, review
// the diff. The JSON carries the source commit so a diff names exactly what
// upstream changed.
package trackerdir

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

//go:embed directory.json
var raw []byte

// Tracker is one external tracker's facts.
type Tracker struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Language    string `json:"language"`
	// Type is public, private or semi-private -- upstream's vocabulary,
	// preserved because the pipeline's "best public or private tracker"
	// decision is exactly this axis.
	Type string `json:"type"`
	// Origin is cardigann (the community YAML corpus) or native (Prowlarr's
	// C# majors). Recorded because the two are maintained differently
	// upstream and a refresh diff reads differently for each; it says
	// nothing about the tracker itself.
	Origin string `json:"origin"`
	// Domains are the tracker's current addresses, first one primary.
	Domains []string `json:"domains"`
	// LegacyDomains are dead or deprecated addresses. Kept for recognition --
	// mapping a URL somebody pasted back to a tracker -- never for requests.
	LegacyDomains []string `json:"legacy_domains"`
	// RequestDelaySeconds is the politeness the definition asks for. Zero
	// means unspecified, not "no limit": a client should still behave.
	RequestDelaySeconds float64 `json:"request_delay_seconds"`
	// Auth is what a member must supply before the tracker answers:
	// none, credentials, cookie, apikey, passkey, or unknown.
	Auth string `json:"auth"`
	// NeedsFlareSolverr marks trackers behind an anti-bot wall that a plain
	// HTTP client cannot pass. Recorded because it disqualifies a tracker
	// from any polite automated client, whatever its other virtues.
	NeedsFlareSolverr bool `json:"needs_flaresolverr"`
	// LoginCaptcha and Login2FA mark logins that need a human at the
	// keyboard -- an image to solve, a TOTP code to type. Either one means
	// "credentials on file" is NOT "wireable unattended", which is the
	// question this directory exists to answer; the first verification pass
	// found 45 captcha and 25 2FA trackers hiding inside plain
	// "credentials".
	LoginCaptcha bool `json:"login_captcha"`
	Login2FA     bool `json:"login_2fa"`
	// Content is the coarse kinds carried: tv, movies, audio, pc, xxx,
	// books, console, other -- plus anime, which is derived from category
	// 5070 rather than a thousands bucket because tv/anime/movies is the
	// pipeline's actual filter set and prod is anime-heavy. Kept separate
	// from Categories because "which trackers do TV" is the question the
	// pipeline asks first.
	Content []string `json:"content"`
	// Categories are standard Newznab ids, full precision. Our own taxonomy
	// is coarser; coarsen at the point of use, because a regenerated file
	// should not lose information our tree happens not to carry today.
	Categories []int  `json:"categories"`
	Search     Search `json:"search"`
}

// Search is how precisely a tracker can be asked.
type Search struct {
	FreeText bool `json:"free_text"`
	// TV is none, title, or episode. Episode means the query carries season
	// and episode numbers as parameters -- the difference between asking for
	// "Succession S02E04" and hoping, and asking for season 2 episode 4.
	TV string `json:"tv"`
	// TVIDs are the external-id parameters tv-search accepts (imdbid,
	// tvdbid, tmdbid, rid, doubanid). An id search skips title matching
	// entirely, which is the failure mode everything else fights.
	TVIDs    []string `json:"tv_ids"`
	Movie    bool     `json:"movie"`
	MovieIDs []string `json:"movie_ids"`
	// Music is none, title, artist or album -- the music analogue of TV,
	// kept as an enum from day one because collapsing it to a boolean now
	// means regenerating the world when the music phase asks.
	Music string `json:"music"`
	Book  bool   `json:"book"`
}

// Source records where the facts came from.
type Source struct {
	Repo   string `json:"repo"`
	Commit string `json:"commit"`
	Schema string `json:"schema"`
	Note   string `json:"note"`
}

type directory struct {
	Source   Source    `json:"source"`
	Trackers []Tracker `json:"trackers"`
}

var (
	once   sync.Once
	dir    directory
	bySlug map[string]int
)

// load parses the embedded JSON exactly once.
//
// A panic on malformed data is deliberate: the file is generated, reviewed
// and committed, so if it does not parse the build that embedded it is
// broken, and no caller has a sensible way to continue without the
// directory it imported this package for.
func load() {
	once.Do(func() {
		if err := json.Unmarshal(raw, &dir); err != nil {
			panic("trackerdir: embedded directory.json does not parse: " + err.Error())
		}
		bySlug = make(map[string]int, len(dir.Trackers))
		for i, t := range dir.Trackers {
			bySlug[t.Slug] = i
		}
	})
}

// Origin reports where the current dataset came from.
func Origin() Source {
	load()
	return dir.Source
}

// All returns every tracker, sorted by slug. The slice is a copy; callers
// can reorder it freely.
func All() []Tracker {
	load()
	out := make([]Tracker, len(dir.Trackers))
	copy(out, dir.Trackers)
	return out
}

// BySlug returns one tracker.
func BySlug(slug string) (Tracker, bool) {
	load()
	i, ok := bySlug[slug]
	if !ok {
		return Tracker{}, false
	}
	return dir.Trackers[i], true
}

// Carrying returns the trackers whose content includes the given coarse kind
// ("tv", "movies", ...), sorted by slug.
func Carrying(kind string) []Tracker {
	load()
	var out []Tracker
	for _, t := range dir.Trackers {
		for _, c := range t.Content {
			if c == kind {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

// EpisodeSearchable returns the trackers that can be asked for a specific
// season and episode -- the ones the gap-filling step can use at all.
// Sorted best-first: an external-id search beats a title search, a public
// tracker needs no wiring before it is useful, and within a band the slug
// keeps the order stable.
func EpisodeSearchable() []Tracker {
	load()
	var out []Tracker
	for _, t := range dir.Trackers {
		if t.Search.TV == "episode" {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (len(a.Search.TVIDs) > 0) != (len(b.Search.TVIDs) > 0) {
			return len(a.Search.TVIDs) > 0
		}
		if (a.Auth == "none") != (b.Auth == "none") {
			return a.Auth == "none"
		}
		return a.Slug < b.Slug
	})
	return out
}

// Recognize maps a URL back to the tracker it belongs to, current or legacy
// domains alike. The match is by host suffix so a path or a subdomain does
// not defeat it. Empty slug means nobody claims it.
func Recognize(rawURL string) (Tracker, bool) {
	load()
	host := hostOf(rawURL)
	if host == "" {
		return Tracker{}, false
	}
	for _, t := range dir.Trackers {
		for _, set := range [][]string{t.Domains, t.LegacyDomains} {
			for _, d := range set {
				if h := hostOf(d); h != "" && (host == h || strings.HasSuffix(host, "."+h)) {
					return t, true
				}
			}
		}
	}
	return Tracker{}, false
}

// hostOf is the lowercased host of a URL-ish string, tolerant of the forms
// the dataset and pasted links actually take.
func hostOf(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	for _, cut := range []string{"/", "?", "#"} {
		if i := strings.Index(s, cut); i >= 0 {
			s = s[:i]
		}
	}
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	return s
}
