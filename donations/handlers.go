package donations

// Public donation page handler. Renders /help/donate with:
//   - Per-period thermometers (monthly = sum of monthly site_costs;
//     yearly = sum of yearly site_costs). Locks to "✓ Funded — donations
//     reopen on <date>" when raised >= goal.
//   - The points-curve preview table so donors see what they'd earn.
//   - Recent-donor leaderboard, anon-respecting (empty donor_label
//     renders as "Anonymous"; the underlying user_id stays so the
//     Donator badge + points still apply on their profile).
//   - BTC / ETH / XMR addresses pulled from settings; hidden when both
//     period locks are active.
//
// Admin pages (cost CRUD + manual donation entry + points-config) live
// in admin_donate_handler.go (next commit).
//
// Crypto integration is out of scope for this file — donations land
// here either via the admin's manual-entry form or, in a later phase,
// a BTCPay webhook handler that calls store.CreateDonation directly.

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// DonationPointsRow is one ($, points) cell in the curve-preview table.
type DonationPointsRow struct {
	Dollars float64
	Points  float64
}

// TipJarGoal is one admin-configured tip-jar target shown on the
// donate page's "Tip Jar" panel (formerly Community Goals). All are
// treated as yearly goals — the ring widget renders RaisedUSD as a
// fraction of TargetUSD. RaisedUSD is admin-set (not auto-summed
// from donations) per the chosen MVP shape; promoting to auto-track
// later means swapping the read site, not the model.
//
// PercentRound is the integer 0-100 rendered in the centre of the
// ring; the storage layer computes it once so the template doesn't
// have to deal with float division + cap math.
type TipJarGoal struct {
	Slot         int     // 1-based position in the settings keyspace; stable across reads
	Name         string  // free-form label, e.g. "Animated Profile Effects"
	TargetUSD    float64 // 0 = goal not configured; the entry is skipped on render
	RaisedUSD    float64 // admin-set; clamped to >= 0 by the parser
	PercentRound int     // capped at 100 for the bar; raw percent shown to the right
	// RingOffset is the SVG stroke-dashoffset for the progress ring
	// (r=30, circumference ≈ 188.5). Pre-computed here so the
	// template doesn't have to do float arithmetic — a prior bug
	// shipped because the donate page tried `add (printf "%.2f" $x)`
	// for similar work.
	RingOffset float64
}

// tipJarRingCircumference is 2 × π × r where r=30 — matches the
// SVG <circle r="30"> in the template. Kept here so the template
// and Go agree on a single source of truth.
const tipJarRingCircumference = 188.5

// tipJarSlots is the max number of tip-jar goals the admin can
// configure. Two slots cover the "small + stretch" pattern the
// donate page wants; extending to more is a one-line change here
// plus the corresponding admin form fields.
const tipJarSlots = 2

