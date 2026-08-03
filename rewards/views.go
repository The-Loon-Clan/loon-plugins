package rewards

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

//go:embed templates/*.html
var viewFS embed.FS

// The admin page answers the three questions an operator has about a rewards
// system they cannot see: what is configured, is the machinery running (are
// windows being materialised?), and is anything actually being granted.

type adminVM struct {
	Events  []EventStats
	Rewards []Reward
	Grants  []GrantRow
	Now     time.Time
	Msg     string
	Err     string

	// The event whose windows are being authored, when one is selected.
	// Zero means the windows panel is collapsed.
	PickedEvent int64
	PickedSlug  string
	Windows     []Window
	// Pre-formatted for <input type="datetime-local">, which will not accept
	// an RFC3339 string with a zone.
	DefaultStart string
	DefaultEnd   string

	// Cross-table findings, shown on BOTH pages: the configuration that breaks
	// a reward usually lives on the other page from the one being looked at.
	Findings []Finding
}

func (p *Plugin) registerViews(c *core.Core) error {
	if err := p.parseTemplates(); err != nil {
		return err
	}
	// Two pages, not one with three cards. Events are not reward-specific —
	// the table is deliberately named `events`, because a season or an outage
	// window is a site fact other systems can reference — so burying them
	// inside a Rewards page would misrepresent what they are. It also keeps
	// each page to one job: Events is WHEN, Rewards is WHAT.
	if err := c.RegisterView(core.View{
		Slug: "rewards-events", Title: "Events", Slot: core.SlotAdminPage,
		Description: "When rewards are earnable: campaign windows and reset periods.",
		Nav:         core.NavHint{Group: "Operations"},
		Render: func(gc *gin.Context) (template.HTML, error) {
			// event id reaches SQL as a bound parameter; an unknown value
			// simply lists no windows.
			ev, _ := strconv.ParseInt(gc.Query("event"), 10, 64)
			return p.renderPage(gc.Request.Context(), "rewards_events.html", ev, gc.Query("msg"), gc.Query("err"))
		},
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"event-create": p.actionCreateEvent,
			"event-toggle": p.actionToggleEvent,
			"window-add":   p.actionAddWindow,
			"window-del":   p.actionDeleteWindow,
		},
	}); err != nil {
		return err
	}
	return c.RegisterView(core.View{
		Slug: "rewards", Title: "Rewards", Slot: core.SlotAdminPage,
		Description: "What is earnable, what it pays, and what has actually been granted.",
		Nav:         core.NavHint{Group: "Operations"},
		Render: func(gc *gin.Context) (template.HTML, error) {
			return p.renderPage(gc.Request.Context(), "rewards_admin.html", 0, gc.Query("msg"), gc.Query("err"))
		},
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"reward-create": p.actionCreateReward,
			"reward-toggle": p.actionToggleReward,
			"test-grant":    p.actionTestGrant,
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
			return t.UTC().Format("2006-01-02 15:04")
		},
		"dur": func(d *time.Duration) string {
			if d == nil {
				return "—"
			}
			return d.String()
		},
		"str": derefStr,
		"payouts": func(ps []Payout) string {
			if len(ps) == 0 {
				// Worth shouting about: this reward will be refused at grant
				// time, and the admin table is where that becomes visible.
				return "NONE — will not grant"
			}
			parts := make([]string, 0, len(ps))
			for _, p := range ps {
				if p.Kind == PayoutPoints {
					parts = append(parts, fmt.Sprintf("%d points", p.Amount))
					continue
				}
				parts = append(parts, string(p.Kind)+" "+p.Target)
			}
			return strings.Join(parts, " + ")
		},
		"window": func(w *Window) string {
			if w == nil {
				return "—"
			}
			return fmt.Sprintf("#%d %s → %s", w.ID,
				w.StartsAt.UTC().Format("01-02 15:04"), w.EndsAt.UTC().Format("01-02 15:04"))
		},
	}).ParseFS(viewFS, "templates/*.html")
	if err != nil {
		return err
	}
	p.tmpl = t
	return nil
}

func (p *Plugin) renderPage(ctx context.Context, tmpl string, pickedEvent int64, msg, errMsg string) (template.HTML, error) {
	now := time.Now()
	vm := adminVM{Now: now, Msg: msg, Err: errMsg, PickedEvent: pickedEvent}
	var err error
	if vm.Events, err = p.admin.ListEventStats(ctx, now); err != nil {
		return "", err
	}
	if vm.Rewards, err = p.admin.ListRewards(ctx); err != nil {
		return "", err
	}
	if vm.Grants, err = p.admin.RecentGrants(ctx, 50); err != nil {
		return "", err
	}
	if pickedEvent != 0 {
		for _, e := range vm.Events {
			if e.ID == pickedEvent {
				vm.PickedSlug = e.Slug
			}
		}
		if vm.Windows, err = p.admin.ListWindows(ctx, pickedEvent, 50); err != nil {
			return "", err
		}
		// Sensible defaults so the picker opens on today rather than 1970.
		start := now.UTC().Truncate(24 * time.Hour)
		vm.DefaultStart = start.Format(localTimeLayout)
		vm.DefaultEnd = start.Add(24 * time.Hour).Format(localTimeLayout)
	}
	// Deliberately not fatal: a validator that can take the page down with it
	// is worse than no validator, and the page's first job is to show state.
	if vm.Findings, err = p.Validate(ctx); err != nil {
		vm.Findings = []Finding{{SeverityWarn, "validator", "could not run: " + err.Error(), ""}}
	}
	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, tmpl, vm); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

