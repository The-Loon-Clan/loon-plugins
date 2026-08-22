package donations

import (
	"context"
	"html/template"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// Executing both fragments is the only thing that proves the view models the
// handlers build still match what the markup reads. html/template streams: a
// field the markup wants and the data lacks aborts the render part way
// through and returns half a page with nothing logged — which is exactly how
// the public donate page shipped for months with a reference to a field the
// Donation model lost in a rename, truncating at the Recent Donors carousel
// whenever a donation existed. The view models are structs so that miss is
// an ERROR, and these tests are where it surfaces.

func donationsTestAuth(signedIn bool) core.AuthService {
	return core.NewAuth(core.AuthAdapter{
		CurrentUserFn: func(c *gin.Context) (*core.User, bool) {
			if !signedIn {
				return nil, false
			}
			return &core.User{ID: 7, Username: "tester", Role: core.RoleAdmin}, true
		},
	})
}

func testRenderDonate(t *testing.T, signedIn bool, name string, vm donateVM) string {
	t.Helper()
	var captured template.HTML
	SetDeps(Deps{
		RenderPage: func(c *gin.Context, status int, title string, body template.HTML) { captured = body },
		RenderError: func(c *gin.Context, code int, title, msg string) {
			t.Fatalf("page render reached RenderError: %d %s %s", code, title, msg)
		},
		CSRFToken:    func(c *gin.Context) string { return "test-csrf" },
		RelativeTime: func(v any) string { return "3 days ago" },
		Settings:     nil, // render path never touches the data seams
	})
	t.Cleanup(func() { deps = nil; pageTmpl = nil })
	if err := parseTemplates(); err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}
	h := &Handlers{deps: *deps, auth: donationsTestAuth(signedIn)}
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/help/donate", nil)
	h.render(c, 200, "t", name, vm, gin.H{})
	if captured == "" {
		// Re-execute directly to surface the underlying template error in
		// the failure message.
		var sb strings.Builder
		err := pageTmpl.ExecuteTemplate(&sb, name, vm)
		t.Fatalf("%s rendered empty — template error: %v", name, err)
	}
	out := string(captured)
	for _, unwanted := range []string{"<!DOCTYPE", "<html", `template "navbar"`, `template "footer"`} {
		if strings.Contains(out, unwanted) {
			t.Errorf("%s carries host chrome it should not: %q", name, unwanted)
		}
	}
	return out
}

// ── fixtures — keys read off the actual handler calls, not invented ──

func sampleGroup() *DonationGoalGroup {
	return &DonationGoalGroup{
		Name: "site", Locks: true,
		MonthlyGoalUSD: 100, MonthlyRaisedUSD: 40,
		YearlyGoalUSD: 600, YearlyRaisedUSD: 120,
		Items: []*SiteCost{
			{ID: 1, Label: "Dedicated Server", GoalGroup: "site", Period: "monthly", AmountUSD: 80, Notes: "", Active: true},
			{ID: 2, Label: "Domain & DNS", GoalGroup: "site", Period: "yearly", AmountUSD: 30, Notes: "registrar", Active: true},
		},
		MonthlyResetAt: time.Now().AddDate(0, 1, 0),
		YearlyResetAt:  time.Now().AddDate(1, 0, 0),
	}
}

func samplePackageView(funded bool) *DonationPackageView {
	v := &DonationPackageView{DonationPackage: DonationPackage{
		ID: 3, Label: "Server Patron 2026", AmountUSD: 250, StockTotal: 2,
		Reward: "Hall of Fame slot", Description: "Covers one year of VPS hosting.", Active: true,
	}}
	if funded {
		v.Recompute(2)
	} else {
		v.Recompute(1)
	}
	return v
}

func sampleDonation() *Donation {
	uid := 7
	return &Donation{
		ID: 1, Asset: "BTC", AmountUSD: 25, DonorUserID: &uid,
		DonorLabel: "rain-friend", ReceivedAt: time.Now(), Note: "ty",
	}
}

