package events

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

//go:embed templates/*.html
var viewFS embed.FS

// The admin page. Ported from the rewards plugin, whose own comment explained
// why it should never have lived there:
//
//	"Events are not reward-specific — the table is deliberately named `events`,
//	because a season or an outage window is a site fact other systems can
//	reference — so burying them inside a Rewards page would misrepresent what
//	they are. It also keeps each page to one job: Events is WHEN, Rewards is
//	WHAT."
//
// This page is the WHEN, now living where the WHEN is owned.

type adminVM struct {
	Now     time.Time
	Msg     string
	Err     string
	Events  []eventVM
	Picked  string
	Windows []pluginapi.EventWindow

	// Findings is the window-health report. It sits at the TOP of the page
	// because it is the only part that says something is wrong -- an operator
	// reading a healthy-looking table of events has no way to know the generator
	// stalled a month ago and everything gated on them quietly stopped.
	Findings []Finding
}

// eventVM is one row of the definitions table, with the state an operator
// actually wants: is it open right now, and when does it next open.
//
// OpenNow and NextOpen are computed rather than stored, because a stored "is
// open" is a cache that goes stale between ticks and an operator reading a
// stale flag concludes the generator is broken.
type eventVM struct {
	pluginapi.ScheduledEvent
	OpenNow  bool
	NextOpen time.Time
	Windows  int
	LastEnds time.Time
}

// Shape reads the definition back as a sentence, because cron plus a nullable
// duration is not something an operator should have to re-derive. The three
// cases are the whole rule.
func (e eventVM) Shape() string {
	switch {
	case e.OneOff() && e.Duration == 0:
		return "one-off, never closes"
	case e.OneOff():
		return "one-off, open for " + e.Duration.String()
	case e.Duration == 0:
		return "recurring, contiguous (each window runs to the next)"
	default:
		return "recurring, open for " + e.Duration.String()
	}
}

func (p *Plugin) registerViews(c *core.Core) error {
	if err := p.parseTemplates(); err != nil {
		return err
	}
	return c.RegisterView(core.View{
		Slug: "events", Title: "Scheduled events", Slot: core.SlotAdminPage,
		Description: "Named time windows other systems gate on: seasons, resets, launch weeks.",
		Nav:         core.NavHint{Group: "Operations"},
		Render: func(gc *gin.Context) (template.HTML, error) {
			// The slug reaches SQL as a bound parameter; an unknown one simply
			// lists no windows.
			return p.renderPage(gc.Request.Context(), gc.Query("event"), gc.Query("msg"), gc.Query("err"))
		},
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"event-save":   p.actionSaveEvent,
			"event-toggle": p.actionToggleEvent,
			"event-delete": p.actionDeleteEvent,
		},
	})
}

// parseTemplates is split out so a test can render the page without a Core.
func (p *Plugin) parseTemplates() error {
	t, err := template.New("").Funcs(template.FuncMap{
		"ts": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			// A perpetual end is a sentinel, not a date. Printing
			// "9999-12-31" invites an operator to wonder what happened in the
			// year 9999.
			if pluginapi.IsPerpetual(t) {
				return "never"
			}
			return t.UTC().Format("2006-01-02 15:04")
		},
		"cron": func(s string) string {
			if s == "" {
				return "one-off"
			}
			return s
		},
	}).ParseFS(viewFS, "templates/*.html")
	if err != nil {
		return err
	}
	p.tmpl = t
	return nil
}

func (p *Plugin) renderPage(ctx context.Context, picked, msg, errMsg string) (template.HTML, error) {
	now := time.Now()
	vm := adminVM{Now: now, Msg: msg, Err: errMsg, Picked: picked}

	evs, err := p.store.ListEvents(ctx)
	if err != nil {
		return "", err
	}
	open, err := p.store.AllOpen(ctx, now)
	if err != nil {
		return "", err
	}
	for _, ev := range evs {
		row := eventVM{ScheduledEvent: ev, OpenNow: open[ev.Slug]}
		// Errors here are per-row and non-fatal: a malformed cron should cost
		// that row its "next opens" cell, not the page. The same rule the
		// generator follows for the same reason.
		if next, err := NextStart(ev, now); err == nil {
			row.NextOpen = next
		}
		if last, err := p.store.LastWindowEnd(ctx, ev.Slug); err == nil {
			row.LastEnds = last
		}
		if ws, err := p.store.ListWindows(ctx, ev.Slug, 500); err == nil {
			row.Windows = len(ws)
		}
		vm.Events = append(vm.Events, row)
	}

	if picked != "" {
		if vm.Windows, err = p.store.ListWindows(ctx, picked, 50); err != nil {
			return "", err
		}
	}

	// Non-fatal, like the per-row lookups above: a page that cannot run the
	// validator is still worth rendering, and losing the banner is a visible
	// symptom where a 500 would hide every event on the site.
	if fs, err := p.Validate(ctx); err == nil {
		vm.Findings = fs
	}

	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "events_admin.html", vm); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

