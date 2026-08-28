package offers

import (
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// These fragments are LIFTED markup — 600 lines nobody in this package wrote —
// so executing them is the only thing that proves the view types match what
// they read. html/template streams: a field the markup wants and the data
// lacks aborts the render part way through, and the caller sees a 200 with
// half a page and nothing logged.

func ts(s string) *time.Time { t, _ := time.Parse(time.RFC3339, s); return &t }

func sampleBucket() Bucket {
	one := 1
	return Bucket{
		BucketID: 5, EntityType: EntityAnime, EntityID: &one,
		SeasonNum: &one, EpisodeNum: &one,
		Resolution: "1080p", SourceTag: "web-dl", SizeBucket: "<1GB",
		OfferCount: 3, ActiveOfferCount: 2, MinPoints: 0,
		HasPrivate: true, HasPublic: false, HasPersonal: true,
	}
}

func sampleTracker() Tracker {
	return Tracker{
		ID: 1, Name: "AnimeBytes", ShortName: "ab",
		Visibility: VisibilityPrivate, Status: StatusActive,
		RulesMarkdown: "be nice", ScrapeMinSeconds: 30,
		OfferCount: 12, AccessCount: 4,
	}
}

// render executes a fragment directly, bypassing the chrome seam so the test
// needs no host.
func render(t *testing.T, name string, data gin.H) string {
	t.Helper()
	data["CSRFToken"] = "test-csrf"
	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, name, data); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	s := sb.String()
	if strings.TrimSpace(s) == "" {
		t.Fatalf("%s rendered empty", name)
	}
	// Body content only — the host wrapper owns the document.
	for _, unwanted := range []string{"<!DOCTYPE", "<html", `template "navbar"`, `template "footer"`} {
		if strings.Contains(s, unwanted) {
			t.Errorf("%s carries host chrome it should not: %q", name, unwanted)
		}
	}
	return s
}

// listingData is the offers.html handler contract: every key OffersPage
// always passes, overridable per test. The pager is host-rendered HTML now
// (deps.RenderPagination), handed over pre-built like the requests feed's.
func listingData(extra gin.H) gin.H {
	base := gin.H{
		"EntityType": EntityAnime, "SizeBucket": "", "Kind": "", "Query": "", "Tab": "open",
		"Groups": []BucketGroup{}, "FulfilledTotal": 0,
		"Total": 0, "Pagination": template.HTML(`<nav id="pg"></nav>`),
	}
	for k, v := range extra {
		base[k] = v
	}
	return base
}

// groupOf wraps buckets in one show-group, the listing's unit.
func groupOf(title string, buckets ...Bucket) []BucketGroup {
	g := BucketGroup{EntityType: EntityAnime, EntityID: 1, Title: title, Buckets: buckets}
	for _, b := range buckets {
		g.TotalFiles += b.FileCount
	}
	return []BucketGroup{g}
}

func TestOffersPageRenders(t *testing.T) {
	a := sampleBucket()
	a.FileCount, a.SampleFile = 14, "[VCB-Studio] Yamada-kun [01][Ma10p_1080p].mkv"
	b := sampleBucket()
	b.BucketID, b.Resolution, b.FileCount, b.SampleFile = 6, "", 6, "[VCB-Studio] Yamada-kun [Menu01].png"
	got := render(t, "offers.html", listingData(gin.H{
		"Groups": groupOf("Yamada and the Seven Witches", a, b),
		"Total":  1,
	}))
	// One card per SHOW; the variant rows carry the sample filename — the
	// identity a requester reads when the parsed tags say nothing. And the
	// community sidebar stays gone (the leaderboard lives on /stats now).
	for _, want := range []string{
		"Yamada and the Seven Witches", "2 variants", "20 files",
		"[VCB-Studio] Yamada-kun [01][Ma10p_1080p].mkv",
		"[VCB-Studio] Yamada-kun [Menu01].png",
		"1080p", "account-settings#offers", "/stats",
		`<nav id="pg">`, // the host-rendered pager must be placed
	} {
		if !strings.Contains(got, want) {
			t.Errorf("offers page missing %q", want)
		}
	}
	if got := strings.Count(got, "Yamada and the Seven Witches"); got != 1 {
		t.Errorf("the show's title renders %d times — variants must group under ONE card", got)
	}
	// Every show renders collapsed — including single-variant ones, by the
	// operator's call: the page is a list of shows, uniformly. The
	// disclosure is a native <details>, so it works scriptless.
	if !strings.Contains(got, "<details") || strings.Contains(got, "<details class=\"card mb-2 of-group\" open") {
		t.Error("a multi-variant show should render as a collapsed <details>")
	}

	single := render(t, "offers.html", listingData(gin.H{
		"Groups": groupOf("One Variant Show", a), "Total": 1,
	}))
	if strings.Contains(single, "of-group\" open") || strings.Contains(single, "<details open") {
		t.Error("a single-variant show must render collapsed like every other card")
	}
	for _, gone := range []string{"Top deliverers", "Recent fulfillments", "Active trackers"} {
		if strings.Contains(got, gone) {
			t.Errorf("offers page still renders the removed %q panel", gone)
		}
	}
}