// redirect sends the operator back to the page with a message. Actions return
// no HTML of their own — the host re-renders.
func (p *Plugin) redirect(gc *gin.Context, page, msg, errMsg string) (template.HTML, error) {
	q := url.Values{}
	if errMsg != "" {
		q.Set("err", errMsg)
	} else if msg != "" {
		q.Set("msg", msg)
	}
	// Keep the windows panel open across an add/delete, so authoring a run of
	// them is not twelve round trips through a collapsed page.
	if ev := gc.PostForm("event_id"); ev != "" {
		q.Set("event", ev)
	}
	gc.Redirect(http.StatusSeeOther, page+"?"+q.Encode())
	return "", nil
}

// localTimeLayout is what <input type="datetime-local"> emits and accepts. It
// carries no zone, so both directions are interpreted as UTC explicitly rather
// than as whatever the server happens to be set to.
const localTimeLayout = "2006-01-02T15:04"

// The two page paths, so an action returns to the page it was pressed on.
const (
	eventsPage  = "/admin/p/rewards-events"
	rewardsPage = "/admin/p/rewards"
)

func (p *Plugin) actionAddWindow(gc *gin.Context) (template.HTML, error) {
	eventID, err := strconv.ParseInt(gc.PostForm("event_id"), 10, 64)
	if err != nil {
		return p.redirect(gc, eventsPage, "", "bad event id")
	}
	start, err := time.ParseInLocation(localTimeLayout, gc.PostForm("starts_at"), time.UTC)
	if err != nil {
		return p.redirect(gc, eventsPage, "", "bad start time")
	}
	end, err := time.ParseInLocation(localTimeLayout, gc.PostForm("ends_at"), time.UTC)
	if err != nil {
		return p.redirect(gc, eventsPage, "", "bad end time")
	}
	if !end.After(start) {
		return p.redirect(gc, eventsPage, "", "the window must end after it starts")
	}
	if err := p.admin.AddWindow(gc.Request.Context(), Window{EventID: eventID, StartsAt: start, EndsAt: end}); err != nil {
		return p.redirect(gc, eventsPage, "", err.Error())
	}
	return p.redirect(gc, eventsPage, "window added", "")
}

func (p *Plugin) actionDeleteWindow(gc *gin.Context) (template.HTML, error) {
	id, err := strconv.ParseInt(gc.PostForm("id"), 10, 64)
	if err != nil {
		return p.redirect(gc, eventsPage, "", "bad window id")
	}
	if err := p.admin.DeleteWindow(gc.Request.Context(), id); err != nil {
		return p.redirect(gc, eventsPage, "", err.Error())
	}
	return p.redirect(gc, eventsPage, "window deleted", "")
}

func (p *Plugin) actionCreateEvent(gc *gin.Context) (template.HTML, error) {
	slug := strings.TrimSpace(gc.PostForm("slug"))
	if slug == "" {
		return p.redirect(gc, eventsPage, "", "slug is required")
	}
	cron := strings.TrimSpace(gc.PostForm("cron"))
	// Validate here rather than at generation time: a bad expression caught on
	// the form is a message someone reads, and one caught at 3am inside the
	// generator is an event that silently never produces windows.
	if cron != "" {
		if err := ValidateCron(cron); err != nil {
			return p.redirect(gc, eventsPage, "", "bad cron: "+err.Error())
		}
	}
	tz := strings.TrimSpace(gc.PostForm("timezone"))
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return p.redirect(gc, eventsPage, "", "unknown timezone "+tz)
	}

	ev := Event{Slug: slug, Name: strings.TrimSpace(gc.PostForm("name")), Timezone: tz, Enabled: true}
	if cron != "" {
		ev.Cron = &cron
	}
	// Blank duration is the WHOLE point of a reset: the window runs until the
	// next firing rather than closing.
	if raw := strings.TrimSpace(gc.PostForm("duration")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return p.redirect(gc, eventsPage, "", "duration must be a positive Go duration like 1536h")
		}
		if cron == "" {
			return p.redirect(gc, eventsPage, "", "a duration needs a cron — 'closes after' has no 'starting when'")
		}
		ev.Duration = &d
	}
	if _, err := p.admin.CreateEvent(gc.Request.Context(), ev); err != nil {
		return p.redirect(gc, eventsPage, "", err.Error())
	}
	return p.redirect(gc, eventsPage, "event+"+slug+"+created", "")
}

