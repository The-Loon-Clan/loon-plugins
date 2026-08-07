package rewards

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

//go:embed templates/*.html
var viewFS embed.FS

// The admin page answers the three questions an operator has about a rewards
// system they cannot see: what is configured, is the machinery running (are
// windows being materialised?), and is anything actually being granted.

type adminVM struct {
	// ScheduledEvents feeds the reward form's event picker, read from the events
	// plugin. Empty on a host with no events plugin, which renders as a select
	// with only "none" — honest, since nothing there could be gated anyway.
	ScheduledEvents []pluginapi.ScheduledEvent

	Rewards []Reward
	Grants  []GrantRow
	Now     time.Time
	Msg     string
	Err     string

	// The event whose windows are being authored, when one is selected.
	// Zero means the windows panel is collapsed.
	PickedSlug string
	// Pre-formatted for <input type="datetime-local">, which will not accept
	// an RFC3339 string with a zone.
	DefaultStart string
	DefaultEnd   string

	// Cross-table findings, shown on BOTH pages: the configuration that breaks
	// a reward usually lives on the other page from the one being looked at.
	Findings []Finding

	// Triggers offers the surface names already in use, so the form is a
	// picker rather than a spelling test. A typo here is silent: the reward
	// saves fine, no surface ever asks for that string, and nobody is offered
	// the reward — which looks exactly like a reward that is merely unpopular.
	//
	// Advisory, not a whitelist. Fire() takes any string and the HOST decides
	// what it fires, so the field stays free-text via a datalist; the plugin
	// cannot know a surface it was never told about.
	Triggers []string
}

func (p *Plugin) registerViews(c *core.Core) error {
	if err := p.parseTemplates(); err != nil {
		return err
	}
	// One page now. The Events page moved to the events plugin, which owns the
	// tables -- the comment that used to sit here explained why it should:
	// "Events are not reward-specific ... it also keeps each page to one job:
	// Events is WHEN, Rewards is WHAT." This page is the WHAT.
	return c.RegisterView(core.View{
		Slug: "rewards", Title: "Rewards", Slot: core.SlotAdminPage,
		Description: "What is earnable, what it pays, and what has actually been granted.",
		Nav:         core.NavHint{Group: "Operations"},
		Render: func(gc *gin.Context) (template.HTML, error) {
			return p.renderPage(gc.Request.Context(), "rewards_admin.html", gc.Query("msg"), gc.Query("err"))
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
	}).ParseFS(viewFS, "templates/*.html")
	if err != nil {
		return err
	}
	p.tmpl = t
	return nil
}

func (p *Plugin) renderPage(ctx context.Context, tmpl string, msg, errMsg string) (template.HTML, error) {
	now := time.Now()
	vm := adminVM{Now: now, Msg: msg, Err: errMsg}
	if p.events != nil {
		// Non-fatal: a page that cannot list events is still worth rendering,
		// and the picker degrading to "none" is a visible symptom where a 500
		// would hide every other thing on the page.
		if evs, err := p.events.Events(ctx); err == nil {
			vm.ScheduledEvents = evs
		}
	}
	var err error
	if vm.Rewards, err = p.admin.ListRewards(ctx); err != nil {
		return "", err
	}
	if vm.Grants, err = p.admin.RecentGrants(ctx, 50); err != nil {
		return "", err
	}
	vm.Triggers = p.triggerOptions(ctx, vm.Rewards)
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
	// A slug from the events plugin's dropdown, not an id. Existence is checked
	// against the capability rather than assumed: a typo'd slug would otherwise
	// create a reward that looks configured and can never be earned, and the
	// validator would only surface it later.
	r.EventSlug = strings.TrimSpace(gc.PostForm("scheduled_event_slug"))
	if r.EventSlug != "" && p.events != nil {
		_, known, err := p.events.Event(gc.Request.Context(), r.EventSlug)
		if err != nil {
			return p.redirect(gc, rewardsPage, "", "could not check the scheduled event: "+err.Error())
		}
		if !known {
			return p.redirect(gc, rewardsPage, "", "no scheduled event called "+r.EventSlug)
		}
	}
	if kind == KindRecurring && r.EventSlug == "" {
		return p.redirect(gc, rewardsPage, "", "a recurring reward needs a scheduled event — that is its reset")
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
	// The reference is a name now, and an empty one (a one_off) reads better as
	// "-" than as a gap in the sentence.
	ref := g.Reference
	if ref == "" {
		ref = "-"
	}
	return p.redirect(gc, rewardsPage, fmt.Sprintf("granted+%d+(ref+%s,+%s)", g.ID, ref, g.State), "")
}

// triggerOptions is what the trigger picker offers.
//
// The host's DECLARED catalogue when there is one — that is the whole point of
// declaring it, and it is the only list that can contain a trigger nobody has
// configured yet. Falling back to the derived list otherwise, because an
// install that predates the catalogue must keep working; keys already in use
// are merged in either way, so a rename in the catalogue does not make an
// existing reward's trigger vanish from the dropdown that edits it.
func (p *Plugin) triggerOptions(ctx context.Context, rewards []Reward) []string {
	cat, err := p.Catalogue(ctx)
	if err != nil {
		// A picker is not worth failing a page render for; the derived list
		// still edits what exists.
		return knownTriggers(rewards)
	}
	declared := cat.Triggers().Keys()
	if len(declared) == 0 {
		return knownTriggers(rewards)
	}
	seen := map[string]bool{}
	for _, k := range declared {
		seen[k] = true
	}
	for _, r := range rewards {
		if r.Trigger != "" {
			seen[r.Trigger] = true
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// knownTriggers is the pre-catalogue fallback: every surface name already
// configured, plus the ones a stock host fires.
//
// Seeded rather than purely derived because the list is empty exactly when it
// is most needed — configuring the FIRST trigger-driven reward, when there is
// nothing to derive from. Superseded by the catalogue, kept for hosts that
// have not declared one.
func knownTriggers(rewards []Reward) []string {
	seen := map[string]bool{"login": true}
	for _, r := range rewards {
		if r.Trigger != "" {
			seen[r.Trigger] = true
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