func donatePageFixture() *donatePageVM {
	return &donatePageVM{
		Groups:          []*DonationGoalGroup{sampleGroup()},
		PointsConfig:    DonationPointsConfig{PointsPerDollar: 1, MultiplierPer10: 1.2, DonatorThresholdUSD: 5},
		BTCAddress:      "bc1qtestaddr",
		RecentDonations: []*Donation{sampleDonation()},
		TotalMonthlyUSD: 80, TotalYearlyUSD: 30, TotalAnnualUSD: 990,
		// Mirrors the handler, which passes donorTiers(ctx, Settings). nil
		// Settings yields the default ladder, which is the path worth
		// rendering: it is what every host without a donate_tiers setting
		// gets.
		Tiers:       donorTiers(context.Background(), nil),
		TipJarGoals: []TipJarGoal{{Slot: 1, Name: "Profile Effects", TargetUSD: 250, RaisedUSD: 50, PercentRound: 20, RingOffset: 150.8}},
		Packages:    []*DonationPackageView{samplePackageView(false)},
		FundedPackages: []*DonationPackageView{func() *DonationPackageView {
			p := samplePackageView(true)
			p.Label = "Domain Patron"
			return p
		}()},
	}
}

func adminDonateFixture() *adminDonateVM {
	uid := 7
	return &adminDonateVM{
		IsAdmin: true,
		Costs: []*SiteCost{
			{ID: 1, Label: "Dedicated Server", GoalGroup: "site", Period: "monthly", AmountUSD: 80, Notes: "hetzner", Active: true},
			{ID: 2, Label: "Domain & DNS", GoalGroup: "site", Period: "yearly", AmountUSD: 30, Active: false},
		},
		Config:        DonationPointsConfig{PointsPerDollar: 1, MultiplierPer10: 1.2, DonatorThresholdUSD: 5},
		LockingGroups: "site",
		Preview:       []DonationPointsRow{{Dollars: 10, Points: 12}},
		DonateEnabled: true,
		Donations: []*Donation{{
			ID: 1, Asset: "fiat", AmountUSD: 25, DonorUserID: &uid,
			DonorLabel: "rain-friend", ReceivedAt: time.Now(), Note: "manual",
		}},
		Usernames: map[int]string{7: "tester"},
		Wallet:    map[string]string{"btc": "bc1q", "eth": "", "xmr": "", "btcpay_url": "", "btcpay_store_id": "", "btcpay_api_key": "", "btcpay_secret": ""},
		TipJar:    map[string]string{"goal_1_name": "", "goal_1_target_usd": "", "goal_1_raised_usd": "", "goal_2_name": "", "goal_2_target_usd": "", "goal_2_raised_usd": ""},
		Packages:  []*DonationPackage{{ID: 3, Label: "Server Patron 2026", AmountUSD: 250, StockTotal: 2, Active: true}},
	}
}

// The donor ladder is DATA now, and the default must not carry one site's
// motif. It used to be five cards in the markup named Rain, Storm, Monsoon,
// Typhoon and Legendary Supporter — the weather ladder of the site this plugin
// was lifted out of, on every host that installed it.
func TestDonorTiersDefaultIsSiteNeutral(t *testing.T) {
	out := testRenderDonate(t, false, "help_donate.html", donatePageFixture())
	for _, motif := range []string{"Rain Supporter", "Storm Supporter",
		"Monsoon Supporter", "Typhoon Supporter", "Keep the Rain Falling"} {
		if strings.Contains(out, motif) {
			t.Errorf("the page still ships the lifted site's motif: %q", motif)
		}
	}
	// Matched inside the tier-name element, not as bare words: "Patron"
	// also appears in the fixture's "Server Patron 2026" package, so a
	// substring check would have passed with no ladder rendered at all.
	for _, want := range []string{"Supporter", "Patron", "Benefactor", "Champion", "Founder"} {
		if !strings.Contains(out, `<div class="tier-name">`+want+`</div>`) {
			t.Errorf("the default ladder is missing %q", want)
		}
	}
}

// An operator's ladder replaces the default, and a malformed one does not
// leave the section empty — a donate page with no tiers reads as a site that
// offers nothing for donating, which is a worse answer to a typo than the
// defaults. An EMPTY array is honoured, because that is a deliberate choice
// rather than an absent one.
func TestDonorTiersComeFromTheSettingIfSet(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      []string
		notWant   []string
	}{
		{"configured", `[{"name":"Bronze","perks":"Badge","price":"$3+"}]`,
			[]string{"Bronze", "$3+"}, []string{"Benefactor"}},
		{"malformed", `{not json`, []string{"Supporter", "Founder"}, nil},
		{"unset", "", []string{"Supporter", "Founder"}, nil},
		{"deliberately empty", `[]`, nil, []string{"Supporter", "Founder"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := donorTiers(context.Background(), fakeSettings{"donate_tiers": tc.raw})
			var names strings.Builder
			for _, x := range got {
				names.WriteString(x.Name + " " + x.Price + " ")
			}
			for _, w := range tc.want {
				if !strings.Contains(names.String(), w) {
					t.Errorf("missing %q in %q", w, names.String())
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(names.String(), w) {
					t.Errorf("unexpected %q in %q", w, names.String())
				}
			}
		})
	}
}

