package ranks

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

func widgetPlugin(t *testing.T) (*Plugin, *MemStore) {
	t.Helper()
	st := NewMemStore()
	return &Plugin{store: st}, st
}

func noBoost() pluginapi.APIBoost { return pluginapi.APIBoost{Factor: 1} }

// The table is built from the catalog, so what it includes and excludes is the
// behaviour worth pinning: a hidden group is staff machinery an operator marked
// not-for-display, and a group conferring no quota says nothing about
// allowances.
func TestAllowancesTableSelectsRows(t *testing.T) {
	ctx := context.Background()
	p, st := widgetPlugin(t)
	seedGroup(t, st, &Group{Name: "Power User", Slug: "power", Kind: "paid", Visible: true,
		Grants: map[string]int64{entAPIDaily: 25000, entDownloadDaily: 250}})
	seedGroup(t, st, &Group{Name: "Staff", Slug: "staff", Kind: "assigned", Visible: false,
		Grants: map[string]int64{entAPIDaily: 99999, entDownloadDaily: 9999}})
	seedGroup(t, st, &Group{Name: "Badge Only", Slug: "badge", Kind: "earned", Visible: true})

	got, err := p.allowancesTable(ctx, noBoost())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := string(got)
	if !strings.Contains(out, "Power User") {
		t.Error("a visible rank with quotas is missing")
	}
	if strings.Contains(out, "Staff") {
		t.Error("a hidden rank is on a public card")
	}
	if strings.Contains(out, "Badge Only") {
		t.Error("a rank conferring no quota is listed, inviting the reader to wonder what they are missing")
	}
	// Grouped digits: these numbers exist to be compared, and comparing them by
	// counting zeroes is what makes the table useless.
	if !strings.Contains(out, "25,000") {
		t.Errorf("API allowance is not digit-grouped:\n%s", out)
	}
}

// Nothing to show must be an empty fragment, not an empty table: the host drops
// the widget entirely rather than drawing a box around nothing.
func TestAllowancesTableEmptyWhenNoQuotasExist(t *testing.T) {
	p, st := widgetPlugin(t)
	seedGroup(t, st, &Group{Name: "Badge", Slug: "badge", Kind: "earned", Visible: true})
	got, err := p.allowancesTable(context.Background(), noBoost())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got != "" {
		t.Errorf("want an empty fragment, got:\n%s", got)
	}
}

// With a boost active the card must show BOTH numbers. Showing only the boosted
// one silently redefines the rank, and the day the window closes a member's
// allowance appears to have been cut.
func TestAllowancesTableShowsBoostAndBase(t *testing.T) {
	ctx := context.Background()
	p, st := widgetPlugin(t)
	seedGroup(t, st, &Group{Name: "Member", Slug: "member", Kind: "assigned", Visible: true,
		Grants: map[string]int64{entAPIDaily: 10000, entDownloadDaily: 100}})

	ends := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	boost := pluginapi.APIBoost{Factor: 10, Slug: "load-testing-month",
		Name: "Load Testing Month", Ends: ends}

	got, err := p.allowancesTable(ctx, boost)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := string(got)
	for _, want := range []string{
		"Load Testing Month", // the banner names the event
		"×10",                // and the factor
		"10 Sep 2026",        // and when it lapses
		"100,000",            // the boosted allowance
		"<s>10,000</s>",      // with the base still visible
	} {
		if !strings.Contains(out, want) {
			t.Errorf("boosted card is missing %q:\n%s", want, out)
		}
	}

	// Grabs must NOT be multiplied: a grab costs provider bandwidth, an API
	// call costs a query, and the two are deliberately different decisions.
	if strings.Contains(out, "1,000") {
		t.Errorf("the download allowance was boosted too:\n%s", out)
	}
}

// A perpetual window's end is a sentinel in the year 9000; printing it would be
// worse than printing nothing, so the host strips it and the banner must cope.
func TestAllowancesBannerOmitsUnknownEnd(t *testing.T) {
	ctx := context.Background()
	p, st := widgetPlugin(t)
	seedGroup(t, st, &Group{Name: "Member", Slug: "member", Kind: "assigned", Visible: true,
		Grants: map[string]int64{entAPIDaily: 10000}})
	got, _ := p.allowancesTable(ctx, pluginapi.APIBoost{Factor: 2, Name: "Forever"})
	out := string(got)
	if !strings.Contains(out, "Forever") || !strings.Contains(out, "×2") {
		t.Errorf("banner missing its label or factor:\n%s", out)
	}
	if strings.Contains(out, "until") {
		t.Errorf("banner claimed an end date it does not have:\n%s", out)
	}
}

