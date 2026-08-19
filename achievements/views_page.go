package achievements

import (
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The member-facing achievements page: /p/<slug>, a loon SlotSitePage.
//
// It exists because the badges had nowhere to lead. The card on a profile is a
// summary with no destination, the catalogue block only appears where an
// editor embedded it, and the admin page is for the operator who defines them
// — so "what are these, and how do I get one" had no answer a member could
// click. This page is that answer: what you have, what everyone can earn, and
// the one control over how it is published.
//
// It also carries the visibility opt-out, and that placement is the reason the
// preference is plugin-side at all. loon has no user-settings slot, and the
// host mounts actions for site pages but not for profile widgets — so a page
// this plugin owns is the only surface where a plugin-owned setting can be
// both read and written without the host growing a column, a form field and a
// POST handler on the plugin's behalf.

// memberPageVM is the page as a member reads it.
type memberPageVM struct {
	// SignedIn separates "you have earned none of these" from "we do not know
	// who you are", which are different sentences on the same page.
	SignedIn bool
	// Card is the member's own achievements card — the same fragment their
	// profile shows — or empty when they have earned nothing and started
	// nothing.
	Card template.HTML
	// Hidden is the current state of the opt-out, so the checkbox reflects the
	// stored answer rather than always rendering unticked.
	Hidden bool
	// Catalogue is every enabled, non-secret achievement, rendered by the same
	// code the wiki content block uses.
	Catalogue template.HTML
	CSRFToken string
	Msg, Err  string
}

// registerMemberPage mounts the page at the host's site-page convention
// (GET /p/achievements, POST /p/achievements/visibility).
//
// The slug repeats this plugin's other two views on purpose: loon scopes slugs
// per SLOT, and the admin page, the profile card and this page are three views
// of one subject. Sharing the name is what makes /p/achievements and
// /admin/p/achievements read as the member's and the operator's view of the
// same thing.
//
// Public, deliberately. The catalogue half answers "what can be earned here",
// which is exactly the question a visitor deciding whether to join has, and it
// names no member — the personal half renders only for whoever is signed in,
// about themselves. A host that is members-only still gates it, because the
// host's own access mode runs before any view is consulted.
func (p *Plugin) registerMemberPage(c *core.Core) error {
	return c.RegisterView(core.View{
		Slug: "achievements", Title: "Achievements", Slot: core.SlotSitePage,
		Description: "What can be earned here, and where you stand.",
		Public:      true,
		Nav:         core.NavHint{Group: "Community"},
		Render:      p.renderMemberPage,
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"visibility": p.actionSetVisibility,
		},
	})
}

func (p *Plugin) renderMemberPage(gc *gin.Context) (template.HTML, error) {
	if p.store == nil || p.tmpl == nil {
		return "", nil
	}
	ctx := gc.Request.Context()
	vm := memberPageVM{
		Msg:       gc.Query("msg"),
		Err:       pageError(gc),
		CSRFToken: pluginapi.CSRFToken(p.core, gc),
	}

	catalogue, err := p.renderCatalogue(ctx)
	if err != nil {
		return "", err
	}
	vm.Catalogue = catalogue

	if u, ok := p.viewer(gc); ok {
		vm.SignedIn = true
		// self=true: this is the member's own page, so their in-progress bars
		// belong on it. That is the same rule the profile card applies, read
		// from the same function.
		card, err := p.renderCardFor(gc, u.ID, true)
		if err != nil {
			return "", err
		}
		vm.Card = card
		hidden, err := p.store.ProfileHidden(ctx, u.ID)
		if err != nil {
			return "", err
		}
		vm.Hidden = hidden
	}

	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "achievements_page.html", vm); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

// pageError is the banner text after a failed action, from either marker.
//
// Two of them, because two different writers set one. The action's own refusal
// redirects to ?err=<text>, the msg/err convention every plugin here uses. But
// an action that RETURNS an error never reaches its own redirect: the host's
// site-page wrapper catches it, logs it, and sends the member back with a bare
// marker of the host's choosing — prod uses ?error=1, and it carries no text
// to show.
//
// Reading only ?err= therefore renders a member whose privacy choice failed to
// save a page with no message at all, which they would read as success. A
// generic sentence is not much, but "something went wrong" is the difference
// between retrying and believing you are hidden when you are not.
func pageError(gc *gin.Context) string {
	if msg := gc.Query("err"); msg != "" {
		return msg
	}
	if gc.Query("error") != "" {
		return "That did not save. Please try again."
	}
	return ""
}

// actionSetVisibility records the member's choice about publishing their
// badges.
//
// The signed-in check is this action's own. The view is Public so the page can
// be read by anyone, and the host's site-page group carries SoftAuth — it
// loads a viewer without requiring one — so a Public view's action is reachable
// anonymously. There is no user to store a choice against then, and refusing
// here is cheaper than making the view non-public and losing the catalogue for
// visitors.
//
// An absent checkbox means SHOWN. That is what an unticked box posts, and it
// is also what a form submitted from a stale page posts, so both read as the
// default rather than as a silent opt-out.
func (p *Plugin) actionSetVisibility(gc *gin.Context) (template.HTML, error) {
	u, ok := p.viewer(gc)
	if !ok {
		gc.Redirect(http.StatusSeeOther, "/login")
		return "", nil
	}
	if p.store == nil {
		gc.Redirect(http.StatusSeeOther, "/p/achievements?err="+url.QueryEscape("Achievements are not available right now."))
		return "", nil
	}
	hide := gc.PostForm("hidden") == "1"
	if err := p.store.SetProfileHidden(gc.Request.Context(), u.ID, hide); err != nil {
		// Returned, not swallowed: the host logs it and redirects with an
		// error marker. A privacy choice that silently failed to save would
		// leave the member believing they had hidden something.
		return "", err
	}
	msg := "Your achievements are shown on your public profile."
	if hide {
		msg = "Your achievements are now hidden from everyone but you."
	}
	gc.Redirect(http.StatusSeeOther, "/p/achievements?msg="+url.QueryEscape(msg))
	return "", nil
}
