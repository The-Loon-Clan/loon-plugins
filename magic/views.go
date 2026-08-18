package magic

import (
	"bytes"
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

var hashPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func (p *Plugin) registerViews(c *core.Core) error {
	if err := c.RegisterView(core.View{
		Slug: "magic", Title: "Magic", Slot: core.SlotSitePage,
		MinRole: core.RoleUser,
		// NOT IN THE MENU. Magic is something you do TO a torrent, so it is
		// reached from one — the torrent page's "Cast magic on this torrent"
		// button, carrying the hash. Listed in a menu it invited a member to
		// arrive with no torrent in mind and be asked for forty hex
		// characters, which is not a thing anyone can answer.
		//
		// Still routed, still linkable, and still the page that shows a
		// member's level and their casts; it simply is not a destination the
		// site advertises browsing to.
		Nav:    core.NavHint{Menu: core.NavHidden},
		Render: p.renderMagic,
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"cast":      p.actionCast,
			"terminate": p.actionTerminate,
		},
	}); err != nil {
		return err
	}
	return c.RegisterView(core.View{
		Slug: "magic", Title: "Magic", Slot: core.SlotAdminSettings,
		Render: func(gc *gin.Context) (template.HTML, error) { return p.renderSettings(gc) },
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"save":        p.actionSaveSettings,
			"buff-create": p.actionCreateBuff,
			"buff-update": p.actionUpdateBuff,
			"buff-toggle": p.actionToggleBuff,
			"buff-delete": p.actionDeleteBuff,
		},
	})
}

// ── the member page ─────────────────────────────────────────────────────

type historyRow struct {
	Magic
	CasterName string
	Status     string // Active / Expired / Terminated
	ShortHash  string
	Name       string // torrent name when the tracker answers
}

