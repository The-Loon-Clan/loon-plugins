package offers

import (
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

func TestOffersPageRenders(t *testing.T) {
	got := render(t, "offers.html", gin.H{
		"EntityType": EntityAnime,
		"SizeBucket": "",
		"Buckets":    []Bucket{sampleBucket(), sampleBucket()},
		"Leaders": []Leader{{
			UserID: 1, Username: "kirisame", OfferCount: 9,
			FulfilledCount: 7, FailedCount: 1, LastFulfilledAt: ts("2026-08-06T12:00:00Z"),
		}},
		"Recent": []Fulfillment{{
			RequestID: 3, BucketID: 5, DeliveredAt: time.Now(),
			EntityType: EntityAnime, Resolution: "1080p", SourceTag: "web-dl", SizeBucket: "<1GB",
		}},
		"Trackers": []TrackerStat{{
			TrackerID: 1, TrackerName: "AnimeBytes", TrackerVisibility: VisibilityPrivate,
			OfferCount: 12, UserCount: 4, DeliveriesWeek: 2,
		}},
	})
	for _, want := range []string{"kirisame", "AnimeBytes", "1080p"} {
		if !strings.Contains(got, want) {
			t.Errorf("offers page missing %q", want)
		}
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

// The fulfilled tab: delivered buckets leave the open list and link their
// release; the tab strip carries the count; and the open tab, searched empty,
// says so against the query.
func TestOffersFulfilledTabRenders(t *testing.T) {
	b := sampleBucket()
	nzbID := int64(176112514)
	b.Fulfilled, b.DeliveredNzbID, b.Title = true, &nzbID, "Spirited Away"

	got := render(t, "offers.html", gin.H{
		"EntityType": EntityAnime, "SizeBucket": "", "Query": "", "Tab": "fulfilled",
		"Buckets": []Bucket{}, "Fulfilled": []Bucket{b}, "FulfilledTotal": 1,
		"Leaders": []Leader{}, "Recent": []Fulfillment{}, "Trackers": []TrackerStat{},
	})
	for _, want := range []string{"Spirited Away", "/release/176112514", "view the release", `tab-count">1`} {
		if !strings.Contains(got, want) {
			t.Errorf("fulfilled tab missing %q", want)
		}
	}
	if strings.Contains(got, "data-bucket-id") {
		t.Error("fulfilled tab renders a Request button — the bytes are already on Usenet")
	}

	open := render(t, "offers.html", gin.H{
		"EntityType": EntityAnime, "SizeBucket": "", "Query": "frieren", "Tab": "open",
		"Buckets": []Bucket{}, "Fulfilled": []Bucket{}, "FulfilledTotal": 0,
		"Leaders": []Leader{}, "Recent": []Fulfillment{}, "Trackers": []TrackerStat{},
	})
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
	got := render(t, "offers.html", gin.H{
		"EntityType": EntityAnime, "SizeBucket": "",
		"Buckets": []Bucket{b}, "Leaders": []Leader{},
		"Recent": []Fulfillment{}, "Trackers": []TrackerStat{},
	})
	if !strings.Contains(got, "1000 pts staked") {
		t.Error("listing does not show the staked pool")
	}
}

// The empty state is what an operator sees on a fresh install, and a range
// over nothing is where a missing {{else}} shows up.
func TestPagesRenderEmpty(t *testing.T) {
	render(t, "offers.html", gin.H{
		"EntityType": EntityAnime, "SizeBucket": "",
		"Buckets": []Bucket{}, "Leaders": []Leader{},
		"Recent": []Fulfillment{}, "Trackers": []TrackerStat{},
	})
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
