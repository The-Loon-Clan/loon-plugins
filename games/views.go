package games

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// registerViews publishes the two member pages and the admin settings
// section, all through the view system — no host templates, no host routes.
func (p *Plugin) registerViews(c *core.Core) error {
	if err := c.RegisterView(core.View{
		Slug: "pot", Title: "The Pot", Slot: core.SlotSitePage,
		MinRole: core.RoleUser,
		Nav:     core.NavHint{Group: "Community"},
		Render:  p.renderPot,
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"donate": p.actionDonate,
		},
	}); err != nil {
		return err
	}
	if err := c.RegisterView(core.View{
		Slug: "charity", Title: "Charity", Slot: core.SlotSitePage,
		MinRole: core.RoleUser,
		Nav:     core.NavHint{Group: "Community"},
		Render:  p.renderCharity,
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"give": p.actionGive,
		},
	}); err != nil {
		return err
	}
	return c.RegisterView(core.View{
		Slug: "games", Title: "Games", Slot: core.SlotAdminSettings,
		Render: func(gc *gin.Context) (template.HTML, error) {
			return p.renderSettings(gc)
		},
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"save": p.actionSaveSettings,
		},
	})
}

// ── the pot page ────────────────────────────────────────────────────────

type potVM struct {
	Total, Target  int64
	Pct            int
	DailyMax       int64
	DonatedToday   int64
	LeftToday      int64
	YourCycleTotal int64
	WinPct         int
	RewardMin      int64
	RewardSlug     string
	LastWinnerName string
	LastWinnings   int64
	Msg, Err       string
	CSRFToken      string
}

