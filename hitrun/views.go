package hitrun

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// The member's own page: what they owe, and why.
//
// The view model is flattened to strings here rather than in the template.
// html/template streams, so a missing method or a nil deref truncates the page
// mid-row and still returns 200 — the failure looks like a styling bug and gets
// found by a member rather than by a test. Everything the template touches is a
// plain field.

type standingVM struct {
	ActiveWarnings int
	Blocked        bool
	Policy         Policy
	SeedtimeText   string
	ExpireDays     int
	AtRisk         []riskRowVM
	Warnings       []warningRowVM
}

type riskRowVM struct {
	Name         string
	SeededText   string
	OwedText     string
	LastSeenText string
}

type warningRowVM struct {
	Name        string
	Reason      string
	ExpiresText string
}

// StandingPage renders a member's hit-and-run standing.
func (h *Handlers) StandingPage(c *gin.Context) {
	u, ok := h.auth.CurrentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}
	ctx := c.Request.Context()

	st, err := h.store.Standing(ctx, u.ID)
	if err != nil {
		h.renderError(c, err)
		return
	}
	snatches, err := h.store.UserSnatches(ctx, u.ID)
	if err != nil {
		h.renderError(c, err)
		return
	}

	p := h.policy()
	vm := standingVM{
		ActiveWarnings: st.ActiveWarnings,
		Blocked:        DownloadsBlocked(p, st.ActiveWarnings),
		Policy:         p,
		SeedtimeText:   humanDuration(time.Duration(p.Seedtime) * time.Second),
		ExpireDays:     p.ExpireDays,
	}
	for _, s := range snatches {
		if !AtRisk(p, s.Snatch) {
			continue
		}
		vm.AtRisk = append(vm.AtRisk, riskRowVM{
			Name:         s.TorrentName,
			SeededText:   humanDuration(time.Duration(s.Snatch.Seedtime) * time.Second),
			OwedText:     humanDuration(Owed(p, s.Snatch)),
			LastSeenText: h.relative(s.Snatch.LastSeen),
		})
	}
	for _, w := range st.Warnings {
		name := w.TorrentName
		if name == "" {
			// The torrent was removed after the warning. Say so rather than
			// showing an empty cell, which reads as a rendering fault.
			name = "(removed torrent)"
		}
		vm.Warnings = append(vm.Warnings, warningRowVM{
			Name:        name,
			Reason:      w.Reason,
			ExpiresText: h.relative(w.ExpiresAt),
		})
	}

	var buf bytes.Buffer
	if err := h.tmpl.ExecuteTemplate(&buf, "standing.html", vm); err != nil {
		h.renderError(c, err)
		return
	}
	h.render(c, "Seeding requirements", template.HTML(buf.String()))
}

// humanDuration renders a span the way a member reads it. "168h0m0s" is
// technically the requirement and tells nobody anything.
func humanDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "—"
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= 24*time.Hour:
		return "1 day"
	case d >= time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
}
