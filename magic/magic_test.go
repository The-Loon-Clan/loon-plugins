package magic

import (
	"html/template"
	"strings"
	"testing"
)

// The level curve is the progression contract; a change here is a change to
// what every member has already earned.
func TestLevelCurve(t *testing.T) {
	cases := []struct {
		xp    int64
		level int
	}{
		{0, 0}, {199, 0}, {200, 1}, {799, 1}, {800, 2}, {5000, 5}, {20000, 10},
		{1 << 40, 10}, // the cap holds however rich the caster
	}
	for _, tc := range cases {
		if got := levelFor(tc.xp); got != tc.level {
			t.Errorf("levelFor(%d) = %d, want %d", tc.xp, got, tc.level)
		}
	}
	if discountPct(0) != 0 || discountPct(5) != 10 || discountPct(10) != 20 || discountPct(99) != 20 {
		t.Error("discount schedule drifted")
	}
	if maxHours(0) != 48 || maxHours(3) != 192 || maxHours(10) != 360 {
		t.Error("duration reach drifted")
	}
	if customAllowed(0) || !customAllowed(1) {
		t.Error("custom unlock level drifted")
	}
}

// The price must move in the directions the page promises: public over
// private, stronger over weaker, longer over shorter, bigger over smaller —
// and a no-op buff prices at nothing meaningful because the cast path
// refuses it before pricing.
func TestCastCostOrdering(t *testing.T) {
	cfg := defaults()
	gb := int64(1) << 30

	private := castCost(cfg, "self", 5*gb, 1, 0, 24)
	public := castCost(cfg, "all", 5*gb, 1, 0, 24)
	if public <= private {
		t.Errorf("public (%d) must cost more than private (%d)", public, private)
	}
	weak := castCost(cfg, "self", 5*gb, 1, 0.5, 24)
	strong := castCost(cfg, "self", 5*gb, 2, 0, 24)
	if strong <= weak {
		t.Errorf("2xFree (%d) must cost more than 50%% (%d)", strong, weak)
	}
	short := castCost(cfg, "self", 5*gb, 1, 0, 24)
	long := castCost(cfg, "self", 5*gb, 1, 0, 96)
	if long <= short || long >= 3*short {
		t.Errorf("4x the hours should cost about 2x: %d vs %d", long, short)
	}
	small := castCost(cfg, "self", 1*gb, 1, 0, 24)
	big := castCost(cfg, "self", 50*gb, 1, 0, 24)
	if big <= small {
		t.Errorf("a big torrent (%d) must cost more than a small one (%d)", big, small)
	}
	// A sub-average torrent prices as average — size never discounts below 1.
	if castCost(cfg, "self", 1*gb, 1, 0, 24) != castCost(cfg, "self", 0, 1, 0, 24) {
		t.Error("sub-average size must not discount")
	}
}

// The discount never makes magic free.
func TestApplyDiscountFloor(t *testing.T) {
	if applyDiscount(1, 20) != 1 {
		t.Error("a discounted cast must still cost at least one point")
	}
	if applyDiscount(100, 10) != 90 {
		t.Error("plain percentage went wrong")
	}
}

// The cast form only exists once a torrent is chosen.
//
// This page used to open with a forty-hex-character field and expect a member
// to fill it in — which is not a thing anyone can do from memory, and is why
// the page stopped being a menu destination. Arriving without a torrent is now
// a wrong turn the page points back out of.
func TestCastFormNeedsATorrentFirst(t *testing.T) {
	// The plugin's own embedded set, parsed exactly as Provision does.
	tmpl, err := template.ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	p := &Plugin{tmpl: tmpl}
	// The real data map the handler builds (views.go), not a struct that only
	// this test knows — a double looser than the thing it stands for passes on
	// markup production rejects.
	render := func(vm map[string]any) string {
		var sb strings.Builder
		if err := p.tmpl.ExecuteTemplate(&sb, "magic.html", vm); err != nil {
			t.Fatalf("render: %v", err)
		}
		return sb.String()
	}

	hash := strings.Repeat("a", 40)
	with := render(map[string]any{"Hash": hash, "TorrentName": "Some.Torrent-GRP",
		"MaxHours": 48, "CSRFToken": "tok"})
	for _, want := range []string{
		`name="hash" value="` + hash, // carried, not typed
		"Some.Torrent-GRP",           // and named, so the caster can see it
		`action="/p/magic/cast"`,
		`href="/tracker/t/` + hash, // the way back to where they came from
	} {
		if !strings.Contains(with, want) {
			t.Errorf("the cast form is missing %q", want)
		}
	}
	// The editable hash box is what this change removes; a text input for it
	// reaching the page again is the regression.
	if strings.Contains(with, `id="mg-hash"`) {
		t.Error("the free-text info-hash field is back")
	}

	// No torrent: no form at all, and a route back to picking one.
	without := render(map[string]any{"MaxHours": 48, "CSRFToken": "tok"})
	if strings.Contains(without, `action="/p/magic/cast"`) {
		t.Error("a cast form rendered with no torrent to cast on")
	}
	if !strings.Contains(without, `href="/tracker"`) {
		t.Error("nothing points a member at the torrent list")
	}
	// The history below still renders either way — it is the rest of the page.
	if !strings.Contains(without, "History") {
		t.Error("the page lost its history along with the form")
	}
}
