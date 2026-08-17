package rewards

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/blob"
)

// The achievements half of the admin page.
//
// Until now achievements could only be created by hand in SQL: the store methods
// existed (CreateAchievement, SetAchievementEnabled) with no caller, so the
// catalogue could grow only through opssql. This is the missing surface.
//
// Parsing is split from storage on purpose. parseNewAchievement is a pure
// function over form values, so every refusal below is unit-tested; the insert
// itself, its validation and the disabled-on-create rule stay covered by the PG
// tests, which is where a constraint can actually be exercised.

// achAdminVM is the achievements definition page.
type achAdminVM struct {
	Msg, Err      string
	Achievements  []achievementVM
	MetricOptions []string
	// OneOffRewards is the payout picker — achievements latch once, so only
	// one_off rewards can back them.
	OneOffRewards []Reward
	// IconOptions is the host's sprite vocabulary, arriving through the
	// rewards.icons registry key. Empty when no host published one, in which
	// case the field is free text: the plugin must not invent names for
	// symbols it has never seen.
	IconOptions []string
	// CanUpload is whether a host registered a file store (rewards.files).
	// The upload control is HIDDEN without one rather than rendered broken.
	CanUpload bool
}

// renderAchievementsPage draws the definition page.
func (p *Plugin) renderAchievementsPage(ctx context.Context, msg, errMsg string) (template.HTML, error) {
	vm := achAdminVM{Msg: msg, Err: errMsg,
		MetricOptions: p.metricOptions(),
		IconOptions:   p.iconOptions,
		CanUpload:     p.files != nil,
	}
	rewards, err := p.admin.ListRewards(ctx)
	if err != nil {
		return "", err
	}
	for _, r := range rewards {
		if r.Kind == KindOneOff {
			vm.OneOffRewards = append(vm.OneOffRewards, r)
		}
	}
	if defs, err := p.store.ListAchievementDefs(ctx); err == nil {
		vm.Achievements = achievementRows(defs, rewards)
	}
	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "achievements_admin.html", vm); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

// achievementVM is one row of the admin table, with the reward resolved so the
// page can say what it pays rather than printing a foreign key.
type achievementVM struct {
	AchievementDef
	RewardSlug string
	// Payable is false when the reward behind it cannot pay — disabled, or with
	// no payout lines. The completion path refuses these, so an achievement that
	// looks live and can never complete is exactly what an admin needs pointed
	// out rather than left to discover.
	Payable bool
	Why     string
}

// achievementRows joins definitions to their rewards for the table.
func achievementRows(defs []AchievementDef, rewards []Reward) []achievementVM {
	byID := make(map[int64]Reward, len(rewards))
	for _, r := range rewards {
		byID[r.ID] = r
	}
	out := make([]achievementVM, 0, len(defs))
	for _, d := range defs {
		row := achievementVM{AchievementDef: d, Payable: true}
		r, ok := byID[d.RewardID]
		switch {
		case !ok:
			row.RewardSlug, row.Payable = "?", false
			row.Why = "reward missing"
		case !r.Enabled:
			row.RewardSlug, row.Payable = r.Slug, false
			row.Why = "reward disabled"
		case len(r.Payouts) == 0:
			row.RewardSlug, row.Payable = r.Slug, false
			row.Why = "reward has no payout lines"
		case r.Kind != KindOneOff:
			row.RewardSlug, row.Payable = r.Slug, false
			row.Why = "reward is " + string(r.Kind) + "; only one_off is supported"
		default:
			row.RewardSlug = r.Slug
		}
		out = append(out, row)
	}
	return out
}

// metricOptions lists the metrics something is actually registered to score,
// so the form offers a picker rather than a free-text box.
//
// An achievement on an unregistered metric is inert, not invalid — the same rule
// as a payout kind with no handler — so free text stays possible. But typing one
// by accident is the failure that looks healthy, and the validator only catches
// it after the fact.
func (p *Plugin) metricOptions() []string {
	names := p.metricNames()
	out := make([]string, 0, len(names))
	for n := range names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// parseNewAchievement reads the create form.
//
// Refuses here rather than at the store for the things a form can get wrong, so
// the operator gets the reason instead of a constraint violation. The store
// validates independently — this is a better message, not the guarantee.
func parseNewAchievement(form url.Values) (NewAchievement, error) {
	a := NewAchievement{
		Slug:        strings.TrimSpace(form.Get("slug")),
		Name:        strings.TrimSpace(form.Get("name")),
		Description: strings.TrimSpace(form.Get("description")),
		Metric:      strings.TrimSpace(form.Get("metric")),
		Trigger:     strings.TrimSpace(form.Get("trigger")),
		Hidden:      form.Get("hidden") != "",
	}
	if a.Slug == "" {
		return a, errField("slug is required")
	}
	if a.Name == "" {
		// Defaulted rather than refused: the slug is a usable label and an
		// achievement with a blank name renders as an empty badge.
		a.Name = a.Slug
	}
	if a.Metric == "" {
		return a, errField("metric is required — it is what the threshold counts")
	}

	rewardID, err := strconv.ParseInt(strings.TrimSpace(form.Get("reward_id")), 10, 64)
	if err != nil || rewardID <= 0 {
		return a, errField("pick the reward this achievement pays")
	}
	a.RewardID = rewardID

	threshold, err := strconv.ParseInt(strings.TrimSpace(form.Get("threshold")), 10, 64)
	if err != nil {
		return a, errField("threshold must be a whole number")
	}
	if threshold <= 0 {
		// The schema CHECKs this too. Saying so here means the operator reads a
		// sentence rather than a Postgres error, and a threshold of 0 would
		// complete for every member on the next tick.
		return a, errField("threshold must be greater than zero — a threshold of 0 " +
			"completes for every member on the first pass")
	}
	a.Threshold = threshold

	if s := strings.TrimSpace(form.Get("ordinal")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return a, errField("ordinal must be a whole number")
		}
		a.Ordinal = n
	}
	return a, nil
}

