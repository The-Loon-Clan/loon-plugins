package rewards

import (
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// The member's side of delivery='claim'.
//
// A pending grant with nowhere to collect it is worse than no grant at all:
// the member has been promised something they cannot reach, and the only trace
// is a row in a table they cannot see. This card is what makes claim delivery
// a real option rather than a half-built one — and it is what lets a reward be
// offered to someone who appears once a year.

type claimVM struct {
	Grants []claimRow
	Msg    string
	Err    string
}

type claimRow struct {
	ID      int64
	Pays    string
	Expires string
}

func (p *Plugin) registerMemberViews(c *core.Core) error {
	// A plugin route rather than a View Action: SlotSiteWidget ignores
	// Actions, and this POST needs the host middleware stack (auth, CSRF)
	// that Router.Mount gives it.
	c.Router.Mount("rewards").POST("/claim", p.memberClaim)

	return c.RegisterView(core.View{
		Slug: "rewards-claim", Title: "Rewards to claim", Slot: core.SlotSiteWidget,
		MinRole: core.RoleUser, // signed-in only; there is nothing to show otherwise
		Render:  p.renderClaimCard,
	})
}

// renderClaimCard shows nothing at all when there is nothing to claim.
//
// An empty "you have no rewards" card is noise on every page load for every
// member forever, which is how a widget gets ignored — and then the one time
// it DOES have something, it is already invisible.
func (p *Plugin) renderClaimCard(gc *gin.Context) (template.HTML, error) {
	u, ok := p.core.Auth.CurrentUser(gc)
	if !ok || u == nil {
		return "", nil
	}
	grants, err := p.engine.Pending(gc.Request.Context(), int64(u.ID))
	if err != nil {
		return "", err
	}
	if len(grants) == 0 {
		return "", nil
	}

	vm := claimVM{Msg: gc.Query("claimed"), Err: gc.Query("claim_err")}
	for _, g := range grants {
		row := claimRow{ID: g.ID, Pays: describePayouts(g.Payouts)}
		if g.ExpiresAt != nil {
			row.Expires = g.ExpiresAt.UTC().Format("2 Jan 2006")
		}
		vm.Grants = append(vm.Grants, row)
	}
	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "claim_card.html", vm); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

// describePayouts renders what a grant hands over, in a member's words.
func describePayouts(ps []Payout) string {
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		switch p.Kind {
		case PayoutPoints:
			parts = append(parts, strconv.Itoa(p.Amount)+" points")
		case PayoutMedal:
			parts = append(parts, "the "+p.Target+" medal")
		default:
			parts = append(parts, string(p.Kind)+" "+p.Target)
		}
	}
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, " and ")
}

// memberClaim collects one grant.
//
// The member id comes from the SESSION, never the form. A grant_id posted by a
// caller is only ever used to look one up; ClaimGrant then refuses anything
// that is not theirs, and refuses it identically to a grant that does not
// exist so the endpoint cannot be walked to discover valid ids.
func (p *Plugin) memberClaim(gc *gin.Context) {
	u, ok := p.core.Auth.CurrentUser(gc)
	if !ok || u == nil {
		gc.Redirect(http.StatusSeeOther, "/login")
		return
	}
	grantID, err := strconv.ParseInt(gc.PostForm("grant_id"), 10, 64)
	if err != nil {
		gc.Redirect(http.StatusSeeOther, "/?claim_err=bad+request")
		return
	}
	if err := p.engine.ClaimGrant(gc.Request.Context(), int64(u.ID), grantID); err != nil {
		// Deliberately vague to the member and specific in the log: the
		// difference between "not yours" and "already claimed" is useful to an
		// operator and useful to an attacker.
		log.Printf("rewards: claim refused for user %d grant %d: %v", int64(u.ID), grantID, err)
		gc.Redirect(http.StatusSeeOther, "/?claim_err=that+reward+is+no+longer+available")
		return
	}
	gc.Redirect(http.StatusSeeOther, "/?claimed=reward+claimed")
}