// actionSaveEvent creates or updates a definition.
//
// One action for both, because the form is the same and an upsert by slug is
// what the store offers. Editing a slug is therefore creating a new event, which
// is the honest behaviour: consumers reference the slug, so changing it detaches
// them and pretending otherwise would silently orphan a news post or a reward.
func (p *Plugin) actionSaveEvent(gc *gin.Context) (template.HTML, error) {
	ctx := gc.Request.Context()
	slug := strings.TrimSpace(gc.PostForm("slug"))
	if slug == "" {
		return p.renderPage(ctx, "", "", "A slug is required — it is how other systems reference this event.")
	}

	ev := pluginapi.ScheduledEvent{
		Slug:        slug,
		Name:        strings.TrimSpace(gc.PostForm("name")),
		Description: strings.TrimSpace(gc.PostForm("description")),
		Cron:        strings.TrimSpace(gc.PostForm("cron")),
		Timezone:    strings.TrimSpace(gc.PostForm("timezone")),
		Enabled:     gc.PostForm("enabled") == "1",
	}
	if ev.Timezone == "" {
		ev.Timezone = "UTC"
	}

	// Duration in minutes, because "7 days" typed as a Go duration string is a
	// thing operators get wrong and an interval column will happily store.
	if mins := strings.TrimSpace(gc.PostForm("duration_minutes")); mins != "" {
		n, err := strconv.Atoi(mins)
		if err != nil || n < 0 {
			return p.renderPage(ctx, "", "", "Duration must be a whole number of minutes, or blank.")
		}
		ev.Duration = time.Duration(n) * time.Minute
	}
	if s := strings.TrimSpace(gc.PostForm("starts_at")); s != "" {
		// datetime-local, which has no zone. Read in the event's own timezone:
		// an operator typing 09:00 for a Tokyo event means 09:00 in Tokyo, and
		// reading it as UTC would silently shift the event nine hours.
		loc, err := time.LoadLocation(ev.Timezone)
		if err != nil {
			return p.renderPage(ctx, "", "", "Unknown timezone "+ev.Timezone)
		}
		t, err := time.ParseInLocation("2006-01-02T15:04", s, loc)
		if err != nil {
			return p.renderPage(ctx, "", "", "Start must look like 2026-09-01T00:00.")
		}
		ev.StartsAt = &t
	}

	// The schema's own CHECK, restated here so the operator gets a sentence
	// instead of a constraint-violation error page. Both exist on purpose: this
	// one is the message, the constraint is the guarantee.
	if ev.Cron == "" && ev.StartsAt == nil {
		return p.renderPage(ctx, "", "",
			"An event needs a cron expression or a start date — otherwise it can never open.")
	}
	// Validate the cron HERE rather than discovering it in the generator's log,
	// where a typo becomes "my event has no windows" hours later.
	if ev.Cron != "" {
		if _, err := cronParser.Parse(ev.Cron); err != nil {
			return p.renderPage(ctx, "", "", fmt.Sprintf("Cron %q is not valid: %v", ev.Cron, err))
		}
	}
	if _, err := time.LoadLocation(ev.Timezone); err != nil {
		return p.renderPage(ctx, "", "", "Unknown timezone "+ev.Timezone)
	}

	if err := p.store.UpsertEvent(ctx, ev); err != nil {
		return "", err
	}
	return p.renderPage(ctx, ev.Slug,
		"Saved "+ev.Slug+". Windows appear on the next Event Windows run — trigger it from /admin/jobs to see them now.", "")
}

func (p *Plugin) actionToggleEvent(gc *gin.Context) (template.HTML, error) {
	ctx := gc.Request.Context()
	slug := gc.PostForm("slug")
	ev, ok, err := p.store.GetEvent(ctx, slug)
	if err != nil {
		return "", err
	}
	if !ok {
		return p.renderPage(ctx, "", "", "No such event: "+slug)
	}
	ev.Enabled = gc.PostForm("on") == "1"
	if err := p.store.UpsertEvent(ctx, ev); err != nil {
		return "", err
	}
	// Existing windows are deliberately left alone. Disabling stops an event
	// being reported open (every query joins on enabled) and stops generation,
	// but deleting history would destroy the record of occurrences consumers may
	// already have acted on.
	state := "disabled"
	if ev.Enabled {
		state = "enabled"
	}
	return p.renderPage(ctx, slug, slug+" "+state+".", "")
}

func (p *Plugin) actionDeleteEvent(gc *gin.Context) (template.HTML, error) {
	ctx := gc.Request.Context()
	slug := gc.PostForm("slug")
	if err := p.store.DeleteEvent(ctx, slug); err != nil {
		return "", err
	}
	return p.renderPage(ctx, "", "Deleted "+slug+" and its windows. Anything referencing that slug now sees it as absent.", "")
}

// renderCtx is a context-only render for tests.
func (p *Plugin) renderCtx(ctx context.Context) (template.HTML, error) {
	return p.renderPage(ctx, "", "", "")
}
