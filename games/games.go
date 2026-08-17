// Package games is a loon plugin: community point games. Two ship today,
// both modelled on long-running private-tracker traditions:
//
// THE POT (MyAnonaMouse's millionaire's vault): members drop points in, a
// configurable amount per member per day at most. When the pot reaches its
// target, one contributor wins a configured percentage — the draw is
// weighted by contribution — everyone who gave at least the consolation
// threshold is granted a configured reward from the rewards shelf, and the
// next pot opens empty. The house keeps the remainder: points leave the
// economy, which is the point-sink every points economy needs.
//
// CHARITY (also MaM's): a member picks a ratio ceiling and an amount, and
// the points are split evenly among every member under that ratio who has
// still downloaded a real amount — need, not inactivity. Anonymous in both
// directions: recipients see "charity", donors see a count.
//
// Everything crosses seams that already exist: points through core.Points,
// the consolation reward through pluginapi.RewardBySlugGranter (idempotent
// by reference, so a crash between close and grant cannot double-pay), the
// needy through pluginapi.RankStats — the same figures rank promotion runs
// on, because "who is poor" should have exactly one definition.
package games

import (
	"context"
	"crypto/rand"
	"embed"
	"fmt"
	"html/template"
	"log"
	"math/big"
	"sort"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

//go:embed migrations/*.sql
var migrations embed.FS

//go:embed templates/*.html
var tmplFS embed.FS

// CSRFExtension is where the host publishes its per-session token func
// (func(*gin.Context) string) — the rewards.csrf story, one key per plugin.
const CSRFExtension = "games.csrf"

func init() {
	core.RegisterPlugin("games", func() core.Plugin { return &Plugin{} })
}

type Plugin struct {
	core    *core.Core
	st      *PGStore
	granter pluginapi.RewardBySlugGranter // nil = no consolation rewards
	stats   pluginapi.RankStats           // nil = charity unavailable
	tmpl    *template.Template
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "games",
		Version:     "0.1.0",
		Description: "Community point games: the pot (donate daily, one contributor wins it) and charity (points to members in need).",
		Migrations:  migrations,
		Processes:   []string{"web"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	db := c.Storage.SchemaDB(p.Metadata().Name)
	if db == nil || db.DB() == nil {
		return fmt.Errorf("games: Core.Storage.SchemaDB is nil")
	}
	p.st = NewPGStore(db.DB())

	t, err := template.ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		return fmt.Errorf("games: templates: %w", err)
	}
	p.tmpl = t

	return p.registerViews(c)
}

// Start looks up the sibling capabilities. In Start rather than Provision —
// the achievements lesson: Boot runs every plugin's Provision before any
// Start, so this sees the rewards registration whatever the boot order,
// without a hard Requires edge a rewards-less host cannot satisfy.
func (p *Plugin) Start(ctx context.Context) error {
	// The consolation-reward granter, softly: a host without the rewards
	// plugin still gets a working pot — the winner is points through
	// core.Points — it just has nothing to hand the other contributors.
	if v, ok := p.core.Lookup(pluginapi.RewardBySlugGranterName); ok {
		if g, ok := v.(pluginapi.RewardBySlugGranter); ok {
			p.granter = g
		} else {
			return fmt.Errorf("games: %q is %T, not pluginapi.RewardBySlugGranter",
				pluginapi.RewardBySlugGranterName, v)
		}
	} else {
		log.Printf("games: %q not registered — the pot pays its winner but grants no consolation reward",
			pluginapi.RewardBySlugGranterName)
	}
	// The member figures, softly: charity NEEDS them (it is how need is
	// found), so without the seam that page says so instead of guessing.
	if v, ok := p.core.Lookup(pluginapi.RankStatsName); ok {
		if s, ok := v.(pluginapi.RankStats); ok {
			p.stats = s
		} else {
			return fmt.Errorf("games: %q is %T, not pluginapi.RankStats", pluginapi.RankStatsName, v)
		}
	} else {
		log.Printf("games: %q not registered — charity is unavailable (no member figures to find need with)",
			pluginapi.RankStatsName)
	}
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }

func (p *Plugin) csrfToken(gc *gin.Context) string {
	if v, ok := p.core.Lookup(CSRFExtension); ok {
		if fn, ok := v.(func(*gin.Context) string); ok {
			return fn(gc)
		}
	}
	return ""
}

// ── the pot ─────────────────────────────────────────────────────────────

// potOutcome is what a donation did, for the redirect message.
type potOutcome struct {
	Donated int64
	// Closed is set when this donation filled the pot; WonBy/Winnings say
	// who the draw picked and what they took.
	Closed   bool
	WonBy    int64
	Winnings int64
}

// donate runs one member's donation and, when it fills the pot, the close.
func (p *Plugin) donate(ctx context.Context, userID, amount int64) (potOutcome, error) {
	cfg, err := p.st.Settings(ctx)
	if err != nil {
		return potOutcome{}, err
	}
	if amount <= 0 {
		return potOutcome{}, errBadInput("pick an amount")
	}
	cyc, err := p.st.OpenCycle(ctx, cfg.PotTarget)
	if err != nil {
		return potOutcome{}, err
	}
	today, err := p.st.DonatedToday(ctx, cyc.ID, userID)
	if err != nil {
		return potOutcome{}, err
	}
	if today+amount > cfg.PotDailyMax {
		left := cfg.PotDailyMax - today
		if left < 0 {
			left = 0
		}
		return potOutcome{}, errBadInput(fmt.Sprintf("the daily limit is %d points — you have %d left today", cfg.PotDailyMax, left))
	}

	// Deduct BEFORE recording: a member whose balance refuses has donated
	// nothing, and the ledger row is the receipt.
	if _, err := p.core.Points.Deduct(ctx, userID, int(amount), "spend_pot_donation",
		"Dropped points into the pot", cyc.ID); err != nil {
		return potOutcome{}, err
	}
	total, err := p.st.AddDonation(ctx, cyc.ID, userID, amount)
	if err != nil {
		// The points are spent and the row failed — refund rather than
		// swallow, mirroring the store's unwind.
		if _, rerr := p.core.Points.Refund(ctx, userID, int(amount), "spend_pot_donation",
			"Pot donation failed — refunded", cyc.ID); rerr != nil {
			log.Printf("games: refund after failed donation: %v", rerr)
		}
		return potOutcome{}, err
	}
	out := potOutcome{Donated: amount}
	if total < cyc.Target {
		return out, nil
	}

	// The pot is full. CloseCycle elects exactly one closer — concurrent
	// donors both land here, one wins the UPDATE, the other's pot simply
	// closed under them.
	closed, err := p.st.CloseCycle(ctx, cyc.ID)
	if err != nil || !closed {
		return out, err
	}
	totals, err := p.st.ContributorTotals(ctx, cyc.ID)
	if err != nil {
		return out, err
	}
	winner := pickWeighted(rand.Reader, totals)
	winnings := total * int64(cfg.PotWinPct) / 100
	if winner != 0 && winnings > 0 {
		if _, err := p.core.Points.Award(ctx, winner, int(winnings), "earn_pot_win",
			fmt.Sprintf("Won the pot (%d of %d points)", winnings, total), cyc.ID); err != nil {
			log.Printf("games: pot payout: %v", err)
		}
	}
	if err := p.st.RecordWinner(ctx, cyc.ID, winner, winnings); err != nil {
		log.Printf("games: record winner: %v", err)
	}
	// Consolation rewards, idempotent by reference — a retry after a crash
	// here cannot double-grant, which is the whole reason the granter takes
	// one.
	if p.granter != nil && cfg.PotRewardSlug != "" {
		ref := fmt.Sprintf("pot:%d", cyc.ID)
		for uid, gave := range totals {
			if gave >= cfg.PotRewardMin {
				if _, err := p.granter.GrantOneOff(ctx, uid, cfg.PotRewardSlug, ref); err != nil {
					log.Printf("games: consolation grant user %d: %v", uid, err)
				}
			}
		}
	}
	out.Closed, out.WonBy, out.Winnings = true, winner, winnings
	return out, nil
}

// pickWeighted draws one contributor, weight ∝ total given. crypto/rand
// because a draw people paid into deserves a generator nobody can argue
// with, and the cost is once per cycle. Deterministic iteration (sorted
// ids) so equal randomness cannot favour map order.
func pickWeighted(r interface{ Read([]byte) (int, error) }, totals map[int64]int64) int64 {
	var sum int64
	ids := make([]int64, 0, len(totals))
	for id, n := range totals {
		if n > 0 {
			ids = append(ids, id)
			sum += n
		}
	}
	if sum <= 0 {
		return 0
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	nBig, err := rand.Int(r, big.NewInt(sum))
	if err != nil {
		// A failed entropy read picks the largest contributor rather than
		// nobody — the pot must not close without a winner.
		var best int64
		for _, id := range ids {
			if totals[id] > totals[best] {
				best = id
			}
		}
		return best
	}
	n := nBig.Int64()
	for _, id := range ids {
		n -= totals[id]
		if n < 0 {
			return id
		}
	}
	return ids[len(ids)-1]
}

// ── charity ─────────────────────────────────────────────────────────────

// charityRatios are the ceilings the form offers. A closed set rather than
// a free float: "who counts as in need" is a site-culture decision, and a
// donor typing 47.3 is not choosing a policy, they are making a typo.
var charityRatios = []float64{0.1, 0.25, 0.5, 0.75, 1.0}

func validCharityRatio(r float64) bool {
	for _, v := range charityRatios {
		if r == v {
			return true
		}
	}
	return false
}

// give distributes one member's charity. Returns how many members it
// reached.
func (p *Plugin) give(ctx context.Context, donorID, amount int64, ratioMax float64) (int, error) {
	if p.stats == nil {
		return 0, errBadInput("charity is unavailable on this site — there are no member figures to find need with")
	}
	cfg, err := p.st.Settings(ctx)
	if err != nil {
		return 0, err
	}
	if amount < cfg.CharityMin || amount > cfg.CharityMax {
		return 0, errBadInput(fmt.Sprintf("charity is between %d and %d points", cfg.CharityMin, cfg.CharityMax))
	}
	if !validCharityRatio(ratioMax) {
		return 0, errBadInput("pick one of the offered ratio bands")
	}
	all, err := p.stats.AllStats(ctx)
	if err != nil {
		return 0, err
	}
	floor := cfg.CharityDLFloorGB << 30
	var needy []int64
	for uid, s := range all {
		if uid == donorID {
			continue // charity is for others; self-gifting would be a points loop
		}
		if s.Ratio <= ratioMax && s.Downloaded >= floor {
			needy = append(needy, uid)
		}
	}
	if len(needy) == 0 {
		return 0, errBadInput("nobody currently matches that band — there is no one to give to")
	}
	// Poorest first, so the remainder of an uneven split lands on the
	// members who need it most rather than on map order.
	sort.Slice(needy, func(i, j int) bool {
		if all[needy[i]].Ratio != all[needy[j]].Ratio {
			return all[needy[i]].Ratio < all[needy[j]].Ratio
		}
		return needy[i] < needy[j]
	})

	if _, err := p.core.Points.Deduct(ctx, donorID, int(amount), "spend_charity",
		fmt.Sprintf("Charity to %d members", len(needy)), 0); err != nil {
		return 0, err
	}
	giftID, err := p.st.RecordCharity(ctx, donorID, amount, ratioMax, len(needy))
	if err != nil {
		log.Printf("games: record charity: %v", err)
	}
	shares := splitEven(amount, len(needy))
	for i, uid := range needy {
		if shares[i] <= 0 {
			continue
		}
		if _, err := p.core.Points.Award(ctx, uid, int(shares[i]), "earn_charity",
			"Charity from a generous member", giftID); err != nil {
			log.Printf("games: charity award user %d: %v", uid, err)
		}
	}
	return len(needy), nil
}

// splitEven divides amount into n shares differing by at most one, the
// extra going to the FIRST shares — which give sorted poorest-first.
func splitEven(amount int64, n int) []int64 {
	shares := make([]int64, n)
	if n <= 0 {
		return shares
	}
	base, rem := amount/int64(n), amount%int64(n)
	for i := range shares {
		shares[i] = base
		if int64(i) < rem {
			shares[i]++
		}
	}
	return shares
}

// errBadInput is a member-facing refusal, distinct from a system error so
// the handlers can show the sentence rather than "something went wrong".
type errBadInput string

func (e errBadInput) Error() string { return string(e) }