type fieldError string

func (e fieldError) Error() string { return string(e) }
func errField(s string) error      { return fieldError(s) }

func (p *Plugin) actionCreateAchievement(gc *gin.Context) (template.HTML, error) {
	if p.admin == nil {
		return p.redirect(gc, "/admin/p/achievements", "", "no admin store configured")
	}
	// Multipart because of the image; a plain form still parses through it.
	if err := gc.Request.ParseMultipartForm(5 << 20); err != nil && err != http.ErrNotMultipart {
		return p.redirect(gc, "/admin/p/achievements", "", "could not read the form")
	}
	a, err := parseNewAchievement(gc.Request.PostForm)
	if err != nil {
		return p.redirect(gc, "/admin/p/achievements", "", err.Error())
	}
	a.Icon = strings.TrimSpace(gc.PostForm("icon"))
	// The badge image, when one was sent. Sniffed against blob.ImageExts —
	// the shared allow-list every upload path uses — and named by slug, so
	// re-uploading replaces rather than accumulating orphans.
	if file, hdr, ferr := gc.Request.FormFile("image"); ferr == nil {
		defer func() { _ = file.Close() }()
		if p.files == nil {
			return p.redirect(gc, "/admin/p/achievements", "",
				"this host has no file store wired (rewards.files), so badge images cannot be uploaded")
		}
		if hdr.Size > 2<<20 {
			return p.redirect(gc, "/admin/p/achievements", "", "badge image over 2 MiB")
		}
		data, rerr := io.ReadAll(io.LimitReader(file, 2<<20+1))
		if rerr != nil {
			return p.redirect(gc, "/admin/p/achievements", "", "could not read the image")
		}
		ext, ok := blob.ImageExts[http.DetectContentType(data)]
		if !ok {
			return p.redirect(gc, "/admin/p/achievements", "", "badge image must be JPEG, PNG, GIF or WebP")
		}
		url, serr := p.files.Save(gc.Request.Context(), "achievement-badges/"+a.Slug+ext, data)
		if serr != nil {
			return p.redirect(gc, "/admin/p/achievements", "", fmt.Sprintf("saving image: %v", serr))
		}
		a.ImagePath = url
	}
	if _, err := p.admin.CreateAchievement(gc.Request.Context(), a); err != nil {
		return p.redirect(gc, "/admin/p/achievements", "", err.Error())
	}
	// Created DISABLED, and the message says so: enabling backfills it to
	// everyone already past the threshold on the next tick, which is a separate
	// and deliberate act.
	return p.redirect(gc, "/admin/p/achievements",
		"Created "+a.Slug+" — DISABLED. Enabling it awards it to everyone already past "+
			strconv.FormatInt(a.Threshold, 10)+", silently, on the next job tick.", "")
}

func (p *Plugin) actionToggleAchievement(gc *gin.Context) (template.HTML, error) {
	if p.admin == nil {
		return p.redirect(gc, "/admin/p/achievements", "", "no admin store configured")
	}
	id, err := strconv.ParseInt(gc.PostForm("id"), 10, 64)
	if err != nil || id <= 0 {
		return p.redirect(gc, "/admin/p/achievements", "", "bad achievement id")
	}
	on := gc.PostForm("on") == "1"
	if err := p.admin.SetAchievementEnabled(gc.Request.Context(), id, on); err != nil {
		return p.redirect(gc, "/admin/p/achievements", "", err.Error())
	}
	if on {
		return p.redirect(gc, "/admin/p/achievements",
			"Enabled. The next job tick backfills it to everyone already qualified, silently.", "")
	}
	return p.redirect(gc, "/admin/p/achievements", "Disabled. Progress is kept; nothing new completes.", "")
}