func (p *Plugin) renderPot(gc *gin.Context) (template.HTML, error) {
	ctx := gc.Request.Context()
	cfg, err := p.st.Settings(ctx)
	if err != nil {
		return "", err
	}
	cyc, err := p.st.OpenCycle(ctx, cfg.PotTarget)
	if err != nil {
		return "", err
	}
	vm := potVM{
		Total: cyc.Total, Target: cyc.Target,
		DailyMax: cfg.PotDailyMax, WinPct: cfg.PotWinPct,
		RewardMin: cfg.PotRewardMin, RewardSlug: cfg.PotRewardSlug,
		Msg: gc.Query("msg"), Err: gc.Query("err"),
		CSRFToken: p.csrfToken(gc),
	}
	if cyc.Target > 0 {
		vm.Pct = int(cyc.Total * 100 / cyc.Target)
		if vm.Pct > 100 {
			vm.Pct = 100
		}
	}
	if u, ok := p.core.Auth.CurrentUser(gc); ok && u != nil {
		if n, err := p.st.DonatedToday(ctx, cyc.ID, u.ID); err == nil {
			vm.DonatedToday = n
			vm.LeftToday = cfg.PotDailyMax - n
			if vm.LeftToday < 0 {
				vm.LeftToday = 0
			}
		}
		if n, err := p.st.UserCycleTotal(ctx, cyc.ID, u.ID); err == nil {
			vm.YourCycleTotal = n
		}
	}
	if last, ok, _ := p.st.LastClosed(ctx); ok && last.WinnerUserID.Valid {
		vm.LastWinnings = last.WinnerPoints
		vm.LastWinnerName = p.username(gc, last.WinnerUserID.Int64)
	}
	var buf bytes.Buffer
	if err := p.tmpl.ExecuteTemplate(&buf, "pot.html", vm); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

func (p *Plugin) username(gc *gin.Context, id int64) string {
	if p.core != nil && p.core.Users != nil {
		if u, err := p.core.Users.GetByID(gc.Request.Context(), id); err == nil && u != nil {
			return u.Username
		}
	}
	return fmt.Sprintf("member #%d", id)
}

func (p *Plugin) actionDonate(gc *gin.Context) (template.HTML, error) {
	u, ok := p.core.Auth.CurrentUser(gc)
	if !ok || u == nil {
		gc.Redirect(http.StatusSeeOther, "/login")
		return "", nil
	}
	amount, _ := strconv.ParseInt(strings.TrimSpace(gc.PostForm("amount")), 10, 64)
	out, err := p.donate(gc.Request.Context(), u.ID, amount)
	switch {
	case err == nil && out.Closed:
		gc.Redirect(http.StatusSeeOther, "/p/pot?msg="+url.QueryEscape(
			fmt.Sprintf("Your %d points FILLED the pot — %s takes %d points, and a new pot is open!",
				out.Donated, p.username(gc, out.WonBy), out.Winnings)))
	case err == nil:
		gc.Redirect(http.StatusSeeOther, "/p/pot?msg="+url.QueryEscape(
			fmt.Sprintf("%d points in the pot — good luck!", out.Donated)))
	default:
		gc.Redirect(http.StatusSeeOther, "/p/pot?err="+url.QueryEscape(memberErr(err)))
	}
	return "", nil
}

// ── the charity page ────────────────────────────────────────────────────

type charityVM struct {
	Min, Max    int64
	FloorGB     int64
	Ratios      []float64
	Gifts       int64
	PointsMoved int64
	Available   bool
	Msg, Err    string
	CSRFToken   string
}

func (p *Plugin) renderCharity(gc *gin.Context) (template.HTML, error) {
	ctx := gc.Request.Context()
	cfg, err := p.st.Settings(ctx)
	if err != nil {
		return "", err
	}
	vm := charityVM{
		Min: cfg.CharityMin, Max: cfg.CharityMax, FloorGB: cfg.CharityDLFloorGB,
		Ratios: charityRatios, Available: p.stats != nil,
		Msg: gc.Query("msg"), Err: gc.Query("err"),
		CSRFToken: p.csrfToken(gc),
	}
	if g, pts, err := p.st.CharityTotals(ctx); err == nil {
		vm.Gifts, vm.PointsMoved = g, pts
	}
	var buf bytes.Buffer
	if err := p.tmpl.ExecuteTemplate(&buf, "charity.html", vm); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

func (p *Plugin) actionGive(gc *gin.Context) (template.HTML, error) {
	u, ok := p.core.Auth.CurrentUser(gc)
	if !ok || u == nil {
		gc.Redirect(http.StatusSeeOther, "/login")
		return "", nil
	}
	amount, _ := strconv.ParseInt(strings.TrimSpace(gc.PostForm("amount")), 10, 64)
	ratio, _ := strconv.ParseFloat(gc.PostForm("ratio"), 64)
	n, err := p.give(gc.Request.Context(), u.ID, amount, ratio)
	if err != nil {
		gc.Redirect(http.StatusSeeOther, "/p/charity?err="+url.QueryEscape(memberErr(err)))
		return "", nil
	}
	gc.Redirect(http.StatusSeeOther, "/p/charity?msg="+url.QueryEscape(
		fmt.Sprintf("Your %d points reached %d members. Thank you!", amount, n)))
	return "", nil
}

// memberErr shows a refusal's own sentence and hides everything else.
func memberErr(err error) string {
	var be errBadInput
	if errors.As(err, &be) {
		return string(be)
	}
	if errors.Is(err, core.ErrInsufficientPoints) || strings.Contains(err.Error(), "insufficient") {
		return "you do not have that many points"
	}
	return "something went wrong — try again shortly"
}

// ── admin settings ──────────────────────────────────────────────────────

type settingsVM struct {
	Config
	RewardOptions []string
	Msg           string
	CSRFToken     string
}

func (p *Plugin) renderSettings(gc *gin.Context) (template.HTML, error) {
	ctx := gc.Request.Context()
	cfg, err := p.st.Settings(ctx)
	if err != nil {
		return "", err
	}
	vm := settingsVM{Config: cfg, Msg: gc.Query("msg"), CSRFToken: p.csrfToken(gc)}
	if p.granter != nil {
		if slugs, err := p.granter.ListOneOff(ctx); err == nil {
			vm.RewardOptions = slugs
		}
	}
	var buf bytes.Buffer
	if err := p.tmpl.ExecuteTemplate(&buf, "games_settings.html", vm); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

func (p *Plugin) actionSaveSettings(gc *gin.Context) (template.HTML, error) {
	ctx := gc.Request.Context()
	// Every knob the form carries, written as sent; Settings() re-validates
	// on read (bad values keep defaults), so a typo degrades instead of
	// breaking the games.
	for _, key := range []string{
		"pot_target", "pot_win_pct", "pot_daily_max",
		"pot_reward_min", "pot_reward_slug",
		"charity_min", "charity_max", "charity_dl_floor_gb",
	} {
		if v, ok := gc.GetPostForm(key); ok {
			if err := p.st.SaveSetting(ctx, key, strings.TrimSpace(v)); err != nil {
				return "", err
			}
		}
	}
	gc.Redirect(http.StatusSeeOther, "/admin/settings/games?msg="+url.QueryEscape("saved"))
	return "", nil
}
