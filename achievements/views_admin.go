package achievements

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

// The definition page.
//
// Parsing is split from storage on purpose. parseNewAchievement is a pure
// function over form values, so every refusal below is unit-tested; the
// insert itself, its validation and the disabled-on-create rule stay covered
// at the store, which is where a constraint can actually be exercised.
//
// WHAT WAS LOST IN THE MOVE, on purpose: the old table flagged each row whose
// reward could not pay (disabled, deleted, payout-less, wrong kind) with a
// Payable/Why column, computed eagerly by reading the rewards tables. That
// eager warning required exactly the cross-schema read this split removed, so
// it is gone: the Pays column shows the slug (or "badge only"), and an
// unpayable slug surfaces LAZILY — in the scoring job's log every tick, and
// as paid_at staying NULL on the member's row. Less immediate, honest about
// where the authority lives.

// achAdminVM is the definition page.
type achAdminVM struct {
	Msg, Err     string
	Achievements []AchievementDef
	// MetricOptions is the computed union of registered metric sources and
	// countable declared events — everything a threshold could actually
	// count, with no dependency on rewards' source catalogue.
	MetricOptions []string
	// TriggerOptions is every declared event — fires-shaped, so any of them
	// can complete an achievement outright.
	TriggerOptions []string
	// IconOptions is the host's sprite vocabulary, arriving through the
	// achievements.icons registry key. Empty when no host published one, in
	// which case the field is free text: the plugin must not invent names
	// for symbols it has never seen.
	IconOptions []string
	// CanUpload is whether a host registered a file store
	// (achievements.files). The upload control is HIDDEN without one rather
	// than rendered broken.
	CanUpload bool
}

// renderAdminPage draws the definition page.
func (p *Plugin) renderAdminPage(ctx context.Context, msg, errMsg string) (template.HTML, error) {
	vm := achAdminVM{Msg: msg, Err: errMsg,
		MetricOptions:  p.metricOptions(),
		TriggerOptions: p.triggerOptions(),
		IconOptions:    p.iconOptions,
		CanUpload:      p.files != nil,
	}
	if defs, err := p.store.ListAchievementDefs(ctx); err == nil {
		vm.Achievements = defs
	}
	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "achievements_admin.html", vm); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