// The rank colour is operator-supplied and reaches a style attribute, so it is
// validated as a hex literal rather than trusted. An operator field arriving
// unchecked in CSS is how a catalog row starts closing tags.
func TestRankColourMustBeHex(t *testing.T) {
	if got := rankNameHTML(rankRow{Name: "Elite", Color: "#ff0000"}); !strings.Contains(got, `color:#ff0000`) {
		t.Errorf("a valid hex colour was dropped: %s", got)
	}
	hostile := rankNameHTML(rankRow{Name: "Elite", Color: `red;"></span><script>alert(1)</script>`})
	if strings.Contains(hostile, "<script") || strings.Contains(hostile, "style=") {
		t.Errorf("a non-hex colour reached the markup: %s", hostile)
	}
	if !strings.Contains(hostile, "Elite") {
		t.Errorf("rejecting the colour also lost the name: %s", hostile)
	}
	// The name itself is escaped whatever the colour does.
	esc := rankNameHTML(rankRow{Name: `<b>x</b>`, Color: "#fff"})
	if strings.Contains(esc, "<b>") {
		t.Errorf("rank name was not escaped: %s", esc)
	}
}

func TestThousands(t *testing.T) {
	for in, want := range map[int64]string{
		0: "0", 7: "7", 999: "999", 1000: "1,000", 10000: "10,000",
		100000: "100,000", 999999: "999,999", 1000000: "1,000,000", -1234: "-1,234",
	} {
		if got := thousands(in); got != want {
			t.Errorf("thousands(%d) = %q, want %q", in, got, want)
		}
	}
}

// ── the groups roster ───────────────────────────────────────────────────────

// testGinContext is the minimal request the widget needs: it reads nothing off
// the context but the request's own ctx, because the config arrives as a plain
// argument — which is what makes this renderable without a session.
func testGinContext() *gin.Context {
	gc, _ := gin.CreateTestContext(httptest.NewRecorder())
	gc.Request = httptest.NewRequest("GET", "/staff", nil)
	return gc
}

// seedRoster puts named members in a group, which is what the widget lists.
func seedRoster(t *testing.T, st *MemStore, g *Group, names map[int]string) {
	t.Helper()
	for id, name := range names {
		// A real duration: MemStore stamps expires_at = now + dur, and a
		// zero-length membership has already lapsed by the time Roster asks.
		if err := st.AddMember(context.Background(), id, g.ID, 24*time.Hour); err != nil {
			t.Fatalf("add member: %v", err)
		}
		st.SetUsername(id, name)
	}
}

// The config is the whole interface an operator has to this widget, whether
// they typed it in a placement field or in a page's [widget ranks-groups ...]
// shortcode. Each arm selects a different set, and getting one wrong publishes
// a list nobody asked for.
func TestGroupRosterSelection(t *testing.T) {
	p, st := widgetPlugin(t)
	staff := seedGroup(t, st, &Group{Name: "Sysop", Slug: "sysop", Kind: "assigned", Visible: true, Icon: "shield"})
	earned := seedGroup(t, st, &Group{Name: "Elite", Slug: "elite", Kind: "earned", Visible: true})
	hidden := seedGroup(t, st, &Group{Name: "Machinery", Slug: "machinery", Kind: "assigned", Visible: false})
	empty := seedGroup(t, st, &Group{Name: "Retired", Slug: "retired", Kind: "assigned", Visible: true})
	_ = empty

	seedRoster(t, st, staff, map[int]string{1: "alice"})
	seedRoster(t, st, earned, map[int]string{2: "bob"})
	seedRoster(t, st, hidden, map[int]string{3: "carol"})

	render := func(cfg string) string {
		got, err := p.renderGroupRoster(testGinContext(), cfg)
		if err != nil {
			t.Fatalf("render %q: %v", cfg, err)
		}
		return string(got)
	}

	all := render("")
	for _, want := range []string{"Sysop", "alice", "Elite", "bob", "#shield"} {
		if !strings.Contains(all, want) {
			t.Errorf("the unconfigured widget is missing %q", want)
		}
	}
	// Two absences that are policy, not accident: a group an operator marked
	// not-for-display, and one with nobody in it. Listing "Retired — 0" tells a
	// reader nothing, and on a fresh install it would be every panel.
	for _, absent := range []string{"Machinery", "carol", "Retired"} {
		if strings.Contains(all, absent) {
			t.Errorf("the widget published %q", absent)
		}
	}

	assigned := render("assigned")
	if !strings.Contains(assigned, "alice") || strings.Contains(assigned, "bob") {
		t.Errorf("kind=assigned selected the wrong groups:\n%s", assigned)
	}
	one := render("elite")
	if !strings.Contains(one, "bob") || strings.Contains(one, "alice") {
		t.Errorf("a slug config selected the wrong group:\n%s", one)
	}
	// A config naming nothing renders nothing rather than falling back to
	// everything — a typo must not publish the whole membership.
	if got := render("no-such-group"); strings.TrimSpace(got) != "" {
		t.Errorf("an unknown config rendered %q", got)
	}
}
