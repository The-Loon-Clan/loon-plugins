package rewards

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
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

	// Lootboxes: the slugs that exist, the box being edited, and its lines.
	// A box has no row of its own — it IS its slug — so "which box" is a
	// query parameter rather than a selected id, and the editor is open on
	// whichever one ?box= names.
	// CSRFToken for every form on this page. It was ABSENT, and so were the
	// tokens: five POST forms carrying none, against a host whose CSRF
	// middleware gates every POST globally — so toggling a reward, creating
	// one and test-granting all answered 403 for every operator who tried.
	// The access audit cannot see this class (it probes WITH a valid token by
	// design), which is why the template test below counts tokens per form.
	CSRFToken string

	Boxes      []string
	PickedBox  string
	BoxEntries []LootboxEntry
	BoxTotal   int

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
	// Events is WHEN, Rewards is WHAT." This page is the WHAT. The
	// Achievements page moved out the same way, with the achievements plugin
	// that owns those tables; its slug and URL survived the move.
	return c.RegisterView(core.View{
		Slug: "rewards", Title: "Rewards", Slot: core.SlotAdminPage,
		Description: "What is earnable, what it pays, and what has actually been granted.",
		Nav:         core.NavHint{Group: "Operations"},
		Render: func(gc *gin.Context) (template.HTML, error) {
			return p.renderPageBox(gc.Request.Context(), "rewards_admin.html",
				gc.Query("msg"), gc.Query("err"), gc.Query("box"), p.csrfToken(gc))
		},
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"reward-create":  p.actionCreateReward,
			"reward-toggle":  p.actionToggleReward,
			"test-grant":     p.actionTestGrant,
			"lootbox-add":    p.actionLootboxAdd,
			"lootbox-remove": p.actionLootboxRemove,
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
		// The three the EDIT form needs: the same values the row displays, in
		// the shape the inputs take. Without them an edit would render blank
		// fields over real data, and saving would replace a medal payout with
		// nothing because the box looked empty.
		"durform": func(d *time.Duration) string {
			if d == nil {
				return ""
			}
			return d.String()
		},
		"payoutamount": func(ps []Payout, kind string) string {
			for _, p := range ps {
				if string(p.Kind) == kind && p.Amount > 0 {
					return strconv.Itoa(p.Amount)
				}
			}
			return ""
		},
		"payouttarget": func(ps []Payout, kind string) string {
			for _, p := range ps {
				if string(p.Kind) == kind {
					return p.Target
				}
			}
			return ""
		},
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
	return p.renderPageBox(ctx, tmpl, msg, errMsg, "", "")
}

// renderPageBox is renderPage with a lootbox open in the editor. Separate
// entry point rather than a parameter on every caller: three of the four
// callers have no box to open, and threading an empty string through them
// would say nothing.
func (p *Plugin) renderPageBox(ctx context.Context, tmpl, msg, errMsg, box, csrf string) (template.HTML, error) {
	now := time.Now()
	vm := adminVM{Now: now, Msg: msg, Err: errMsg, CSRFToken: csrf}
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
	// Lootboxes. Non-fatal like the pickers above: a box list that cannot be
	// read should cost its own panel, not the page.
	if boxes, berr := p.admin.LootboxSlugs(ctx); berr == nil {
		vm.Boxes = boxes
	}
	// Deliberately not fatal: a validator that can take the page down with it
	// is worse than no validator, and the page's first job is to show state.
	if vm.Findings, err = p.Validate(ctx); err != nil {
		vm.Findings = []Finding{{SeverityWarn, "validator", "could not run: " + err.Error(), ""}}
	}
	// The box under edit, when one was named. Its entries carry the reward
	// names, so the table reads as prizes rather than as ids.
	if box != "" {
		vm.PickedBox = box
		if entries, eerr := p.admin.LootboxEntries(ctx, box); eerr == nil {
			vm.BoxEntries = entries
			vm.BoxTotal = LootboxTotalWeight(entries)
		}
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

// rewardFromForm reads a reward definition off the admin form.
//
// Shared by create and update, because the validation is the definition: an
// edit can break a reward exactly as thoroughly as a create can — a recurring
// one that loses its scheduled event, a payout list emptied to nothing — and a
// form that only checks on the way in is one an edit walks straight past.
//
// Returns a member-facing refusal rather than redirecting, so the two callers
// keep their own success messages.
func (p *Plugin) rewardFromForm(gc *gin.Context, slug string, enabled bool) (r Reward, refusal string) {
	kind := Kind(gc.PostForm("kind"))
	switch kind {
	case KindOneOff, KindRecurring, KindPerUnit:
	default:
		return r, "unknown kind"
	}
	delivery := Delivery(gc.PostForm("delivery"))
	if delivery != DeliveryAuto && delivery != DeliveryClaim {
		return r, "unknown delivery"
	}

	r = Reward{
		Slug: slug, Name: strings.TrimSpace(gc.PostForm("name")), Kind: kind,
		Trigger: strings.TrimSpace(gc.PostForm("trigger")), Delivery: delivery,
		Enabled: enabled,
	}
	// A slug from the events plugin's dropdown, not an id. Existence is checked
	// against the capability rather than assumed: a typo'd slug would otherwise
	// create a reward that looks configured and can never be earned, and the
	// validator would only surface it later.
	r.EventSlug = strings.TrimSpace(gc.PostForm("scheduled_event_slug"))
	if r.EventSlug != "" && p.events != nil {
		_, known, err := p.events.Event(gc.Request.Context(), r.EventSlug)
		if err != nil {
			return r, "could not check the scheduled event: " + err.Error()
		}
		if !known {
			return r, "no scheduled event called " + r.EventSlug
		}
	}
	if kind == KindRecurring && r.EventSlug == "" {
		return r, "a recurring reward needs a scheduled event — that is its reset"
	}
	if raw := strings.TrimSpace(gc.PostForm("expires_after")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return r, "expiry must be a positive Go duration like 720h"
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
	// A lootbox line: the reward hands over a box, and opening it draws one of
	// the box's prizes. Offered on the form rather than reachable only by a
	// hand-crafted POST, which is the state the medal line was found in.
	if target := strings.TrimSpace(gc.PostForm("lootbox")); target != "" {
		r.Payouts = append(r.Payouts, Payout{Kind: PayoutLootbox, Target: target})
	}
	if len(r.Payouts) == 0 {
		return r, "a reward with no payout lines would grant nothing"
	}

	return r, ""
}

func (p *Plugin) actionCreateReward(gc *gin.Context) (template.HTML, error) {
	ctx := gc.Request.Context()
	slug := strings.TrimSpace(gc.PostForm("slug"))
	if slug == "" {
		return p.redirect(gc, rewardsPage, "", "slug is required")
	}
	// Deliberately created DISABLED. A reward that starts paying the moment it
	// is typed leaves no chance to check the payout line first, and un-paying
	// is not a thing.
	r, refusal := p.rewardFromForm(gc, slug, false)
	if refusal != "" {
		return p.redirect(gc, rewardsPage, "", refusal)
	}
	if _, err := p.admin.CreateReward(ctx, r); err != nil {
		return p.redirect(gc, rewardsPage, "", err.Error())
	}
	return p.redirect(gc, rewardsPage, "reward+"+slug+"+created+(disabled)", "")
}

// actionUpdateReward edits a definition, payout lines included.
//
// The page could create and toggle and nothing else, so a wrong payout or a
// mistyped trigger could only be fixed in SQL — and there is no delete either,
// which made every mistake permanent. Grants already made are untouched: they
// froze their payout lines at grant time precisely so an edit cannot rewrite
// what a member was already promised.
func (p *Plugin) actionUpdateReward(gc *gin.Context) (template.HTML, error) {
	id, _ := strconv.ParseInt(gc.PostForm("id"), 10, 64)
	if id <= 0 {
		return p.redirect(gc, rewardsPage, "", "no such reward")
	}
	slug := strings.TrimSpace(gc.PostForm("slug"))
	// Enabled is not on the edit form — toggling is its own action, and a save
	// that silently re-enabled a reward an operator had switched off would be
	// the worst kind of surprise. Carried through as it stands.
	r, refusal := p.rewardFromForm(gc, slug, gc.PostForm("was_enabled") == "1")
	if refusal != "" {
		return p.redirect(gc, rewardsPage, "", refusal)
	}
	if err := p.admin.UpdateReward(gc.Request.Context(), id, r); err != nil {
		return p.redirect(gc, rewardsPage, "", err.Error())
	}
	return p.redirect(gc, rewardsPage, "reward+"+slug+"+saved", "")
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

// ── lootbox editing ─────────────────────────────────────────────────────────

// lootboxSlug is the shape a box slug must have — the same one the schema's
// CHECK enforces, so the form refuses what the table would refuse anyway. Both
// exist on purpose: one is the message, the other is the guarantee.
var lootboxSlug = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// actionLootboxAdd puts a prize in a box, or re-weights one already there.
//
// Creating a box IS adding its first entry: the box has no row of its own, so
// there is no "create box" to do first and no way to end up with an empty one.
func (p *Plugin) actionLootboxAdd(gc *gin.Context) (template.HTML, error) {
	box := strings.TrimSpace(gc.PostForm("box"))
	if !lootboxSlug.MatchString(box) {
		return p.redirect(gc, rewardsPage, "", "box+names+are+lowercase+with+dashes")
	}
	rewardID, err := strconv.ParseInt(gc.PostForm("reward_id"), 10, 64)
	if err != nil || rewardID <= 0 {
		return p.redirect(gc, rewardsPage, "", "pick+a+reward+for+the+box")
	}
	// Weight defaults to 1 rather than 0: zero is refused by the schema, and an
	// operator who left the field alone meant "the same chance as the others",
	// not "never".
	weight := 1
	if n, err := strconv.Atoi(strings.TrimSpace(gc.PostForm("weight"))); err == nil && n > 0 {
		weight = n
	}
	ordinal, _ := strconv.Atoi(strings.TrimSpace(gc.PostForm("ordinal")))
	if err := p.admin.AddLootboxEntry(gc.Request.Context(), LootboxEntry{
		BoxSlug: box, RewardID: rewardID, Weight: weight, Ordinal: ordinal,
	}); err != nil {
		return p.redirect(gc, rewardsPage, "", err.Error())
	}
	// Back to the box that was just edited, not to the top of the page: an
	// operator building a box adds several lines in a row.
	return p.redirect(gc, rewardsPage+"?box="+url.QueryEscape(box), "box+updated", "")
}

// actionLootboxRemove drops one line. Removing the last one unmakes the box,
// which is the only definition of "delete a box" that cannot leave one behind
// with nothing in it.
func (p *Plugin) actionLootboxRemove(gc *gin.Context) (template.HTML, error) {
	id, _ := strconv.ParseInt(gc.PostForm("id"), 10, 64)
	box := strings.TrimSpace(gc.PostForm("box"))
	if id > 0 {
		if err := p.admin.RemoveLootboxEntry(gc.Request.Context(), id); err != nil {
			return p.redirect(gc, rewardsPage, "", err.Error())
		}
	}
	return p.redirect(gc, rewardsPage+"?box="+url.QueryEscape(box), "box+updated", "")
}