func (p *Plugin) renderMagic(gc *gin.Context) (template.HTML, error) {
	ctx := gc.Request.Context()
	cfg, err := p.st.Settings(ctx)
	if err != nil {
		return "", err
	}
	buffs, err := p.st.BuffDefs(ctx)
	if err != nil {
		return "", err
	}

	var level int
	var xp int64
	isAdmin := false
	if u, ok := p.core.Auth.CurrentUser(gc); ok && u != nil {
		if n, err := p.st.XP(ctx, u.ID); err == nil {
			xp, level = n, levelFor(n)
		}
		isAdmin = u.AtLeast(core.RoleAdmin)
	}

	// The hash arrives from a torrent page link, or is typed. Named when the
	// tracker answers.
	hash := strings.ToLower(strings.TrimSpace(gc.Query("hash")))
	torrentName := ""
	if hashPattern.MatchString(hash) && p.torrentInfo != nil {
		if name, _, ok, err := p.torrentInfo(ctx, hash); err == nil && ok {
			torrentName = name
		}
	}

	rows, err := p.st.History(ctx, 50)
	if err != nil {
		return "", err
	}
	var hist []historyRow
	for _, m := range rows {
		r := historyRow{Magic: m, ShortHash: m.InfoHash[:12], CasterName: p.username(gc, m.CasterID)}
		switch {
		case m.TerminatedAt.Valid:
			r.Status = "Terminated"
		case m.EndsAt.Valid && m.EndsAt.Time.Before(time.Now()):
			r.Status = "Expired"
		default:
			r.Status = "Active"
		}
		if p.torrentInfo != nil {
			if name, _, ok, err := p.torrentInfo(ctx, m.InfoHash); err == nil && ok {
				r.Name = name
			}
		}
		hist = append(hist, r)
	}

	var buf bytes.Buffer
	if err := p.tmpl.ExecuteTemplate(&buf, "magic.html", map[string]any{
		"Buffs":       buffs,
		"Hash":        hash,
		"TorrentName": torrentName,
		"XP":          xp,
		"Level":       level,
		"Discount":    discountPct(level),
		"MaxHours":    maxHours(level),
		"Custom":      customAllowed(level),
		"CustomUpMax": cfg.CustomUpMax, "CustomDownMax": cfg.CustomDownMax,
		"History":   hist,
		"IsAdmin":   isAdmin,
		"Msg":       gc.Query("msg"),
		"Err":       gc.Query("err"),
		"CSRFToken": p.csrfToken(gc),
	}); err != nil {
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

func (p *Plugin) actionCast(gc *gin.Context) (template.HTML, error) {
	u, ok := p.core.Auth.CurrentUser(gc)
	if !ok || u == nil {
		gc.Redirect(http.StatusSeeOther, "/login")
		return "", nil
	}
	ctx := gc.Request.Context()
	back := func(msg, err string) (template.HTML, error) {
		q := "msg=" + url.QueryEscape(msg)
		if err != "" {
			q = "err=" + url.QueryEscape(err)
		}
		if h := gc.PostForm("hash"); h != "" {
			q += "&hash=" + url.QueryEscape(h)
		}
		gc.Redirect(http.StatusSeeOther, "/p/magic?"+q)
		return "", nil
	}

	hash := strings.ToLower(strings.TrimSpace(gc.PostForm("hash")))
	if !hashPattern.MatchString(hash) {
		return back("", "that is not a torrent info-hash (40 hex characters)")
	}
	cfg, err := p.st.Settings(ctx)
	if err != nil {
		return "", err
	}
	xp, _ := p.st.XP(ctx, u.ID)
	level := levelFor(xp)

	scope := gc.PostForm("scope")
	var target sql.NullInt64
	switch scope {
	case "all", "self":
	case "user":
		id, err := strconv.ParseInt(strings.TrimSpace(gc.PostForm("target")), 10, 64)
		if err != nil || id <= 0 {
			return back("", "a specified-member cast needs the member's numeric id")
		}
		target = sql.NullInt64{Int64: id, Valid: true}
	default:
		return back("", "pick who the magic is for")
	}

	hours, _ := strconv.Atoi(gc.PostForm("hours"))
	if hours < minHours || hours > maxHours(level) {
		return back("", fmt.Sprintf("duration is %d–%d hours at your level", minHours, maxHours(level)))
	}

	// The buff: a preset, or a custom pair for the practised.
	var up, down float64
	if slug := gc.PostForm("buff"); slug != "custom" {
		d, ok, err := p.st.BuffBySlug(ctx, slug)
		if err != nil {
			return "", err
		}
		if !ok {
			return back("", "pick a buff")
		}
		up, down = d.UpRatio, d.DownRatio
	} else {
		if !customAllowed(level) {
			return back("", "custom ratios unlock at magic level 1 — cast the classics first")
		}
		up, _ = strconv.ParseFloat(gc.PostForm("up"), 64)
		down, _ = strconv.ParseFloat(gc.PostForm("down"), 64)
		if up < 1 || up > cfg.CustomUpMax || down < 0 || down > cfg.CustomDownMax {
			return back("", fmt.Sprintf("custom ratios: upload 1–%.2f, download 0–%.2f", cfg.CustomUpMax, cfg.CustomDownMax))
		}
	}
	if up == 1 && down == 1 {
		return back("", "that buff changes nothing — nothing to cast")
	}

	// Size for the price, when the tracker can say. Also the existence
	// check: a cast on a hash the tracker does not serve is a typo, and
	// points spent on a typo come back as a complaint.
	var size int64
	if p.torrentInfo != nil {
		name, sz, ok, err := p.torrentInfo(ctx, hash)
		if err == nil && !ok {
			return back("", "no torrent with that info-hash")
		}
		_ = name
		size = sz
	}

	cost := applyDiscount(castCost(cfg, scope, size, up, down, hours), discountPct(level))
	if _, err := p.core.Points.Deduct(ctx, u.ID, int(cost), "spend_magic",
		fmt.Sprintf("Cast %.2fx/%.2fx magic for %dh", up, down, hours), 0); err != nil {
		return back("", fmt.Sprintf("this cast costs %d points — you do not have that many", cost))
	}
	comment := strings.TrimSpace(gc.PostForm("comment"))
	if len(comment) > 240 {
		comment = comment[:240]
	}
	if _, err := p.st.Cast(ctx, Magic{
		CasterID: u.ID, InfoHash: hash, Scope: scope, TargetUserID: target,
		UpRatio: up, DownRatio: down, Hours: hours, Cost: cost, Comment: comment,
	}); err != nil {
		if _, rerr := p.core.Points.Refund(ctx, u.ID, int(cost), "spend_magic",
			"Magic failed — refunded", 0); rerr != nil {
			return "", rerr
		}
		return back("", "something went wrong — try again shortly")
	}
	// Practice: the spend is the experience.
	if err := p.st.AddXP(ctx, u.ID, cost); err == nil {
		if nl := levelFor(xp + cost); nl > level {
			return back(fmt.Sprintf("Magic cast for %d points — and you reached magic level %d!", cost, nl), "")
		}
	}
	return back(fmt.Sprintf("Magic cast for %d points — it is live now.", cost), "")
}

func (p *Plugin) actionTerminate(gc *gin.Context) (template.HTML, error) {
	u, ok := p.core.Auth.CurrentUser(gc)
	if !ok || u == nil || !u.AtLeast(core.RoleAdmin) {
		gc.Redirect(http.StatusSeeOther, "/p/magic?err="+url.QueryEscape("terminating a promotion is an admin act"))
		return "", nil
	}
	id, _ := strconv.ParseInt(gc.PostForm("id"), 10, 64)
	if id > 0 {
		if _, err := p.st.Terminate(gc.Request.Context(), id, u.ID); err != nil {
			gc.Redirect(http.StatusSeeOther, "/p/magic?err="+url.QueryEscape("terminate failed"))
			return "", nil
		}
	}
	gc.Redirect(http.StatusSeeOther, "/p/magic?msg="+url.QueryEscape("Terminated — the record stays"))
	return "", nil
}

// ── admin settings ──────────────────────────────────────────────────────

func (p *Plugin) renderSettings(gc *gin.Context) (template.HTML, error) {
	cfg, err := p.st.Settings(gc.Request.Context())
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	// The multiplier catalogue, edited on the same page as the prices that
	// scale it. It had no editor at all: the six classics arrived through
	// EnsureBuffDefs and a seventh needed a SQL statement.
	defs, derr := p.st.AllBuffDefs(gc.Request.Context())
	if derr != nil {
		// Non-fatal: the prices above are still worth showing, and an empty
		// catalogue panel is a visible symptom where a 500 hides everything.
		defs = nil
	}
	if err := p.tmpl.ExecuteTemplate(&buf, "magic_settings.html", map[string]any{
		"Config":    cfg,
		"Buffs":     defs,
		"Msg":       gc.Query("msg"),
		"Err":       gc.Query("err"),
		"CSRFToken": p.csrfToken(gc),
	}); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

func (p *Plugin) actionSaveSettings(gc *gin.Context) (template.HTML, error) {
	ctx := gc.Request.Context()
	for _, key := range []string{"base_all", "base_self", "base_user", "avg_size_gb", "custom_up_max", "custom_down_max"} {
		if v, ok := gc.GetPostForm(key); ok {
			if err := p.st.SaveSetting(ctx, key, strings.TrimSpace(v)); err != nil {
				return "", err
			}
		}
	}
	gc.Redirect(http.StatusSeeOther, "/admin/settings/magic?msg="+url.QueryEscape("saved"))
	return "", nil
}

// ── the multiplier catalogue ────────────────────────────────────────────
//
// The six classics are ensured by slug at Start and never overwritten, so an
// operator's edits survive a restart. What was missing was any way to make the
// seventh, or to change the ratios of the six: those were SQL statements.

// buffFromForm reads a definition off either form.
func buffFromForm(gc *gin.Context) BuffDef {
	d := BuffDef{
		Slug: strings.TrimSpace(gc.PostForm("slug")),
		Name: strings.TrimSpace(gc.PostForm("name")),
	}
	d.UpRatio, _ = strconv.ParseFloat(strings.TrimSpace(gc.PostForm("up_ratio")), 64)
	d.DownRatio, _ = strconv.ParseFloat(strings.TrimSpace(gc.PostForm("down_ratio")), 64)
	d.Ordinal, _ = strconv.Atoi(strings.TrimSpace(gc.PostForm("ordinal")))
	return d
}

func (p *Plugin) settingsRedirect(gc *gin.Context, msg, errMsg string) (template.HTML, error) {
	q := "?msg=" + url.QueryEscape(msg)
	if errMsg != "" {
		q = "?err=" + url.QueryEscape(errMsg)
	}
	gc.Redirect(http.StatusSeeOther, "/admin/settings/magic"+q)
	return "", nil
}

func (p *Plugin) actionCreateBuff(gc *gin.Context) (template.HTML, error) {
	d := buffFromForm(gc)
	if !slugPattern.MatchString(d.Slug) {
		return p.settingsRedirect(gc, "", "slugs are lowercase with dashes")
	}
	if err := p.st.CreateBuffDef(gc.Request.Context(), d); err != nil {
		return p.settingsRedirect(gc, "", err.Error())
	}
	return p.settingsRedirect(gc, "added "+d.Slug, "")
}

func (p *Plugin) actionUpdateBuff(gc *gin.Context) (template.HTML, error) {
	id, _ := strconv.ParseInt(gc.PostForm("id"), 10, 64)
	if err := p.st.UpdateBuffDef(gc.Request.Context(), id, buffFromForm(gc)); err != nil {
		return p.settingsRedirect(gc, "", err.Error())
	}
	return p.settingsRedirect(gc, "saved", "")
}

func (p *Plugin) actionToggleBuff(gc *gin.Context) (template.HTML, error) {
	id, _ := strconv.ParseInt(gc.PostForm("id"), 10, 64)
	on := gc.PostForm("on") == "1"
	if err := p.st.SetBuffDefEnabled(gc.Request.Context(), id, on); err != nil {
		return p.settingsRedirect(gc, "", err.Error())
	}
	return p.settingsRedirect(gc, "saved", "")
}

func (p *Plugin) actionDeleteBuff(gc *gin.Context) (template.HTML, error) {
	id, _ := strconv.ParseInt(gc.PostForm("id"), 10, 64)
	if err := p.st.DeleteBuffDef(gc.Request.Context(), id); err != nil {
		return p.settingsRedirect(gc, "", err.Error())
	}
	return p.settingsRedirect(gc, "removed", "")
}

// slugPattern is the shape a new multiplier's slug must have — the same one
// every catalogue in this tree uses, and the same one the schema would accept.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
