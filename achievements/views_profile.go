package achievements

import (
	"html/template"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// The achievements card on a member's public profile.
//
// It renders for the profile SUBJECT (core.ViewSubject), not the viewer — the
// same contract the daily-streak and agent-fleet cards use — so looking at
// someone else's profile shows THEIR badges.

// profileAchievement is one badge as the card shows it.
type profileAchievement struct {
	Achievement
	// PercentDone is progress toward the threshold, 0-100, for the bar on a
	// locked achievement. Clamped: a metric that overshoots (615 of 500)
	// would otherwise render a bar wider than its track.
	PercentDone int
}

// profileVM keeps earned and in-progress apart, because they answer different
// questions — "what have they done" versus "what are they close to".
type profileVM struct {
	Earned     []profileAchievement
	InProgress []profileAchievement
	Unlocked   int
	Pending    int
	Locked     int
	// Self is true when the viewer is looking at their own profile. Progress
	// on something not yet earned is shown only to the member themselves: on
	// someone else's profile it is a list of things they have failed to do.
	Self bool
}

func (p *Plugin) registerProfileWidget(c *core.Core) error {
	return c.RegisterView(core.View{
		Slug: "achievements", Title: "Achievements", Slot: core.SlotUserWidget,
		// Public: earned badges are a public fact about a member — that is
		// what makes them worth earning. The in-progress half is gated below.
		Public: true,
		Render: p.renderProfileAchievements,
	})
}

func (p *Plugin) renderProfileAchievements(gc *gin.Context) (template.HTML, error) {
	subject, ok := core.ViewSubject(gc)
	if !ok {
		return "", nil
	}
	if p.store == nil {
		return "", nil
	}
	ctx := gc.Request.Context()

	self := false
	if u, ok := p.viewer(gc); ok && u.ID == subject {
		self = true
	}

	// The member's opt-out, and the only place it is enforced.
	//
	// It has to be here rather than in the host, because this is the moment
	// the card decides what a non-subject viewer gets, and it is the only
	// moment every host shares — the widget is mounted on whatever profile
	// pages a host has, and a host-side filter would have to be re-added by
	// each of them. Before the query, not after: a member who said "do not
	// publish this" should not have it read either.
	//
	// Their OWN profile is unaffected, whichever page it is on. Hiding a
	// member's badges from themselves would leave them with no way to see what
	// they have earned, and nothing to decide about.
	if !self {
		hidden, err := p.store.ProfileHidden(ctx, subject)
		if err != nil {
			return "", err
		}
		if hidden {
			return "", nil
		}
	}

	return p.renderCardFor(gc, subject, self)
}

// viewer is the signed-in user, or nothing. Wrapped because the plugin runs on
// hosts and in tests where core or its auth service is absent, and every call
// site would otherwise repeat the same three nil checks before it could ask
// the only question it cares about.
func (p *Plugin) viewer(gc *gin.Context) (*core.User, bool) {
	if p.core == nil || p.core.Auth == nil {
		return nil, false
	}
	u, ok := p.core.Auth.CurrentUser(gc)
	if !ok || u == nil {
		return nil, false
	}
	return u, true
}

// renderCardFor builds one member's achievements card: the read, the
// per-viewer localization, the visibility rules, the fragment.
//
// Shared by the profile widget and the plugin's own page, so a member reading
// /p/achievements sees the same card other people see on their profile rather
// than a second rendering of the same idea that drifts from it. self carries
// the in-progress rule (see buildProfileVM) and is the caller's to decide,
// because "whose page is this" is a question only the caller can answer.
func (p *Plugin) renderCardFor(gc *gin.Context, userID int64, self bool) (template.HTML, error) {
	all, err := p.store.Achievements(gc.Request.Context(), userID)
	if err != nil {
		return "", err
	}
	// Localize the display names for THIS viewer before the pure VM build —
	// buildProfileVM stays a pure function over the list, and localization is
	// a per-request fact that belongs with the request.
	for i := range all {
		all[i].Name = p.localizedName(gc, all[i])
	}
	vm := buildProfileVM(all, self)

	// Nothing to say: render nothing rather than an empty card. A "no
	// achievements yet" box on every profile forever is how a card gets
	// ignored before it ever has content.
	if len(vm.Earned) == 0 && len(vm.InProgress) == 0 {
		return "", nil
	}

	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "profile_achievements.html", vm); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

// buildProfileVM applies the card's visibility rules.
//
// Pure over the achievement list and one boolean, so every rule below is
// unit-tested rather than asserted about through rendered HTML: which badges
// are public, which progress is private, what a hidden achievement does
// before it is earned, and what an overshooting counter does to a progress
// bar.
func buildProfileVM(all []Achievement, self bool) profileVM {
	vm := profileVM{Self: self}
	vm.Unlocked, vm.Pending, vm.Locked = AchievementCounts(all)

	for _, a := range all {
		// A hidden achievement stays secret until earned — that is the whole
		// point of the flag, and listing it as "locked: ???" still tells the
		// member one exists. (The store withholds these too; this filter
		// keeps the rule true for any caller that hands the card a raw list.)
		if a.Hidden && !a.Earned() {
			continue
		}
		row := profileAchievement{Achievement: a}
		if a.Threshold > 0 {
			pct := int(a.Progress * 100 / a.Threshold)
			if pct > 100 {
				// A metric read can legitimately overshoot (615 of 500).
				// Without this the bar renders wider than its track.
				pct = 100
			}
			if pct < 0 {
				pct = 0
			}
			row.PercentDone = pct
		}
		if a.Earned() {
			vm.Earned = append(vm.Earned, row)
			continue
		}
		// Only the member sees what they are partway through, and only if
		// they have actually started: a full list of everything unearned is
		// not a profile card, it is a chore list.
		if self && a.Progress > 0 {
			vm.InProgress = append(vm.InProgress, row)
		}
	}

	// Earned most-recent first — the newest badge is the interesting one.
	sort.SliceStable(vm.Earned, func(i, j int) bool {
		return vm.Earned[i].EarnedAt.After(vm.Earned[j].EarnedAt)
	})
	// In-progress closest-first, so the one worth pushing on is at the top.
	sort.SliceStable(vm.InProgress, func(i, j int) bool {
		return vm.InProgress[i].PercentDone > vm.InProgress[j].PercentDone
	})
	return vm
}

// localizedName resolves an achievement's display title for this viewer: the
// catalogue slug when one is set AND the host wired a resolver, the plain
// Name otherwise. The wiki content block cannot do this — it renders from a
// bare context with no viewer to have a locale — which is why the fallback
// text stays mandatory on every definition.
func (p *Plugin) localizedName(gc *gin.Context, a Achievement) string {
	if a.TitleSlug != "" && p.l10n != nil {
		if t, ok := p.l10n(gc, a.TitleSlug); ok && t != "" {
			return t
		}
	}
	return a.Name
}
