package cosmetics

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The staff queue for custom titles.
//
// Titles are the only thing this plugin publishes that somebody typed, so they
// are the only thing with a staff surface — and the queue is not a formality.
// A title appears under a member's name on every page they appear on, next to
// other people's names, which makes it the highest-leverage piece of
// user-supplied text on the site per character.

const queuePath = "/admin/p/titles"

// queueRowVM is one submission waiting to be read.
type queueRowVM struct {
	UserID    int64
	Username  string
	Text      string
	Submitted time.Time
	// Previous is what this member had approved before, when they are editing
	// rather than proposing a first one. Shown because "they changed a passed
	// title to this" is a different judgement from "they asked for this", and a
	// moderator with only the new words cannot tell the two apart.
	Previous string
}

func (p *Plugin) queuePage(c *gin.Context) (template.HTML, error) {
	ctx := c.Request.Context()
	rows, err := p.st.PendingTitles(ctx, 0)
	if err != nil {
		return "", err
	}
	names := map[int64]string{}
	if p.users != nil && len(rows) > 0 {
		ids := make([]int64, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.UserID)
		}
		if got, err := p.users.BulkDisplayNames(ctx, ids); err == nil {
			names = got
		}
	}
	out := make([]queueRowVM, 0, len(rows))
	for _, r := range rows {
		name := names[r.UserID]
		vm := queueRowVM{
			UserID: r.UserID, Username: name, Text: r.Text, Submitted: r.SubmittedAt,
		}
		// The published title, if any — read through the same resolver the
		// site renders from, so what a moderator is comparing against is
		// literally what everybody else can see.
		if name != "" && p.core != nil {
			vm.Previous = pluginapi.MemberTitle(p.core, name).Text
			if vm.Previous == vm.Text {
				vm.Previous = ""
			}
		}
		out = append(out, vm)
	}
	return p.render("cosmetics_queue.html", map[string]any{
		"CSRF":  pluginapi.CSRFToken(p.core, c),
		"Rows":  out,
		"Err":   c.Query("err"),
		"Taken": c.Query("taken") == "1",
	})
}

// review passes or refuses one title.
func (p *Plugin) review(c *gin.Context) (template.HTML, error) {
	u, ok := p.core.Auth.CurrentUser(c)
	if !ok || u == nil || !u.AtLeast(core.RoleMod) {
		c.Redirect(http.StatusSeeOther, "/login")
		return "", nil
	}
	userID, _ := strconv.ParseInt(c.PostForm("user"), 10, 64)
	approve := c.PostForm("decision") == "approve"
	reason := strings.TrimSpace(c.PostForm("reason"))
	if userID <= 0 {
		c.Redirect(http.StatusSeeOther, queuePath+"?err=bad")
		return "", nil
	}
	// A refusal needs words. A member told only "no" resubmits the same thing,
	// and the moderator reads it again — the reason is what makes the queue
	// converge rather than loop.
	if !approve && reason == "" {
		c.Redirect(http.StatusSeeOther, queuePath+"?err=noreason")
		return "", nil
	}
	if len(reason) > 300 {
		reason = reason[:300]
	}
	done, err := p.st.ReviewTitle(c.Request.Context(), userID, u.ID, approve, reason)
	switch {
	case err != nil:
		c.Redirect(http.StatusSeeOther, queuePath+"?err=failed")
	case !done:
		// Somebody else decided it first. Told rather than swallowed, because a
		// moderator who typed a rejection and sees the row vanish should know
		// their words were not what happened.
		c.Redirect(http.StatusSeeOther, queuePath+"?taken=1")
	default:
		c.Redirect(http.StatusSeeOther, queuePath)
	}
	return "", nil
}