type fakeSettings map[string]string

func (f fakeSettings) GetSetting(ctx context.Context, k string) (string, error) {
	return f[k], nil
}
func (f fakeSettings) SetSetting(ctx context.Context, k, v string) error {
	f[k] = v
	return nil
}

func TestPagesRender(t *testing.T) {
	out := testRenderDonate(t, false, "help_donate.html", donatePageFixture())
	for _, marker := range []string{
		"Keep this site running", // hero, with no SiteName seam wired
		"Dedicated Server",       // cost card from the group items
		"Server Patron 2026",     // claimable package
		"Domain Patron",          // funded package row
		"Profile Effects",        // tip-jar goal
		"rain-friend",            // recent donor label
		"bc1qtestaddr",           // receive address
		"log in",                 // IsAnon tip (anonymous viewer)
	} {
		if !strings.Contains(out, marker) {
			t.Errorf("help_donate missing %q", marker)
		}
	}

	out = testRenderDonate(t, true, "admin_donate.html", adminDonateFixture())
	for _, marker := range []string{
		"Donate &mdash; admin",
		"hetzner",            // cost note
		"Server Patron 2026", // package table
		"rain-friend",        // donation log row
		"tester",             // resolved donor username
		"Test BTCPay connection",
	} {
		if !strings.Contains(out, marker) {
			t.Errorf("admin_donate missing %q", marker)
		}
	}
}

// The regression this lift fixed: the Recent Donors carousel used to read
// $d.CreatedAt, a field Donation does not have — the map-shaped data
// rendered it as a silent mid-stream abort, so the page truncated at the
// carousel whenever a donation existed. The struct VM makes it a hard
// error, and this test pins the corrected field actually rendering.
func TestHelpDonateRendersTheRecentDonorTimestamp(t *testing.T) {
	out := testRenderDonate(t, false, "help_donate.html", donatePageFixture())
	if !strings.Contains(out, `<div class="rd-when">3 days ago</div>`) {
		t.Error("recent-donor card lost its ReceivedAt timestamp — the carousel would truncate the page")
	}
	// Everything after the carousel must have rendered too — a stream abort
	// would cut the outro banner. Matched on the half of the sentence that is
	// NOT the site's name: it used to read "ameNZB exists because of you" on
	// every host that installed this plugin, and now the name is a seam.
	if !strings.Contains(out, "exists because of you.") {
		t.Error("content after the carousel is missing — the render aborted mid-stream")
	}
}

// The site name is the host's to supply, and this page said "ameNZB" five
// times in copy a visitor reads — the name of the site the plugin was lifted
// out of, on every host that installed it.
//
// Both directions are pinned: a host that supplies a name gets it, and a host
// that supplies none gets a phrase that is true everywhere rather than a gap
// in the middle of a sentence.
func TestSiteNameComesFromTheHost(t *testing.T) {
	named := renderDonateWithSiteName(t, func() string { return "Loon Indexer" })
	if !strings.Contains(named, "Loon Indexer is 100% community-funded.") {
		t.Error("the host's site name did not reach the hero copy")
	}
	if strings.Contains(named, "ameNZB") {
		t.Error("the page still names the site this plugin was lifted out of")
	}

	anon := renderDonateWithSiteName(t, nil)
	if !strings.Contains(anon, "This site is 100% community-funded.") {
		t.Error("with no seam the copy should read \"This site\", not a gap")
	}
	for _, gap := range []string{"  is 100%", "keeping  fast"} {
		if strings.Contains(anon, gap) {
			t.Errorf("the name left a hole in the sentence: %q", gap)
		}
	}
}