// loadTipJarGoals reads all configured slots from site_settings and
// returns only the ones that have BOTH a non-empty Name AND a
// positive TargetUSD. Settings keys per slot N:
//
//	tipjar_goal_N_name        — free-form string
//	tipjar_goal_N_target_usd  — float, parsed via ParseFloat
//	tipjar_goal_N_raised_usd  — float, parsed via ParseFloat
//
// Goals are returned in slot order so the panel's two ring cards
// stay positionally stable when admin edits one without the other.
func loadTipJarGoals(c *gin.Context, h *Handlers) []TipJarGoal {
	ctx := c.Request.Context()
	getF := func(k string) float64 {
		v, _ := h.deps.Settings.GetSetting(ctx, k)
		if v == "" {
			return 0
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || f < 0 {
			return 0
		}
		return f
	}
	goals := make([]TipJarGoal, 0, tipJarSlots)
	for slot := 1; slot <= tipJarSlots; slot++ {
		name, _ := h.deps.Settings.GetSetting(ctx, "tipjar_goal_"+strconv.Itoa(slot)+"_name")
		name = strings.TrimSpace(name)
		target := getF("tipjar_goal_" + strconv.Itoa(slot) + "_target_usd")
		raised := getF("tipjar_goal_" + strconv.Itoa(slot) + "_raised_usd")
		if name == "" || target <= 0 {
			// Slot not configured — skip silently. Empty admin
			// rows are normal; we don't render placeholders.
			continue
		}
		pct := 0
		if target > 0 {
			pct = int(raised / target * 100)
			if pct > 100 {
				pct = 100
			}
		}
		goals = append(goals, TipJarGoal{
			Slot:         slot,
			Name:         name,
			TargetUSD:    target,
			RaisedUSD:    raised,
			PercentRound: pct,
			RingOffset:   tipJarRingCircumference * float64(100-pct) / 100.0,
		})
	}
	return goals
}

// donatePreviewDollars are the X-axis points of the preview table.
// Hand-picked to span typical donor amounts and show the threshold
// effect; admin can't reconfigure without a code change because the
// table is purely presentational.
var donatePreviewDollars = []float64{1, 5, 10, 25, 50, 100, 250, 500}

// pointsConfigFromSettings reads donate_* keys and builds a
// DonationPointsConfig with sensible defaults when any setting is
// missing or unparseable.
func pointsConfigFromSettings(c *gin.Context, h *Handlers) DonationPointsConfig {
	ctx := c.Request.Context()
	get := func(k string, fallback float64) float64 {
		v, _ := h.deps.Settings.GetSetting(ctx, k)
		if v == "" {
			return fallback
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fallback
		}
		return f
	}
	return DonationPointsConfig{
		PointsPerDollar:     get("donate_points_per_dollar", 1.0),
		MultiplierPer10:     get("donate_multiplier_per_10", 1.2),
		DonatorThresholdUSD: get("donate_donator_threshold_usd", 5),
	}
}

// lockingGroupSet returns the set of group names whose full state hides
// the donate addresses. Default is just {"site"} — admin can override
// via the donate_locking_groups CSV setting.
func lockingGroupSet(c *gin.Context, h *Handlers) map[string]bool {
	v, _ := h.deps.Settings.GetSetting(c.Request.Context(), "donate_locking_groups")
	if v == "" {
		return map[string]bool{"site": true}
	}
	out := make(map[string]bool)
	for _, g := range strings.Split(v, ",") {
		g = strings.TrimSpace(g)
		if g != "" {
			out[g] = true
		}
	}
	return out
}

// DonatePage renders /help/donate. Anonymous donors are welcomed; the
// page's points-credit pitch only fires for logged-in users (the
// "log in first to earn points" hint sits above the addresses).
//
// When Deps.IsDonateEnabled() is false, only admins can render
// the page — non-admin requests get a 404 (so the existence of the
// route is invisible while the operator previews on live). Admins
// always see the page so they can validate the layout / costs before
// flipping the switch.
func (h *Handlers) DonatePage(c *gin.Context) {
	if !h.deps.IsDonateEnabled() {
		user, _ := h.auth.CurrentUser(c)
		if !user.AtLeast(core.RoleMod) {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
	}
	ctx := c.Request.Context()

	// Cost line items + derived per-group goals.
	costs, _ := h.store.ListSiteCosts(ctx, false /* exclude inactive */)

	// Period buckets shared across every group — same donations
	// contribute to every group's progress ("all apply" semantics).
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	yearStart := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	monthlyRaised, _ := h.store.SumDonationsSince(ctx, monthStart)
	yearlyRaised, _ := h.store.SumDonationsSince(ctx, yearStart)
	monthlyResetAt := monthStart.AddDate(0, 1, 0)
	yearlyResetAt := time.Date(now.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC)

	// One DonationGoalGroup per distinct goal_group with active rows.
	// 'site' is pinned first by the storage query; everything else
	// follows in alphabetical order. lockingGroups decides which
	// groups' fully-funded state hides the addresses on the page.
	groupNames, _ := h.store.DistinctActiveGoalGroups(ctx)
	lockingGroups := lockingGroupSet(c, h)
	groups := make([]*DonationGoalGroup, 0, len(groupNames))
	addressesHidden := len(lockingGroups) > 0 // toggled false below if any locking group is still open
	for _, name := range groupNames {
		mGoal, _ := h.store.SumSiteCostsByGroupPeriod(ctx, name, "monthly")
		yGoal, _ := h.store.SumSiteCostsByGroupPeriod(ctx, name, "yearly")
		g := &DonationGoalGroup{
			Name:             name,
			Locks:            lockingGroups[name],
			MonthlyGoalUSD:   mGoal,
			MonthlyRaisedUSD: monthlyRaised,
			YearlyGoalUSD:    yGoal,
			YearlyRaisedUSD:  yearlyRaised,
			MonthlyResetAt:   monthlyResetAt,
			YearlyResetAt:    yearlyResetAt,
		}
		// Items for THIS group only. ListSiteCosts sorts by
		// (goal_group, category, sort_order, label) which is great for
		// the admin's category-grouped view but ignores SortOrder as
		// the PRIMARY key on the public page. Re-sort here so the cards
		// in "Where Your Donations Go" appear in the order an admin set
		// via the SortOrder column — regardless of which category each
		// item belongs to.
		for _, c := range costs {
			if c.GoalGroup == name {
				g.Items = append(g.Items, c)
			}
		}
		sort.SliceStable(g.Items, func(i, j int) bool {
			if g.Items[i].SortOrder != g.Items[j].SortOrder {
				return g.Items[i].SortOrder < g.Items[j].SortOrder
			}
			return g.Items[i].Label < g.Items[j].Label
		})
		groups = append(groups, g)
		if g.Locks && !g.FullyFunded() {
			// At least one locking group is still open → keep
			// addresses visible. Once every locking group is
			// fully funded, the visibility toggle stays true.
			addressesHidden = false
		}
	}
	// If no locking group exists at all (admin removed 'site' from
	// the locking list with no replacement), keep addresses visible.
	if len(lockingGroups) == 0 {
		addressesHidden = false
	}

	// Donation packages (migration 261). Each active package shows
	// "buy a slot" cards with live stock; sold-out cards move to a
	// "Funded ✓" subsection. Stock is counted YTD — the same yearly
	// boundary used for the Yearly goal bar — so resetting Jan 1
	// reopens every package automatically.
	pkgs, _ := h.store.ListDonationPackages(ctx, false /* exclude inactive */)
	pkgUsage, _ := h.store.CountDonationsPerPackageSince(ctx, yearStart)
	pkgViews := make([]*DonationPackageView, 0, len(pkgs))
	fundedPkgs := make([]*DonationPackageView, 0)
	for _, p := range pkgs {
		v := &DonationPackageView{DonationPackage: *p}
		v.Recompute(pkgUsage[p.ID])
		if v.Funded {
			fundedPkgs = append(fundedPkgs, v)
		} else {
			pkgViews = append(pkgViews, v)
		}
	}

	// Annual total = sum of every monthly cost row × 12 + sum of
	// every yearly cost row. Computed across ALL groups since a
	// reader wants to see the bottom-line cost of running the site
	// regardless of how admin happens to slice costs into goal
	// groups. Done in Go (not the template) because template
	// arithmetic on floats is fiddly and a prior bug shipped a
	// broken `add (printf ...) (printf ...)` once already.
	var totalMonthlyUSD, totalYearlyUSD float64
	for _, g := range groups {
		totalMonthlyUSD += g.MonthlyGoalUSD
		totalYearlyUSD += g.YearlyGoalUSD
	}
	totalAnnualUSD := totalMonthlyUSD*12 + totalYearlyUSD

	// Receive addresses + recent leaderboard cap.
	btcAddr, _ := h.deps.Settings.GetSetting(ctx, "donate_addr_btc")
	ethAddr, _ := h.deps.Settings.GetSetting(ctx, "donate_addr_eth")
	xmrAddr, _ := h.deps.Settings.GetSetting(ctx, "donate_addr_xmr")
	recentN, _ := h.deps.Settings.GetSetting(ctx, "donate_recent_count")
	limit, _ := strconv.Atoi(recentN)
	if limit <= 0 {
		limit = 20
	}
	recent, _ := h.store.ListRecentDonations(ctx, limit)

	// Points curve preview — applies the live admin-configured curve
	// to a fixed list of dollar amounts.
	cfg := pointsConfigFromSettings(c, h)
	preview := make([]DonationPointsRow, 0, len(donatePreviewDollars))
	for _, d := range donatePreviewDollars {
		preview = append(preview, DonationPointsRow{Dollars: d, Points: cfg.PointsForDollars(d)})
	}

	tipJarGoals := loadTipJarGoals(c, h)
	h.render(c, http.StatusOK, "Donate & Costs", "help_donate.html",
		&donatePageVM{
			Groups:          groups,
			PointsConfig:    cfg,
			PointsPreview:   preview,
			BTCAddress:      btcAddr,
			ETHAddress:      ethAddr,
			XMRAddress:      xmrAddr,
			RecentDonations: recent,
			AddressesHidden: addressesHidden,
			TotalMonthlyUSD: totalMonthlyUSD,
			TotalYearlyUSD:  totalYearlyUSD,
			TotalAnnualUSD:  totalAnnualUSD,
			TipJarGoals:     tipJarGoals,
			Tiers:           donorTiers(ctx, h.deps.Settings),
			Packages:        pkgViews,
			FundedPackages:  fundedPkgs,
		})
}