func (p *Plugin) actionToggleEvent(gc *gin.Context) (template.HTML, error) {
	id, _ := strconv.ParseInt(gc.PostForm("id"), 10, 64)
	on := gc.PostForm("enabled") == "1"
	if err := p.admin.SetEventEnabled(gc.Request.Context(), id, on); err != nil {
		return p.redirect(gc, eventsPage, "", err.Error())
	}
	return p.redirect(gc, eventsPage, "event+updated", "")
}

func (p *Plugin) actionCreateReward(gc *gin.Context) (template.HTML, error) {
	ctx := gc.Request.Context()
	slug := strings.TrimSpace(gc.PostForm("slug"))
	if slug == "" {
		return p.redirect(gc, rewardsPage, "", "slug is required")
	}
	kind := Kind(gc.PostForm("kind"))
	switch kind {
	case KindOneOff, KindRecurring, KindPerUnit:
	default:
		return p.redirect(gc, rewardsPage, "", "unknown kind")
	}
	delivery := Delivery(gc.PostForm("delivery"))
	if delivery != DeliveryAuto && delivery != DeliveryClaim {
		return p.redirect(gc, rewardsPage, "", "unknown delivery")
	}

	r := Reward{
		Slug: slug, Name: strings.TrimSpace(gc.PostForm("name")), Kind: kind,
		Trigger: strings.TrimSpace(gc.PostForm("trigger")), Delivery: delivery,
		// Deliberately created DISABLED. A reward that starts paying the
		// moment it is typed leaves no chance to check the payout line first,
		// and un-paying is not a thing.
		Enabled: false,
	}
	if raw := strings.TrimSpace(gc.PostForm("event_id")); raw != "" && raw != "0" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return p.redirect(gc, rewardsPage, "", "bad event id")
		}
		r.EventID = &id
	}
	if kind == KindRecurring && r.EventID == nil {
		return p.redirect(gc, rewardsPage, "", "a recurring reward needs an event — that is its reset")
	}
	if raw := strings.TrimSpace(gc.PostForm("expires_after")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return p.redirect(gc, rewardsPage, "", "expiry must be a positive Go duration like 720h")
		}
		r.ExpiresAfter = &d
	}

	points, _ := strconv.Atoi(strings.TrimSpace(gc.PostForm("points")))
	if points > 0 {
		r.Payouts = append(r.Payouts, Payout{Kind: PayoutPoints, Amount: points})
	}
	if target := strings.TrimSpace(gc.PostForm("medal")); target != "" {
		r.Payouts = append(r.Payouts, Payout{Kind: PayoutMedal, Target: target})
	}
	if len(r.Payouts) == 0 {
		return p.redirect(gc, rewardsPage, "", "a reward with no payout lines would grant nothing")
	}

	if _, err := p.admin.CreateReward(ctx, r); err != nil {
		return p.redirect(gc, rewardsPage, "", err.Error())
	}
	return p.redirect(gc, rewardsPage, "reward+"+slug+"+created+(disabled)", "")
}

func (p *Plugin) actionToggleReward(gc *gin.Context) (template.HTML, error) {
	id, _ := strconv.ParseInt(gc.PostForm("id"), 10, 64)
	on := gc.PostForm("enabled") == "1"
	if err := p.admin.SetRewardEnabled(gc.Request.Context(), id, on); err != nil {
		return p.redirect(gc, rewardsPage, "", err.Error())
	}
	return p.redirect(gc, rewardsPage, "reward+updated", "")
}

// actionTestGrant runs one reward against the signed-in admin, end to end.
//
// Against THEMSELVES and nobody else: there is deliberately no "grant to user
// N" box. Rewards are rules, and a per-member grant typed into a form is the
// one thing this whole model exists to stop being ad hoc. Testing on your own
// balance costs the operator their own points and nobody else's.
func (p *Plugin) actionTestGrant(gc *gin.Context) (template.HTML, error) {
	rewardID, err := strconv.ParseInt(gc.PostForm("id"), 10, 64)
	if err != nil {
		return p.redirect(gc, rewardsPage, "", "bad reward id")
	}
	u, ok := p.core.Auth.CurrentUser(gc)
	if !ok || u == nil {
		return p.redirect(gc, rewardsPage, "", "not signed in")
	}
	g, err := p.engine.Claim(gc.Request.Context(), int64(u.ID), rewardID)
	if err != nil {
		// ErrAlreadyGranted is the expected answer on a second press and is
		// exactly what a test wants to see — report it plainly rather than as
		// a failure.
		return p.redirect(gc, rewardsPage, "", err.Error())
	}
	return p.redirect(gc, rewardsPage, fmt.Sprintf("granted+%d+(ref+%d,+%s)", g.ID, g.Reference, g.State), "")
}