func renderDonateWithSiteName(t *testing.T, name func() string) string {
	t.Helper()
	var captured template.HTML
	SetDeps(Deps{
		RenderPage:   func(c *gin.Context, status int, title string, body template.HTML) { captured = body },
		RenderError:  func(c *gin.Context, code int, title, msg string) { t.Fatalf("%d %s", code, msg) },
		CSRFToken:    func(c *gin.Context) string { return "test-csrf" },
		RelativeTime: func(v any) string { return "3 days ago" },
		SiteName:     name,
	})
	t.Cleanup(func() { deps = nil; pageTmpl = nil })
	if err := parseTemplates(); err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}
	h := &Handlers{deps: *deps, auth: donationsTestAuth(false)}
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/help/donate", nil)
	h.render(c, 200, "t", "help_donate.html", donatePageFixture(), gin.H{})
	return string(captured)
}

// This test pins the POST-form inventory and their inline tokens. A form
// count change means a new POST surface to check, and a lost token means the
// form 403s.
//
// The claim form's token was `name="csrf_token"` until the anonymity work —
// a field name NO middleware reads, so on any host gating the route the claim
// 403'd regardless of the token's value, and this test was PINNING the broken
// name as correct. It pins _csrf now, the name the host middleware compares.
func TestPostFormsAndInlineCSRFTokens(t *testing.T) {
	out := testRenderDonate(t, false, "help_donate.html", donatePageFixture())
	if forms := strings.Count(out, `method="POST"`); forms != 1 {
		t.Errorf("help_donate renders %d POST forms, want 1 (the package claim)", forms)
	}
	if !strings.Contains(out, `name="_csrf" value="test-csrf"`) {
		t.Error("the claim form lost its inline _csrf input")
	}
	// The anonymity controls: the choice must exist BEFORE payment, because
	// after settlement there is nobody to ask.
	if !strings.Contains(out, `name="anonymous"`) {
		t.Error("the claim form lost the don't-list-me checkbox")
	}
	if !strings.Contains(out, `name="donor_label"`) {
		t.Error("the claim form lost the shown-as field")
	}

	out = testRenderDonate(t, true, "admin_donate.html", adminDonateFixture())
	// costs add + per-cost delete (2 rows) + points + wallet + btcpay-health
	// + package save + per-package delete + tipjar + manual log = 10.
	if forms := strings.Count(out, `method="POST"`); forms != 10 {
		t.Errorf("admin_donate renders %d POST forms, want 10 — the csrf-js reliance note needs re-checking", forms)
	}
	if !strings.Contains(out, `name="_csrf" value="test-csrf"`) {
		t.Error("the btcpay-health form lost its inline _csrf input")
	}
}

// ── The tab-structure tests, moved here with the markup they pin. They
// lived in indexer-site/web/handlers/admin_donate_tabs_test.go, parsing the
// host's copy of this file; the plugin owns the markup now, so the
// invariants move with it. The structural one worth keeping is the
// pane/tab pairing: a tab whose href has no pane is a dead click, a pane
// with no tab is unreachable, and both render fine in a diff. ──

func renderAdminDonate(t *testing.T, vm *adminDonateVM) string {
	t.Helper()
	return testRenderDonate(t, true, "admin_donate.html", vm)
}

func TestAdminDonate_EveryTabHasAPane(t *testing.T) {
	out := renderAdminDonate(t, adminDonateFixture())

	tabs := regexp.MustCompile(`data-bs-toggle="tab" href="#([a-z-]+)"`).FindAllStringSubmatch(out, -1)
	if len(tabs) != 6 {
		t.Fatalf("found %d tabs, want 6 (costs, points, wallet, packages, tipjar, log)", len(tabs))
	}
	panes := map[string]bool{}
	for _, m := range regexp.MustCompile(`<div id="([a-z-]+)" class="tab-pane`).FindAllStringSubmatch(out, -1) {
		panes[m[1]] = true
	}
	for _, tab := range tabs {
		if !panes[tab[1]] {
			t.Errorf("tab #%s has no matching pane — clicking it shows nothing", tab[1])
		}
		delete(panes, tab[1])
	}
	for orphan := range panes {
		t.Errorf("pane #%s has no tab — the section is unreachable", orphan)
	}
}

// Exactly one pane may start active, or Bootstrap shows several stacked at
// once.
func TestAdminDonate_ExactlyOneActivePane(t *testing.T) {
	out := renderAdminDonate(t, adminDonateFixture())

	if n := strings.Count(out, `class="tab-pane active"`); n != 1 {
		t.Errorf("%d panes start active, want exactly 1", n)
	}
	if n := strings.Count(out, `class="nav-link active"`); n != 1 {
		t.Errorf("%d tabs start active, want exactly 1", n)
	}
}