// The availability filter is part of the form contract: both kinds are
// offered, and the active choice comes back selected so Apply does not
// silently reset it.
func TestOffersPageKindFilter(t *testing.T) {
	got := render(t, "offers.html", listingData(gin.H{"Kind": "can_get"}))
	if !strings.Contains(got, `name="kind"`) {
		t.Fatal("offers page has no kind select")
	}
	if !strings.Contains(got, `value="can_get" selected`) {
		t.Error("active kind filter does not render selected")
	}
	if !strings.Contains(got, `value="have"`) {
		t.Error("kind select is missing the 'have' option")
	}
}

func TestAdminOffersPageRenders(t *testing.T) {
	name := "AnimeBytes"
	claimer := "shigure"
	got := render(t, "admin_offers.html", gin.H{
		"PageTitle": "Offers", "ActiveNav": "admin",
		"Recent": []AdminRequest{{
			RequestID: 7, BucketID: 5, Status: "claimed", PointsOffered: 0,
			CreatedAt: time.Now(), ClaimExpiresAt: ts("2026-08-06T12:15:00Z"),
			RequesterUserID: 1, RequesterUsername: "kirisame",
			ClaimerUsername: &claimer, TrackerName: &name,
		}},
		"StatusCounts": []StatusCount{{Status: "open", Count: 4}},
		"Leaders":      []Leader{{UserID: 1, Username: "kirisame", FulfilledCount: 7}},
		"Trackers":     []TrackerStat{{TrackerID: 1, TrackerName: name, OfferCount: 12}},
	})
	for _, want := range []string{"kirisame", "shigure", "claimed"} {
		if !strings.Contains(got, want) {
			t.Errorf("admin offers page missing %q", want)
		}
	}
}

// admin_trackers exercises the self-defined admin_tracker_form partial and the
// dict helper, which is the one FuncMap function this package had to
// reimplement.
func TestAdminTrackersPageRenders(t *testing.T) {
	got := render(t, "admin_trackers.html", gin.H{
		"PageTitle": "Trackers", "ActiveNav": "admin",
		"Trackers": []Tracker{sampleTracker()},
	})
	for _, want := range []string{"AnimeBytes", "test-csrf", VisibilityPrivate} {
		if !strings.Contains(got, want) {
			t.Errorf("admin trackers page missing %q", want)
		}
	}
}

// The detail page's action area follows the request lifecycle, so every state
// renders: delivered-and-broken (release link + re-request), live (join),
// and never-requested (plain request). html/template streams — a field the
// markup wants and the data lacks aborts the render part way with a 200.
func TestOfferDetailPageRendersEveryRequestState(t *testing.T) {
	b := sampleBucket()
	nzbID := int64(176112514)

	delivered := render(t, "offer_detail.html", gin.H{
		"Bucket": &b, "Files": []OfferedFile{},
		"Request": &RequestState{
			RequestID: 1, Status: "delivered", NzbID: &nzbID,
			DeliveredAt: ts("2026-08-16T21:07:00Z"), PoolPoints: 1000, BackerCount: 1,
		},
		"Backers":     []RequestBacker{{UserID: 2, Username: "kirisame", Points: 1000}},
		"RequestLive": false, "Delivered": true,
		"Health": "dead", "CanReRequest": true,
	})
	for _, want := range []string{"/release/176112514", "Request a fresh copy", "dead"} {
		if !strings.Contains(delivered, want) {
			t.Errorf("delivered state missing %q", want)
		}
	}

	live := render(t, "offer_detail.html", gin.H{
		"Bucket": &b, "Files": []OfferedFile{},
		"Request": &RequestState{
			RequestID: 2, Status: "open", PoolPoints: 250, BackerCount: 2,
		},
		"Backers": []RequestBacker{
			{UserID: 2, Username: "kirisame", Points: 250},
			{UserID: 3, Username: "shigure", Points: 0},
		},
		"RequestLive": true, "Delivered": false,
		"Health": "", "CanReRequest": false,
	})
	for _, want := range []string{"Join this request", "250 pts staked", "kirisame"} {
		if !strings.Contains(live, want) {
			t.Errorf("live state missing %q", want)
		}
	}

	fresh := render(t, "offer_detail.html", gin.H{
		"Bucket": &b, "Files": []OfferedFile{},
		"Request": (*RequestState)(nil), "Backers": []RequestBacker{},
		"RequestLive": false, "Delivered": false,
		"Health": "", "CanReRequest": false,
	})
	if !strings.Contains(fresh, "Request this") {
		t.Error("fresh state missing the plain request button")
	}
}

