package rewards

import (
	"html/template"
	"log"
	"net/http"
	"net/url"
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
	Grants    []claimRow
	Msg       string
	Err       string
	CSRFToken string
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

	vm := claimVM{Msg: gc.Query("claimed"), Err: gc.Query("claim_err"), CSRFToken: p.csrfToken(gc)}
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
	// Two dialects, like the daily reward's claim: JSON for the card's fetch
	// (X-Requested-With: fetch), a redirect for a plain form POST. finish is
	// the single exit so the two cannot drift.
	//
	// The redirect goes BACK WHERE THE FORM WAS, not to "/". The old handler
	// hardcoded the home page, so claiming from /rewards teleported the member
	// to the front page with ?claimed=reward+claimed in the bar — which is how
	// this was reported. The Referer is the member's own browser echoing the
	// page they were on; only its PATH is used, so it cannot redirect off-site.
	finish := func(status int, ok bool, msg string) {
		if gc.GetHeader("X-Requested-With") == "fetch" {
			key := "err"
			if ok {
				key = "msg"
			}
			gc.JSON(status, gin.H{"claimed": ok, key: msg})
			return
		}
		back := "/"
		if ref, err := url.Parse(gc.GetHeader("Referer")); err == nil && strings.HasPrefix(ref.Path, "/") {
			back = ref.Path
		}
		key := "claim_err"
		if ok {
			key = "claimed"
		}
		gc.Redirect(http.StatusSeeOther, back+"?"+key+"="+url.QueryEscape(msg))
	}

	u, ok := p.core.Auth.CurrentUser(gc)
	if !ok || u == nil {
		if gc.GetHeader("X-Requested-With") == "fetch" {
			gc.JSON(http.StatusUnauthorized, gin.H{"err": "sign in to claim"})
			return
		}
		gc.Redirect(http.StatusSeeOther, "/login")
		return
	}
	grantID, err := strconv.ParseInt(gc.PostForm("grant_id"), 10, 64)
	if err != nil {
		finish(http.StatusBadRequest, false, "bad request")
		return
	}
	if err := p.engine.ClaimGrant(gc.Request.Context(), int64(u.ID), grantID); err != nil {
		// Deliberately vague to the member and specific in the log: the
		// difference between "not yours" and "already claimed" is useful to an
		// operator and useful to an attacker.
		log.Printf("rewards: claim refused for user %d grant %d: %v", int64(u.ID), grantID, err)
		finish(http.StatusConflict, false, "that reward is no longer available")
		return
	}
	finish(http.StatusOK, true, "Reward claimed")
}

// csrfToken reads the host's token func off the registry, or "" when no host
// registered one — the form then posts tokenless and the host middleware
// answers, which is the pre-seam behaviour rather than a new failure.
func (p *Plugin) csrfToken(gc *gin.Context) string {
	if v, ok := p.core.Lookup(CSRFExtension); ok {
		if fn, ok := v.(func(*gin.Context) string); ok {
			return fn(gc)
		}
	}
	return ""
}
