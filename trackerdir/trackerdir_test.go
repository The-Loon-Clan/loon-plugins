package trackerdir

import (
	"sort"
	"strings"
	"testing"
)

// The invariants a regenerated file must keep. Deliberately structural rather
// than anchored to particular trackers: upstream adds and removes definitions
// weekly, and a test that breaks because a tracker died would train people to
// update it without reading the diff -- the opposite of what a review is for.

func TestTheEmbeddedDirectoryIsSubstantial(t *testing.T) {
	all := All()
	// Half the current 545. Below this something structural went wrong with a
	// regeneration -- a partial clone, a wrong path -- and the person running
	// it should look, not commit.
	if len(all) < 270 {
		t.Fatalf("only %d trackers; a regeneration went wrong", len(all))
	}
	if Origin().Commit == "" {
		t.Fatal("no source commit recorded; the provenance is the point of the header")
	}
}

func TestEverySlugIsUniqueAndResolvable(t *testing.T) {
	seen := map[string]bool{}
	for _, tr := range All() {
		if tr.Slug == "" {
			t.Fatalf("tracker %q has no slug", tr.Name)
		}
		if seen[tr.Slug] {
			t.Fatalf("duplicate slug %q -- BySlug would be ambiguous", tr.Slug)
		}
		seen[tr.Slug] = true
		if got, ok := BySlug(tr.Slug); !ok || got.Name != tr.Name {
			t.Fatalf("BySlug(%q) does not round-trip", tr.Slug)
		}
	}
}

func TestEveryTrackerIsWellFormed(t *testing.T) {
	types := map[string]bool{"public": true, "private": true, "semi-private": true}
	auths := map[string]bool{"none": true, "credentials": true, "cookie": true,
		"apikey": true, "passkey": true, "unknown": true}
	tvs := map[string]bool{"none": true, "title": true, "episode": true}
	musics := map[string]bool{"none": true, "title": true, "artist": true, "album": true}
	for _, tr := range All() {
		if len(tr.Domains) == 0 {
			t.Fatalf("%s: no domains -- unreachable by definition", tr.Slug)
		}
		if !types[tr.Type] {
			t.Fatalf("%s: unknown type %q", tr.Slug, tr.Type)
		}
		if !auths[tr.Auth] {
			t.Fatalf("%s: unknown auth %q", tr.Slug, tr.Auth)
		}
		if !tvs[tr.Search.TV] {
			t.Fatalf("%s: unknown tv precision %q", tr.Slug, tr.Search.TV)
		}
		if len(tr.Search.TVIDs) > 0 && tr.Search.TV == "none" {
			t.Fatalf("%s: id parameters on a tracker with no tv-search", tr.Slug)
		}
		if !musics[tr.Search.Music] {
			t.Fatalf("%s: unknown music precision %q", tr.Slug, tr.Search.Music)
		}
		if len(tr.Categories) == 0 {
			// Upstream, every current definition maps categories in one of the
			// two forms; a row with none means the generator failed to read one
			// of them, which is exactly the bug the first verification found.
			t.Fatalf("%s: no categories -- a generator regression, not a fact", tr.Slug)
		}
		if !sort.IntsAreSorted(tr.Categories) {
			t.Fatalf("%s: categories not sorted -- the file is meant to be deterministic", tr.Slug)
		}
		for _, c := range tr.Categories {
			if c < 1000 || c > 8999 {
				t.Fatalf("%s: category %d outside the Newznab range", tr.Slug, c)
			}
		}
		for _, k := range tr.Content {
			if !strings.ContainsAny(k, "abcdefghijklmnopqrstuvwxyz") {
				t.Fatalf("%s: content kind %q is not a name", tr.Slug, k)
			}
		}
	}
}

func TestContentAgreesWithCategories(t *testing.T) {
	names := map[int]string{1000: "console", 2000: "movies", 3000: "audio",
		4000: "pc", 5000: "tv", 6000: "xxx", 7000: "books", 8000: "other"}
	for _, tr := range All() {
		want := map[string]bool{}
		for _, c := range tr.Categories {
			want[names[c/1000*1000]] = true
			if c == 5070 {
				// Anime is the one facet derived from a subcategory rather
				// than a bucket -- the pipeline filters on tv/anime/movies.
				want["anime"] = true
			}
		}
		if len(want) != len(tr.Content) {
			t.Fatalf("%s: content %v does not match categories %v", tr.Slug, tr.Content, tr.Categories)
		}
		for _, k := range tr.Content {
			if !want[k] {
				t.Fatalf("%s: content claims %q, categories do not", tr.Slug, k)
			}
		}
	}
}