// The save-redirects target #packages, #log and friends. Losing the hash
// handler would answer every save by dropping the admin back on Costs.
func TestAdminDonate_ActivatesTheTabNamedByTheHash(t *testing.T) {
	out := renderAdminDonate(t, adminDonateFixture())

	if !strings.Contains(out, "location.hash") {
		t.Error("no hash handling: a save-redirect to #packages would open Costs")
	}
	if !strings.Contains(out, "shown.bs.tab") {
		t.Error("the hash is not kept in step as tabs are clicked")
	}
	// The ids the redirects use must still exist, since they are the
	// contract between the handlers and this page.
	for _, id := range []string{"costs", "points", "wallet", "packages", "tipjar", "log"} {
		if !strings.Contains(out, `id="`+id+`"`) {
			t.Errorf("section id %q disappeared — a save-redirect targets it", id)
		}
	}
}

// The page should use the house container tier and the shared tab markup,
// not the legacy alias.
func TestAdminDonate_UsesTheHouseChrome(t *testing.T) {
	out := renderAdminDonate(t, adminDonateFixture())

	if strings.Contains(out, "site-container") {
		t.Error("still using the legacy .site-container alias instead of .page")
	}
	if strings.Contains(out, `class="card-header" style="font-size`) {
		t.Error("a card header still carries inline font sizing")
	}
	if !strings.Contains(out, `class="nav tabs"`) {
		t.Error("not using the shared .nav.tabs markup")
	}
}

// Counts render from the data, and must not explode when it is absent —
// with the struct VM the empty case is a nil slice, and the guarded count
// simply does not render (which is also the better pill: no "0" badge on an
// empty tab).
func TestAdminDonate_TabCounts(t *testing.T) {
	vm := adminDonateFixture()
	out := renderAdminDonate(t, vm)
	if !strings.Contains(out, `<span class="tab-count">2</span>`) {
		t.Error("the Costs tab count did not render")
	}
	if !strings.Contains(out, `<span class="tab-count">1</span>`) {
		t.Error("the Packages tab count did not render")
	}

	empty := renderAdminDonate(t, &adminDonateVM{
		Wallet: map[string]string{}, TipJar: map[string]string{},
	})
	if strings.Contains(empty, `class="tab-count"`) {
		t.Error("an empty page rendered a count pill; it should render none")
	}
}

// The whole fragment set must parse in one pass — Provision treats a parse
// failure as a boot failure, and this is the test-side statement of that.
func TestFragmentSetParses(t *testing.T) {
	SetDeps(Deps{RelativeTime: func(v any) string { return "" }})
	t.Cleanup(func() { deps = nil; pageTmpl = nil })
	if err := parseTemplates(); err != nil {
		t.Fatalf("fragment set does not parse: %v", err)
	}
	for _, name := range []string{"help_donate.html", "admin_donate.html"} {
		if pageTmpl.Lookup(name) == nil {
			t.Errorf("fragment %s missing from the parsed set", name)
		}
	}
}

// The legacy contract must keep working, because loon-demo-site still wires
// it and builds against this working tree; and errors must route through the
// seam when it is wired.
func TestRenderContracts(t *testing.T) {
	t.Cleanup(func() { deps = nil })

	legacy := &Deps{BaseData: func(c *gin.Context, extra gin.H) gin.H { return extra }}
	if !legacy.renderContractOK() {
		t.Fatal("a host on the previous contract is refused — this breaks loon-demo-site")
	}

	half := &Deps{RenderPage: func(*gin.Context, int, string, template.HTML) {}}
	if half.renderContractOK() {
		t.Error("a half-wired host was accepted — some pages would render, others blank")
	}

	// renderError routes through the seam when wired.
	var gotCode int
	var gotTitle string
	SetDeps(Deps{
		RenderPage:   func(*gin.Context, int, string, template.HTML) {},
		RenderError:  func(c *gin.Context, code int, title, msg string) { gotCode, gotTitle = code, title },
		CSRFToken:    func(*gin.Context) string { return "" },
		RelativeTime: func(any) string { return "" },
	})
	h := &Handlers{deps: *deps, auth: donationsTestAuth(false)}
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/donate/claim-package/1", nil)
	h.renderError(c, 503, "Click-to-claim isn't set up yet", "BTCPay Server isn't configured on this site.")
	if gotCode != 503 || gotTitle != "Click-to-claim isn't set up yet" {
		t.Errorf("renderError did not cross the seam: code=%d title=%q", gotCode, gotTitle)
	}
}
