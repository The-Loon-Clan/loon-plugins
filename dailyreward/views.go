package dailyreward

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// today/yesterday return civil date strings in UTC.
func today() string     { return time.Now().UTC().Format("2006-01-02") }
func yesterday() string { return time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02") }

// nextStreak is the streak a claim right now would produce (for the button label).
func nextStreak(st State) int {
	switch st.LastClaim {
	case today():
		return st.Streak // already claimed
	case yesterday():
		return st.Streak + 1
	default:
		return 1
	}
}

func (p *Plugin) captchaWidget() template.HTML {
	if p.captcha == nil {
		return ""
	}
	return p.captcha.WidgetHTML()
}

func (p *Plugin) renderWidget(c *gin.Context) (template.HTML, error) {
	u, ok := p.core.Auth.CurrentUser(c)
	if !ok {
		return "", nil // MinRole gate should prevent this; render nothing
	}
	st, err := p.st.Get(c.Request.Context(), u.ID)
	if err != nil {
		return "", err
	}
	claimed := st.LastClaim == today()
	var buf bytes.Buffer
	if err := p.tmpl.ExecuteTemplate(&buf, "widget.html", map[string]any{
		"Streak":  st.Streak,
		"Longest": st.Longest,
		"Total":   st.TotalClaims,
		"Claimed": claimed,
		"Reward":  rewardFor(nextStreak(st)),
		"Captcha": p.captchaWidget(),
	}); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

// renderProfileStreak fills the SlotUserWidget streak card for the profile
// subject (core.ViewSubject), not the current viewer.
func (p *Plugin) renderProfileStreak(c *gin.Context) (template.HTML, error) {
	id, ok := core.ViewSubject(c)
	if !ok {
		return "", nil
	}
	st, err := p.st.Get(c.Request.Context(), id)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := p.tmpl.ExecuteTemplate(&buf, "profile_streak.html", map[string]any{
		"Streak": st.Streak, "Longest": st.Longest, "Total": st.TotalClaims,
	}); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

// wantsJSON reports whether the caller is the widget's fetch() rather than a
// browser submitting the form.
//
// The widget sets X-Requested-With explicitly instead of relying on Accept,
// because a browser's form POST also sends an Accept header that lists
// several types, and matching on it would turn the no-JS path into JSON the
// user would be shown as a page of text.
func wantsJSON(c *gin.Context) bool {
	return c.GetHeader("X-Requested-With") == "fetch"
}

func (p *Plugin) claim(c *gin.Context) {
	// finish answers in whichever dialect the caller asked for: JSON for the
	// widget's fetch, a redirect for a plain form POST. Every exit below goes
	// through it, so the two paths cannot drift.
	finish := func(status int, payload gin.H, redirect string) {
		if wantsJSON(c) {
			c.JSON(status, payload)
			return
		}
		c.Redirect(http.StatusSeeOther, redirect)
	}

	u, ok := p.core.Auth.CurrentUser(c)
	if !ok {
		finish(http.StatusUnauthorized, gin.H{"error": "sign in to claim"}, "/login")
		return
	}
	if p.captcha != nil {
		if err := p.captcha.Verify(c.Request.Context(), c.PostForm("cf-turnstile-response"), c.ClientIP()); err != nil {
			finish(http.StatusBadRequest, gin.H{"error": "captcha failed"}, "/")
			return
		}
	}
	streak, reward, claimed, err := p.st.Claim(c.Request.Context(), u.ID, today(), yesterday())
	if err != nil {
		p.core.LoggerFor("dailyreward").Error("claim", "err", err)
		finish(http.StatusInternalServerError, gin.H{"error": "could not claim right now"}, "/")
		return
	}
	if claimed {
		// Award FIRST, announce second. The emit sat above the award, so a
		// failed Award still announced Claimed{Reward: N} — an event asserting
		// points that were never paid, to every subscriber scoring on it.
		//
		// The claim row is already committed either way, so on a failed award
		// the member's streak advanced and their points did not — that
		// inconsistency existed before this reorder and is logged loudly; what
		// the reorder fixes is the LIE. The event now reports Reward: 0 in
		// that case, which is what actually happened, and `claimed` false
		// still emits nothing because somebody else took today's.
		paid := reward
		if _, err := p.core.Points.Award(c.Request.Context(), u.ID, reward, "earn_daily",
			fmt.Sprintf("Daily login reward (streak %d)", streak), 0); err != nil {
			p.core.LoggerFor("dailyreward").Error("award", "err", err, "user", u.ID, "amount", reward)
			paid = 0
		}
		p.core.Emit(c.Request.Context(), core.Event{
			Name: EventClaimed, UserID: u.ID,
			Data: Claimed{Streak: streak, Reward: paid},
		})
		// Tell the user via the notification pipeline (fans out to the inbox bell,
		// the logger, and any other channel the host registered). System event,
		// so no actor.
		_ = p.core.Notifications.Notify(c.Request.Context(), u.ID, core.Notification{
			Kind:  "daily_reward",
			Title: "Daily reward claimed",
			// paid, not reward: after a failed award "you earned N points" is
			// exactly the claim the reorder above stopped the event making.
			Body: fmt.Sprintf("You earned %d points (streak %d).", paid, streak),
		})
	}
	// The widget redraws itself from these; the totals come back from the same
	// place the template reads them, so a claim shows the same numbers a reload
	// would. claimed=false means somebody already took today's — not an error,
	// and the widget says so rather than pretending it worked.
	st, err := p.st.Get(c.Request.Context(), u.ID)
	if err != nil {
		p.core.LoggerFor("dailyreward").Error("state after claim", "err", err)
	}
	finish(http.StatusOK, gin.H{
		"claimed": claimed,
		"streak":  streak,
		"reward":  reward,
		"longest": st.Longest,
		"total":   st.TotalClaims,
	}, "/")
}

// StatusExtension is the registry key the per-user claim state is published
// under. A host looks it up after Boot, the way it looks up news.home.
//
// It exists because the claim state was reachable only from INSIDE this
// plugin's own widget: the host could render the card but could not answer
// "may this member claim right now?", so it had no way to show a compact
// indicator anywhere else — a stat-bar button, a nav badge — without either
// duplicating the once-per-day rule or reading this plugin's table.
const StatusExtension = "dailyreward.status"

// Status is what a host needs to decide whether to offer a claim, and nothing
// more. Deliberately not the whole record: Longest and Total belong to the
// cards that display them, and a seam that hands over everything is one that
// has to change every time the record does.
type Status struct {
	// Claimed is true once today's reward has been taken. A host showing a
	// claim control should hide it.
	Claimed bool
	Streak  int
	// LastClaim is the civil date of the most recent claim ("YYYY-MM-DD"), or
	// "" if the member has never claimed.
	//
	// It ships with Streak because Streak ALONE is uninterpretable: the stored
	// count does not decay on read, so a member who last claimed a year ago
	// still reports the streak they had then, and only resets when they claim
	// again. A host that labels a control "12 day streak" off Streak by itself
	// is stating something that stopped being true eleven months ago. With the
	// date the host can tell a live streak from a stale one — and can place
	// the run on a calendar, since the claimed days are the Streak days ending
	// on LastClaim.
	LastClaim string
	// Reward is what a claim made RIGHT NOW would pay, so a host can label
	// its control. When Claimed is already true there is no such claim, and
	// this reports today's payout rather than tomorrow's — read it only when
	// Claimed is false, which is the only time a host draws the control.
	Reward int
}

// LiveStreak reports the streak only when it is still running — the member
// claimed today, or claimed yesterday and can still keep it alive today.
// Anything older is a lapsed run that the next claim will reset to 1, so it is
// reported as 0 rather than as a number a caller would display as current.
func (s Status) LiveStreak(now time.Time) int {
	if s.Streak <= 0 || s.LastClaim == "" {
		return 0
	}
	switch s.LastClaim {
	case now.UTC().Format("2006-01-02"), now.UTC().AddDate(0, 0, -1).Format("2006-01-02"):
		return s.Streak
	}
	return 0
}

// StatusFunc is the extension's type.
type StatusFunc func(ctx context.Context, userID int64) (Status, error)

// status reports one member's claim state.
func (p *Plugin) status(ctx context.Context, userID int64) (Status, error) {
	st, err := p.st.Get(ctx, userID)
	if err != nil {
		return Status{}, err
	}
	return Status{
		Claimed:   st.LastClaim == today(),
		Streak:    st.Streak,
		LastClaim: st.LastClaim,
		Reward:    rewardFor(nextStreak(st)),
	}, nil
}
