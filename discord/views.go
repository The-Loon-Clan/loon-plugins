package discord

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The /profile Discord card, owned by the plugin that owns Discord.
//
// It used to live in the host: the markup in profile.html, the link lookup and
// the lazy verify-token mint inline in the profile handler, and the unlink as a
// host route. So the bot was a plugin while everything a user could SEE or DO
// with it belonged to the host — and adding a field to this card meant editing
// three host files. It is a loon SlotUserWidget now.
//
// The fragment is a complete card; the host supplies only the page around it.

var cardTmpl = template.Must(template.New("discord-card").Parse(`
<div class="card mb-4">
    <div class="card-header">Discord</div>
    <div class="card-body">
        {{if .Link}}
        <div class="d-flex align-items-center justify-content-between">
            <div class="fs-md">
                <span class="text-success fw-semibold">&#10003; Linked</span>
                &mdash; <strong>{{.Link.DiscordName}}</strong>
                <span class="text-muted fs-xs ms-2">since {{.Link.VerifiedAt.Format "Jan 02, 2006"}}</span>
            </div>
            <form method="POST" action="/profile/discord-unlink" class="d-inline"
                  onsubmit="return confirm('Unlink your Discord account?')">
                <input type="hidden" name="_csrf" value="{{.CSRF}}">
                <button type="submit" class="btn btn-outline-danger btn-sm py-0 px-2 fs-2xs">Unlink</button>
            </form>
        </div>
        {{else}}
        <div style="font-size:0.88rem;color:var(--text-muted);margin-bottom:0.75rem;">
            Link your Discord account to sync your rank role and get notified about new releases.
        </div>
        <div class="d-flex align-items-center gap-2">
            <span class="fs-xs text-muted">Verification Key:</span>
            <code style="background:var(--bg-elevated);padding:0.3rem 0.6rem;border-radius:4px;font-size:0.9rem;user-select:all;">{{.Token}}</code>
        </div>
        <div class="fs-2xs text-muted mt-2">
            In Discord, click the <strong>Verify</strong> button in the verification channel and paste this code.
        </div>
        {{end}}
    </div>
</div>`))

// renderCard is the SlotUserWidget Render.
//
// Renders NOTHING unless the viewer is the profile's owner. The card carries an
// unlink button and, when unlinked, a verification key that anyone holding it
// could use to claim the account from Discord — so it is self-only. loon's
// visibility model is Public/MinRole and cannot express "only the subject", so
// this check is the view's own responsibility; the host publishes the subject
// (core.SetViewSubject) precisely so it can be made.
func (p *Plugin) renderCard(c *gin.Context) (template.HTML, error) {
	subject, ok := core.ViewSubject(c)
	if !ok {
		return "", nil
	}
	viewer := deps.Viewer(c)
	if viewer == nil || int64(viewer.ID) != subject {
		return "", nil
	}
	ctx := c.Request.Context()
	userID := int(subject)

	link, _ := deps.Links.GetDiscordLinkByUserID(ctx, userID)

	// Only mint a token when there is nothing linked — the key is useless to a
	// linked account and minting one per profile view would churn the row.
	var token string
	if link == nil {
		u, err := deps.Users.GetUserByID(ctx, userID)
		if err != nil {
			return "", err
		}
		token = u.VerifyToken
		if token == "" {
			token = generateVerifyToken()
			if err := deps.Links.SetDiscordVerifyToken(ctx, userID, token); err != nil {
				// Showing a key we failed to persist would tell the user to
				// type something the bot will reject.
				return "", err
			}
		}
	}

	var sb strings.Builder
	// The token, baked in. The comment here used to say a "site-wide submit
	// listener" injected it from the cookie at submit time, so a rendered one
	// would be stale — there is no such listener on this host, and the unlink
	// answered 403 for every member who pressed it. A token minted at render
	// time is exactly what every other form on the site carries.
	if err := cardTmpl.Execute(&sb, map[string]any{
		"Link":  link,
		"Token": token,
		"CSRF":  pluginapi.CSRFToken(p.core, c),
	}); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

// unlink drops the viewer's Discord link.
//
// Kept at the host's original URL so nothing user-facing changes, but served by
// the plugin now. Scoped to the session user — the id is never taken from the
// request, so there is no id to forge.
func (p *Plugin) unlink(c *gin.Context) {
	viewer := deps.Viewer(c)
	if viewer == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	if err := deps.Links.DeleteDiscordLink(c.Request.Context(), viewer.ID); err != nil {
		// The host silenced this. An unlink that reports success while the
		// link survives is the one outcome a user cannot detect themselves.
		p.errs.Report(c.Request.Context(), "discord/unlink", err)
		c.Redirect(http.StatusFound, "/profile?error=unlink+failed")
		return
	}
	c.Redirect(http.StatusFound, "/profile")
}