// File scoping (site migration 321): a requestable multi-file offer renders
// pick boxes; a live request renders its scope and NO pick boxes, because a
// joiner backs the request as-is.
func TestOfferDetailFilePicking(t *testing.T) {
	b := sampleBucket()
	files := []OfferedFile{
		{Name: "One Piece - 0783.mkv", SizeBytes: 900 << 20},
		{Name: "One Piece - 0784.mkv", SizeBytes: 900 << 20},
	}

	fresh := render(t, "offer_detail.html", gin.H{
		"Bucket": &b, "Files": files,
		"Request": (*RequestState)(nil), "Backers": []RequestBacker{},
		"RequestLive": false, "Delivered": false,
		"Health": "", "CanReRequest": false,
	})
	if strings.Count(fresh, `class="js-of-file-pick"`) != 2 {
		t.Error("a requestable multi-file offer should render one pick box per file")
	}
	if !strings.Contains(fresh, "Tick files to request only part of this offer") {
		t.Error("the picker hint is missing")
	}

	live := render(t, "offer_detail.html", gin.H{
		"Bucket": &b, "Files": files,
		"Request": &RequestState{
			RequestID: 2, Status: "open", PoolPoints: 0, BackerCount: 1,
			FileFilter: []string{"One Piece - 0783.mkv"},
		},
		"Backers":     []RequestBacker{{UserID: 2, Username: "kirisame"}},
		"RequestLive": true, "Delivered": false,
		"Health": "", "CanReRequest": false,
	})
	if !strings.Contains(live, "Scoped to 1 of the offered files") {
		t.Error("a scoped live request should show its file scope")
	}
	if strings.Contains(live, `class="of-file-pick"`) {
		t.Error("a live request must not offer pick boxes — joiners back it as-is")
	}
}

// The fulfilled tab: delivered buckets leave the open list and link their
// release; the tab strip carries the count; and the open tab, searched empty,
// says so against the query.
func TestOffersFulfilledTabRenders(t *testing.T) {
	b := sampleBucket()
	nzbID := int64(176112514)
	b.Fulfilled, b.DeliveredNzbID = true, &nzbID

	got := render(t, "offers.html", listingData(gin.H{
		"Tab": "fulfilled", "Groups": groupOf("Spirited Away", b),
		"FulfilledTotal": 1, "Total": 1,
	}))
	for _, want := range []string{"Spirited Away", "/release/176112514", "view the release", `tab-count">1`} {
		if !strings.Contains(got, want) {
			t.Errorf("fulfilled tab missing %q", want)
		}
	}
	if strings.Contains(got, "data-bucket-id") {
		t.Error("fulfilled tab renders a Request button — the bytes are already on Usenet")
	}

	open := render(t, "offers.html", listingData(gin.H{"Query": "frieren"}))
	if !strings.Contains(open, "frieren") {
		t.Error("empty search result does not echo the query")
	}
}

// The listing's demand column: the staked pool renders beside the count. A
// 1000-point pool showing as "free" is the confusion this pins against.
func TestOffersListingShowsTheStakedPool(t *testing.T) {
	b := sampleBucket()
	b.BackerCount = 2
	b.PoolPoints = 1000
	got := render(t, "offers.html", listingData(gin.H{"Groups": groupOf("Lady Lady!!", b), "Total": 1}))
	if !strings.Contains(got, "1000 pts staked") {
		t.Error("listing does not show the staked pool")
	}
}

// The empty state is what an operator sees on a fresh install, and a range
// over nothing is where a missing {{else}} shows up.
func TestPagesRenderEmpty(t *testing.T) {
	render(t, "offers.html", listingData(nil))
	render(t, "admin_offers.html", gin.H{
		"Recent": []AdminRequest{}, "StatusCounts": []StatusCount{},
		"Leaders": []Leader{}, "Trackers": []TrackerStat{},
	})
	render(t, "admin_trackers.html", gin.H{"Trackers": []Tracker{}})
}

// The plugin's copies of the host's stored strings. A drift here means a
// tracker saved as "private" stops matching the visibility check — loud and
// immediate, unlike ComputeOfferHash, which is why these are duplicated and
// that one crosses the seam as a function.
func TestStoredVocabularyMatchesTheDatabase(t *testing.T) {
	for got, want := range map[string]string{
		VisibilityPrivate: "private", VisibilityPublic: "public", VisibilityPersonal: "personal",
		StatusUnvetted: "unvetted", StatusActive: "active", StatusBanned: "banned",
		VerificationHonor: "honor", EntityAnime: "anime",
	} {
		if got != want {
			t.Errorf("stored value drifted: got %q want %q", got, want)
		}
	}
}

// The avatar fallback used `slice $u.Username 0 1`, which counts bytes: a
// multi-byte first character went into the page as half a rune, and an empty
// username errored rather than degrading — and html/template streams, so that
// aborts the render part way down the page.
func TestInitialIsRuneSafe(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"kirisame", "k"},
		{"Ω-user", "Ω"},
		{"日本", "日"},
		{"", ""},
		{string([]byte{0xff}), ""}, // a lone invalid byte, not a character
	} {
		if got := initial(tc.in); got != tc.want {
			t.Errorf("initial(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
