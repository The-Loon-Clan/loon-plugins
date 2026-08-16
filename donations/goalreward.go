package donations

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// When the site's monthly goal is met, everybody gets something.
//
// The counterpart to a personal tier. A personal goal is one member's own total
// crossing a threshold and is scored as an achievement; this is the MEMBERSHIP's
// total crossing one, and there is no per-member progress to score — the whole
// site crosses it at once, or nobody does.
//
// So nothing is granted. This opens a named scheduled-event WINDOW and stops
// there. What the window means is somebody else's business: perks turns
// site-freeleech into a download multiplier of zero for every announce, and a
// second consumer could hang anything else off the same window without this
// file learning about it. That separation is the point — donations knows about
// money and goals, and knows nothing about torrents.
//
// OFF UNLESS CONFIGURED. An empty donate_goal_reward_event means no window is
// ever opened, which is the right default for a site whose goals are a
// thermometer rather than a promise.

const (
	// The event slug to open. Empty disables the whole mechanism.
	settingGoalRewardEvent = "donate_goal_reward_event"
	// How long the window stays open, in hours. Default below.
	settingGoalRewardHours = "donate_goal_reward_hours"
	// Which goal group counts. Default "site" — the group whose costs are the
	// site's own bills, and the one lockingGroups already treats as special.
	settingGoalRewardGroup = "donate_goal_reward_group"
	// The last period this fired, as "2006-01". See goalRewardDue.
	settingGoalRewardFired = "donate_goal_reward_fired"
)

// defaultGoalRewardHours is seven days, matching perks' own token duration and
// the hit-and-run seedtime requirement. Three numbers that mean different things
// and should move together; a site changing one should look at the others.
const defaultGoalRewardHours = 168

// goalRewardPeriod names the period a monthly goal belongs to.
//
// Month granularity because the goal it watches is the MONTHLY one — a site
// that meets its bills in January and again in February should be free twice.
func goalRewardPeriod(now time.Time) string { return now.UTC().Format("2006-01") }

// goalRewardDue decides whether to open a window, and is the whole rule.
//
// Pure, and separated from everything around it, because the interesting part
// is not the plumbing — it is the two ways this goes wrong, both silent:
//
//	no goal set        A group with no monthly costs has goal 0, and `raised >=
//	                   0` is true of every site that has ever received a penny.
//	                   Without the first clause a site with an empty cost list
//	                   would declare itself funded and go free forever.
//	fired already      The window closes after a week; the goal stays met for
//	                   the rest of the month. Without the period check the next
//	                   donation after it closed would open a second week, and
//	                   the one after that a third — a site permanently free from
//	                   the day it first met its bills.
func goalRewardDue(raised, goal float64, firedPeriod, nowPeriod string) bool {
	if goal <= 0 || raised < goal {
		return false
	}
	return firedPeriod != nowPeriod
}

// maybeOpenGoalReward opens the reward window if this donation is the one that
// met the site's monthly goal.
//
// Best-effort and never fatal: the donation is already committed and announced,
// and failing here cannot un-receive the money. Every problem is reported and
// swallowed rather than returned, for the reason the achievements handler gives
// about the same shape — a member's payment must not fail because a reward could
// not be arranged.
func (h *Handlers) maybeOpenGoalReward(ctx context.Context) {
	if h.deps.Settings == nil || h.core == nil {
		return
	}
	slug, _ := h.deps.Settings.GetSetting(ctx, settingGoalRewardEvent)
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return // not configured, which is the default
	}

	group, _ := h.deps.Settings.GetSetting(ctx, settingGoalRewardGroup)
	if group = strings.TrimSpace(group); group == "" {
		group = "site"
	}

	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	// The same two figures the donate page's thermometer reads, so the window
	// opens exactly when the bar the members are watching reaches the top. Two
	// ways of computing "are we funded" is how a site ends up free while its
	// own page says it is 80% of the way there.
	raised, err := h.store.SumDonationsSince(ctx, monthStart)
	if err != nil {
		h.errs.Report(ctx, "donations/goal-reward", err)
		return
	}
	goal, err := h.store.SumSiteCostsByGroupPeriod(ctx, group, "monthly")
	if err != nil {
		h.errs.Report(ctx, "donations/goal-reward", err)
		return
	}

	fired, _ := h.deps.Settings.GetSetting(ctx, settingGoalRewardFired)
	period := goalRewardPeriod(now)
	if !goalRewardDue(raised, goal, fired, period) {
		return
	}

	v, ok := h.core.Lookup(pluginapi.ScheduledEventsName)
	if !ok {
		return // no events plugin: nothing can hold a window
	}
	events, ok := v.(pluginapi.ScheduledEvents)
	if !ok {
		return
	}
	if _, _, err := events.OpenWindow(ctx, slug, goalRewardDuration(ctx, h)); err != nil {
		// Reported and NOT marked fired, so the next donation this month tries
		// again. A misconfigured slug is the likely cause and it is worth
		// retrying after somebody fixes it.
		h.errs.Report(ctx, "donations/goal-reward", err)
		return
	}
	// Marked whether this call opened the window or found one already running:
	// either way the site has had its reward for this period, and an operator
	// who opened it by hand should not have it re-opened underneath them.
	if err := h.deps.Settings.SetSetting(ctx, settingGoalRewardFired, period); err != nil {
		// The window IS open, so the reward happened. Without the marker a
		// later donation re-opens after it closes — which OpenWindow will not
		// do while it is running, so the damage is bounded and worth logging
		// rather than unwinding.
		h.errs.Report(ctx, "donations/goal-reward", err)
	}
}

// goalRewardDuration reads the configured window length.
func goalRewardDuration(ctx context.Context, h *Handlers) time.Duration {
	v, _ := h.deps.Settings.GetSetting(ctx, settingGoalRewardHours)
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		n = defaultGoalRewardHours
	}
	return time.Duration(n) * time.Hour
}
