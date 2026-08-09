package feeds

import (
	"context"
	"strings"
	"testing"
)

func TestLooksOldByYear(t *testing.T) {
	const currentYear = 2026
	cases := []struct {
		title string
		old   bool
		year  int
	}{
		{"[Group] Show - 05 (1080p)", false, 0},              // no year stamp → default-include
		{"[Group] Show (2026) - 05", false, 2026},            // current year
		{"[Group] Show (2025) - 05", false, 2025},            // last year is still fresh
		{"[Group] Show (2024) BDRip", true, 2024},            // two years back → old
		{"[Group] Show (1999) Complete", true, 1999},         //
		{"Show 2160p HEVC", false, 0},                        // resolution digits are not a year (no word boundary)
		{"Show 1080p", false, 0},                             //
		{"[Group] Show (2003) [2026 remaster]", false, 2026}, // MAX year wins — a remaster of an old title is fresh
	}
	for _, c := range cases {
		old, year := looksOldByYear(c.title, currentYear)
		if old != c.old || year != c.year {
			t.Errorf("looksOldByYear(%q) = (%v, %d), want (%v, %d)", c.title, old, year, c.old, c.year)
		}
	}
}

func TestDecideItem(t *testing.T) {
	const currentYear = 2026
	existing := map[string]bool{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": true,
		"[group] known title - 01":                 true,
	}

	cases := []struct {
		name string
		it   item
		want string
	}{
		{"clean item proceeds", item{title: "[Group] New Show - 01"}, StatusObserved},
		{"old year wins over dup — status attribution matches the original service",
			item{title: "[Group] Known Title - 01 (2020)", infoHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, StatusSkippedOld},
		{"hash dedup", item{title: "[Group] Renamed Upload", infoHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, StatusSkippedDupHash},
		{"title dedup is case-insensitive", item{title: "[Group] KNOWN Title - 01"}, StatusSkippedDupTitle},
		{"empty hash never matches the set", item{title: "[Group] Fresh - 02", infoHash: ""}, StatusObserved},
	}
	for _, c := range cases {
		if got := decideItem(c.it, existing, currentYear); got != c.want {
			t.Errorf("%s: decideItem = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSortItemsAiringFirstThenSeeders(t *testing.T) {
	items := []item{
		{title: "cold-100", seeders: 100},
		{title: "airing-5", seeders: 5, airing: true},
		{title: "cold-200", seeders: 200},
		{title: "airing-50", seeders: 50, airing: true},
	}
	sortItems(items)
	got := []string{items[0].title, items[1].title, items[2].title, items[3].title}
	want := []string{"airing-50", "airing-5", "cold-200", "cold-100"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v (airing first, then seeders desc)", got, want)
		}
	}
}

func TestExtractHashFromMagnet(t *testing.T) {
	cases := map[string]string{
		"magnet:?xt=urn:btih:ABCDEF1234567890ABCDEF1234567890ABCDEF12&dn=x": "abcdef1234567890abcdef1234567890abcdef12",
		"magnet:?xt=urn:BTIH:abc123":                                        "abc123", // case-insensitive marker, truncates at end
		"https://example.com/no-magnet-here":                                "",
	}
	for in, want := range cases {
		if got := extractHashFromMagnet(in); got != want {
			t.Errorf("extractHashFromMagnet(%q) = %q, want %q", in, got, want)
		}
	}
}

// normStub stands in for the host's normalizeTitle: both sides of every
// comparison go through the same function, so lowercasing is enough to
// exercise the matching logic.
func normStub(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func TestBuildAiringIndexDedupsAndDropsEmpty(t *testing.T) {
	p := &Plugin{deps: JobDeps{
		NormalizeTitle: normStub,
		AiringEntries: func() []AiringEntry {
			return []AiringEntry{
				{AnilistID: 1, AID: 10, Title: "Show One"},
				{AnilistID: 1, AID: 10, Title: "Show One (dup)"},
				{AnilistID: 2, AID: 0, Title: "   "},
				{AnilistID: 3, AID: 30, Title: "Show Three"},
			}
		},
	}}
	idx := p.buildAiringIndex()
	if len(idx) != 2 {
		t.Fatalf("index has %d entries, want 2 (AniList dup + empty title dropped): %+v", len(idx), idx)
	}
	if idx[0].title != "show one" || idx[1].title != "show three" {
		t.Errorf("index titles not normalized: %+v", idx)
	}
}

func TestIsAiring(t *testing.T) {
	index := []airingInfo{{anilistID: 1, aid: 10, title: "show one"}}
	if !isAiring(item{title: "[Subs] Show One - 05 (1080p)"}, index, normStub) {
		t.Error("substring containment should match the airing index")
	}
	if isAiring(item{title: "[Subs] Unrelated - 05"}, index, normStub) {
		t.Error("unrelated title matched")
	}
	if isAiring(item{title: "[Subs] Show One - 05"}, nil, normStub) {
		t.Error("empty index must never match")
	}
}

// baseDeps returns a JobDeps where every lookup misses — tests override the
// fields they exercise.
func baseDeps() JobDeps {
	miss := func(ctx context.Context, id int) (*AnimeRef, error) { return nil, nil }
	return JobDeps{
		NormalizeTitle:   normStub,
		BlockedExtension: func(string) bool { return false },
		AnimeByID:        miss,
		AnimeByAnilistID: miss,
		AnimeByTvdbID:    miss,
		AnimeByTmdbID:    miss,
	}
}

func TestBuildRequestRefusesBlockedExtensions(t *testing.T) {
	deps := baseDeps()
	deps.BlockedExtension = func(name string) bool { return strings.HasSuffix(name, ".iso") }
	p := &Plugin{deps: deps}
	if req := p.buildRequest(context.Background(), item{title: "Some.Movie.iso"}, nil); req != nil {
		t.Error("a blocked-extension title produced a request — the agent would fetch, delete, then fail prepare")
	}
	if req := p.buildRequest(context.Background(), item{title: "   "}, nil); req != nil {
		t.Error("a blank title produced a request")
	}
}

func TestBuildRequestParsesTitleMetadata(t *testing.T) {
	p := &Plugin{deps: baseDeps(), botUserID: 42}
	it := item{
		title:   "[Subs] Great Show S02E04 1080p WEB-DL",
		link:    "https://nyaa.si/view/123",
		source:  "nyaa",
		seeders: 77,
		size:    1 << 30,
	}
	req := p.buildRequest(context.Background(), it, nil)
	if req == nil {
		t.Fatal("no request built")
	}
	if req.Resolution != "1080p" || req.Source != "WEB-DL" || req.Season != "02" || req.Episodes != "04" {
		t.Errorf("title metadata wrong: %+v", req)
	}
	if req.UserID != 42 || req.Username != "nyaa-bot" {
		t.Errorf("bot identity wrong: UserID=%d Username=%q", req.UserID, req.Username)
	}
	if req.SizeBytes == nil || *req.SizeBytes != 1<<30 {
		t.Errorf("SizeBytes not carried: %v", req.SizeBytes)
	}
	if !strings.Contains(req.Notes, "Auto-imported from nyaa") {
		t.Errorf("notes missing provenance: %q", req.Notes)
	}
	if !strings.Contains(req.Notes, "Size: 1.00 GiB") {
		t.Errorf("notes missing derived size: %q", req.Notes)
	}
}

func TestBuildRequestResolvesAnimeFromAiringIndex(t *testing.T) {
	mal, ani := 111, 222
	deps := baseDeps()
	deps.AnimeByID = func(ctx context.Context, aid int) (*AnimeRef, error) {
		if aid != 10 {
			t.Errorf("looked up aid %d, want 10", aid)
		}
		return &AnimeRef{AID: 10, MalID: &mal, AnilistID: &ani}, nil
	}
	p := &Plugin{deps: deps}
	index := []airingInfo{{anilistID: 2, aid: 10, title: "great show"}}

	req := p.buildRequest(context.Background(), item{title: "[Subs] Great Show - 04", airing: true}, index)
	if req == nil {
		t.Fatal("no request built")
	}
	if req.AnimeID == nil || *req.AnimeID != 10 {
		t.Fatalf("AnimeID = %v, want 10", req.AnimeID)
	}
	if req.MalID != &mal || req.AnilistID != &ani {
		t.Errorf("MAL/AniList ids not carried through")
	}
	if !strings.Contains(req.Notes, "Currently airing") {
		t.Errorf("airing note missing: %q", req.Notes)
	}
}

func TestBuildRequestFallsBackToTvdbLookup(t *testing.T) {
	deps := baseDeps()
	deps.AnimeByTvdbID = func(ctx context.Context, id int) (*AnimeRef, error) {
		if id != 123456 {
			t.Errorf("tvdb lookup got %d", id)
		}
		return &AnimeRef{AID: 55}, nil
	}
	p := &Plugin{deps: deps}
	req := p.buildRequest(context.Background(), item{title: "Neko Show S01E01", tvdbID: 123456, tmdbID: 654321}, nil)
	if req == nil {
		t.Fatal("no request built")
	}
	if req.AnimeID == nil || *req.AnimeID != 55 {
		t.Fatalf("AnimeID = %v, want 55 via TVDB", req.AnimeID)
	}
	if req.TvdbID == nil || *req.TvdbID != 123456 || req.TmdbID == nil || *req.TmdbID != 654321 {
		t.Errorf("external ids not persisted: tvdb=%v tmdb=%v", req.TvdbID, req.TmdbID)
	}
}

func TestPickFailedSearches(t *testing.T) {
	failed := []FailedSearch{
		{Query: "popular one", Count: 50},
		{Query: "", Count: 40},         // empty query dropped
		{Query: "rare typo", Count: 2}, // below min count
		{Query: "popular two", Count: 9},
		{Query: "popular three", Count: 8},
	}
	picked := pickFailedSearches(failed, 5, 2)
	if len(picked) != 2 {
		t.Fatalf("picked %d, want 2 (batch cap)", len(picked))
	}
	if picked[0].Query != "popular one" || picked[1].Query != "popular two" {
		t.Errorf("order not preserved: %+v", picked)
	}
}

func TestGroupArchiveKey(t *testing.T) {
	cases := []struct {
		source, link, want string
	}{
		{"nyaa", "https://nyaa.si/view/1900001", "nyaa-1900001"},
		{"nyaa", "https://nyaa.si/download/1900001.torrent", "nyaa-1900001"},
		{"nekobt", "https://nekobt.to/torrents/9001/anything", "9001"},
		{"nekobt", "https://nekobt.to/somewhere-else", ""},
		{"tokyotosho", "https://www.tokyotosho.info/details.php?id=42", ""},
	}
	for _, c := range cases {
		if got := groupArchiveKey(c.source, c.link); got != c.want {
			t.Errorf("groupArchiveKey(%q, %q) = %q, want %q", c.source, c.link, got, c.want)
		}
	}
}

func TestCrossWriteToGroupArchive(t *testing.T) {
	var wrote []GroupTorrent
	deps := baseDeps()
	deps.SlugifyGroup = func(name string) string { return strings.ToLower(strings.ReplaceAll(name, "-", "")) }
	deps.GroupIDBySlug = func(ctx context.Context, slug string) (int, bool, error) {
		if slug == "subsplease" {
			return 7, true, nil
		}
		return 0, false, nil
	}
	deps.UpsertGroupTorrent = func(ctx context.Context, gt GroupTorrent) error {
		wrote = append(wrote, gt)
		return nil
	}
	p := &Plugin{deps: deps}
	ctx := context.Background()

	// Happy path.
	p.crossWriteToGroupArchive(ctx, item{
		title:    "[SubsPlease] Show - 05 (1080p)",
		infoHash: "AAAA0000AAAA0000AAAA0000AAAA0000AAAA0000",
		link:     "https://nyaa.si/view/42",
		source:   "nyaa",
		size:     999,
		seeders:  12,
	})
	if len(wrote) != 1 {
		t.Fatalf("wrote %d rows, want 1", len(wrote))
	}
	got := wrote[0]
	if got.ExternalID != "nyaa-42" || got.GroupID != 7 {
		t.Errorf("keying wrong: %+v", got)
	}
	if got.InfoHash != "aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000" {
		t.Errorf("hash not lowercased: %q", got.InfoHash)
	}

	// Every skip path must write nothing.
	skips := []item{
		{title: "[SubsPlease] No Hash", link: "https://nyaa.si/view/43", source: "nyaa"},
		{title: "No Group Prefix", infoHash: "bbbb0000bbbb0000bbbb0000bbbb0000bbbb0000", link: "https://nyaa.si/view/44", source: "nyaa"},
		{title: "[UnknownGroup] Item", infoHash: "cccc0000cccc0000cccc0000cccc0000cccc0000", link: "https://nyaa.si/view/45", source: "nyaa"},
		{title: "[SubsPlease] Tosho Item", infoHash: "dddd0000dddd0000dddd0000dddd0000dddd0000", link: "https://www.tokyotosho.info/details.php?id=9", source: "tokyotosho"},
	}
	for _, it := range skips {
		p.crossWriteToGroupArchive(ctx, it)
	}
	if len(wrote) != 1 {
		t.Errorf("a skip path wrote a row: %+v", wrote[1:])
	}
}