// metricOptions is what the metric picker offers: the sorted union of
// registered MetricSource keys and countable declared event names — the two
// ways progress can actually move. Free text stays possible in principle (an
// achievement on an unregistered metric is inert, not invalid), but typing
// one by accident is the failure that looks healthy, so the form offers only
// names something can score.
func (p *Plugin) metricOptions() []string {
	seen := map[string]bool{}
	for name := range p.metrics {
		seen[name] = true
	}
	if p.core != nil {
		for _, d := range p.core.EventDefs() {
			if d.Countable {
				seen[d.Name] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// triggerOptions is what the trigger picker offers: every declared event.
// All of them, not only countable ones — a trigger is "when X happens", which
// is a fires question, and Countable was never about firing.
func (p *Plugin) triggerOptions() []string {
	if p.core == nil {
		return nil
	}
	defs := p.core.EventDefs()
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name)
	}
	sort.Strings(out)
	return out
}

// parseNewAchievement reads the create form.
//
// Refuses here rather than at the store for the things a form can get wrong,
// so the operator gets the reason instead of a constraint violation. The
// store validates independently, and the schema CHECK carries the criterion
// rule — this is a better message, not the guarantee.
func parseNewAchievement(form url.Values) (NewAchievement, error) {
	a := NewAchievement{
		Slug:        strings.TrimSpace(form.Get("slug")),
		Name:        strings.TrimSpace(form.Get("name")),
		Description: strings.TrimSpace(form.Get("description")),
		RewardSlug:  strings.TrimSpace(form.Get("reward_slug")),
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

	// The criterion: a metric with a value, OR a trigger. One or the other —
	// both set would leave nobody able to say which one earns it, and
	// neither leaves nothing that can ever score it.
	switch {
	case a.Metric == "" && a.Trigger == "":
		return a, errField("pick a criterion: a metric with a value, or a trigger event")
	case a.Metric != "" && a.Trigger != "":
		return a, errField("pick a metric OR a trigger, not both — one achievement has one way of being earned")
	}

	if a.Metric != "" {
		threshold, err := strconv.ParseInt(strings.TrimSpace(form.Get("threshold")), 10, 64)
		if err != nil {
			return a, errField("value must be a whole number")
		}
		if threshold <= 0 {
			// The schema CHECKs this too. Saying so here means the operator
			// reads a sentence rather than a Postgres error, and a threshold
			// of 0 would complete for every member on the next tick.
			return a, errField("value must be greater than zero — a value of 0 " +
				"completes for every member on the first pass")
		}
		a.Threshold = threshold
	}
	// A trigger achievement carries no threshold; whatever the form field
	// held is deliberately ignored rather than stored as a number nothing
	// reads.

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
		return p.redirect(gc, "no admin store configured")
	}
	// Multipart because of the image; a plain form still parses through it.
	if err := gc.Request.ParseMultipartForm(5 << 20); err != nil && err != http.ErrNotMultipart {
		return p.redirect(gc, "could not read the form")
	}
	a, err := parseNewAchievement(gc.Request.PostForm)
	if err != nil {
		return p.redirect(gc, err.Error())
	}
	a.Icon = strings.TrimSpace(gc.PostForm("icon"))
	// The badge image, when one was sent. Sniffed against blob.ImageExts —
	// the shared allow-list every upload path uses — and named by slug, so
	// re-uploading replaces rather than accumulating orphans.
	if file, hdr, ferr := gc.Request.FormFile("image"); ferr == nil {
		defer func() { _ = file.Close() }()
		if p.files == nil {
			return p.redirect(gc,
				"this host has no file store wired (achievements.files), so badge images cannot be uploaded")
		}
		if hdr.Size > 2<<20 {
			return p.redirect(gc, "badge image over 2 MiB")
		}
		data, rerr := io.ReadAll(io.LimitReader(file, 2<<20+1))
		if rerr != nil {
			return p.redirect(gc, "could not read the image")
		}
		ext, ok := blob.ImageExts[http.DetectContentType(data)]
		if !ok {
			return p.redirect(gc, "badge image must be JPEG, PNG, GIF or WebP")
		}
		url, serr := p.files.Save(gc.Request.Context(), "achievement-badges/"+a.Slug+ext, data)
		if serr != nil {
			return p.redirect(gc, fmt.Sprintf("saving image: %v", serr))
		}
		a.ImagePath = url
	}
	if _, err := p.admin.CreateAchievement(gc.Request.Context(), a); err != nil {
		return p.redirect(gc, err.Error())
	}
	// Created DISABLED, and the message says so: enabling backfills a metric
	// achievement to everyone already past the threshold on the next tick,
	// which is a separate and deliberate act.
	if a.Trigger != "" {
		return p.redirectOK(gc, "Created "+a.Slug+" — DISABLED. It completes when "+a.Trigger+" fires, once enabled.")
	}
	return p.redirectOK(gc,
		"Created "+a.Slug+" — DISABLED. Enabling it awards it to everyone already past "+
			strconv.FormatInt(a.Threshold, 10)+", silently, on the next job tick.")
}

func (p *Plugin) actionToggleAchievement(gc *gin.Context) (template.HTML, error) {
	if p.admin == nil {
		return p.redirect(gc, "no admin store configured")
	}
	id, err := strconv.ParseInt(gc.PostForm("id"), 10, 64)
	if err != nil || id <= 0 {
		return p.redirect(gc, "bad achievement id")
	}
	on := gc.PostForm("on") == "1"
	if err := p.admin.SetAchievementEnabled(gc.Request.Context(), id, on); err != nil {
		return p.redirect(gc, err.Error())
	}
	if on {
		return p.redirectOK(gc,
			"Enabled. The next job tick backfills it to everyone already qualified, silently.")
	}
	return p.redirectOK(gc, "Disabled. Progress is kept; nothing new completes.")
}

// adminPage is the one absolute destination every action returns to.
const adminPage = "/admin/p/achievements"

// redirect sends the operator back to the page with an error; redirectOK with
// a success message. Actions return no HTML of their own — the host
// re-renders.
func (p *Plugin) redirect(gc *gin.Context, errMsg string) (template.HTML, error) {
	q := url.Values{}
	q.Set("err", errMsg)
	gc.Redirect(http.StatusSeeOther, adminPage+"?"+q.Encode())
	return "", nil
}

func (p *Plugin) redirectOK(gc *gin.Context, msg string) (template.HTML, error) {
	q := url.Values{}
	q.Set("msg", msg)
	gc.Redirect(http.StatusSeeOther, adminPage+"?"+q.Encode())
	return "", nil
}