func TestEpisodeSearchableRanksUsefulnessFirst(t *testing.T) {
	eps := EpisodeSearchable()
	if len(eps) < 100 {
		t.Fatalf("only %d episode-searchable trackers; the TV pipeline would starve", len(eps))
	}
	for _, tr := range eps {
		if tr.Search.TV != "episode" {
			t.Fatalf("%s leaked into EpisodeSearchable with tv=%q", tr.Slug, tr.Search.TV)
		}
	}
	// The ranking bands: id-search before not, then no-auth before auth. One
	// inversion anywhere means the sort is decorative.
	band := func(tr Tracker) int {
		b := 0
		if len(tr.Search.TVIDs) == 0 {
			b += 2
		}
		if tr.Auth != "none" {
			b++
		}
		return b
	}
	for i := 1; i < len(eps); i++ {
		if band(eps[i-1]) > band(eps[i]) {
			t.Fatalf("ranking inverted at %d: %s (band %d) before %s (band %d)",
				i, eps[i-1].Slug, band(eps[i-1]), eps[i].Slug, band(eps[i]))
		}
	}
}

func TestUnattendedLoginBarriersAreCarried(t *testing.T) {
	captcha, twofa := 0, 0
	for _, tr := range All() {
		if tr.LoginCaptcha {
			captcha++
		}
		if tr.Login2FA {
			twofa++
		}
	}
	// Dozens of each exist upstream. Zero of either means the extraction
	// silently stopped reading them -- and every such tracker would then
	// claim to be wireable with credentials alone, which is the precise lie
	// this field exists to prevent.
	if captcha < 20 {
		t.Fatalf("only %d captcha-gated logins; extraction lost the field", captcha)
	}
	if twofa < 10 {
		t.Fatalf("only %d 2FA logins; extraction lost the field", twofa)
	}
}

func TestAnimeIsAFacetNotABucket(t *testing.T) {
	anime := Carrying("anime")
	if len(anime) < 100 {
		t.Fatalf("only %d anime carriers; the corpus says ~270", len(anime))
	}
	for _, tr := range anime {
		has5070 := false
		for _, c := range tr.Categories {
			has5070 = has5070 || c == 5070
		}
		if !has5070 {
			t.Fatalf("%s claims anime without category 5070", tr.Slug)
		}
	}
}

func TestCarryingFiltersByKind(t *testing.T) {
	tv := Carrying("tv")
	if len(tv) < 100 {
		t.Fatalf("only %d trackers carry tv; that contradicts the corpus", len(tv))
	}
	for _, tr := range tv {
		found := false
		for _, k := range tr.Content {
			found = found || k == "tv"
		}
		if !found {
			t.Fatalf("%s returned from Carrying(tv) without tv in content", tr.Slug)
		}
	}
	if got := Carrying("no-such-kind"); len(got) != 0 {
		t.Fatalf("an unknown kind matched %d trackers", len(got))
	}
}

func TestRecognizeMapsDomainsBack(t *testing.T) {
	// Use whatever the dataset holds rather than a hard-coded tracker: take
	// the first tracker with a legacy domain and prove both eras resolve.
	var probe Tracker
	for _, tr := range All() {
		if len(tr.LegacyDomains) > 0 {
			probe = tr
			break
		}
	}
	if probe.Slug == "" {
		t.Skip("no tracker with legacy domains in the dataset")
	}
	if got, ok := Recognize(probe.Domains[0] + "some/path?x=1"); !ok || got.Slug != probe.Slug {
		t.Fatalf("current domain of %s not recognized (got %q, ok=%v)", probe.Slug, got.Slug, ok)
	}
	if got, ok := Recognize(probe.LegacyDomains[0]); !ok || got.Slug != probe.Slug {
		t.Fatalf("legacy domain of %s not recognized", probe.Slug)
	}
	if _, ok := Recognize("https://definitely-not-a-tracker.example/"); ok {
		t.Fatal("an unknown domain was claimed by somebody")
	}
}

func TestAllReturnsACopy(t *testing.T) {
	a := All()
	a[0].Slug = "clobbered"
	if b := All(); b[0].Slug == "clobbered" {
		t.Fatal("All returns the backing slice; a caller's sort would corrupt the directory")
	}
}

func TestHostOfTolerantForms(t *testing.T) {
	cases := map[string]string{
		"https://1337x.to/":            "1337x.to",
		"http://user@host.example:80/": "host.example",
		"HTTPS://UPPER.example/path":   "upper.example",
		"bare.example":                 "bare.example",
		"":                             "",
	}
	for in, want := range cases {
		if got := hostOf(in); got != want {
			t.Fatalf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
}
