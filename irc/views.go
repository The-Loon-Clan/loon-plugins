package irc

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The /profile IRC card, owned by the plugin that owns IRC. Mirror of the
// discord one — see the discord plugin's views.go for the reasoning; this is
// the second widget through the same seam rather than a new idea.
//
// Richer than Discord's because the instructions depend on the bot's own
// config: without a server and nick there is nothing useful to tell the user,
// so the card says so instead of printing a half command.

var cardTmpl = template.Must(template.New("irc-card").Parse(`
<div class="card mb-4">
    <div class="card-header">IRC</div>
    <div class="card-body">
        {{if .Link}}
        <div class="d-flex align-items-center justify-content-between">
            <div class="fs-md">
                <span class="text-success fw-semibold">&#10003; Linked</span>
                &mdash; <strong>{{.Link.IRCNick}}</strong>
                {{if .Link.AccountName}}<span style="color:var(--text-muted);font-size:0.78rem;margin-left:0.4rem;">(account: {{.Link.AccountName}})</span>{{end}}
                <span style="color:var(--text-muted);font-size:0.78rem;margin-left:0.5rem;">since {{.Link.VerifiedAt.Format "Jan 02, 2006"}}</span>
            </div>
            <form method="POST" action="/profile/irc-unlink" class="d-inline"
                  onsubmit="return confirm('Unlink your IRC account?')">
                <input type="hidden" name="_csrf" value="{{.CSRF}}">
                <button type="submit" class="btn btn-outline-danger btn-sm py-0 px-2 fs-2xs">Unlink</button>
            </form>
        </div>
        {{else}}
        <div style="font-size:0.88rem;color:var(--text-muted);margin-bottom:0.75rem;">
            Link your IRC nick to share chat across Discord, IRC, and the site widget, and to receive site whispers as IRC PMs.
        </div>
        <div class="d-flex align-items-center gap-2 mb-2">
            <span class="fs-xs text-muted">Verification token:</span>
            <code style="background:var(--bg-elevated);padding:0.3rem 0.6rem;border-radius:4px;font-size:0.9rem;user-select:all;">{{.Token}}</code>
        </div>
        {{if and .Server .BotNick}}
        <div class="fs-2xs text-muted">
            Connect to <code>{{.Server}}</code>{{if .Channel}} (channel: <code>{{.Channel}}</code>){{end}}, then PM the bot:<br>
            <code style="margin-top:0.3rem;display:inline-block;">/msg {{.BotNick}} link {{.Token}}</code>
        </div>
        {{else}}
        <div class="fs-2xs text-muted">
            The IRC bot isn't configured yet. Once it is, you'll see the connect details + bot nick here.
        </div>
        {{end}}
        {{end}}
    </div>
</div>`))

// renderCard is the SlotUserWidget Render.
//
// Self-only: the card carries an unlink button and a verification token that
// anyone could PM the bot with to claim the account. loon's Public/MinRole
// model cannot express "only the subject", so the view checks — which is why
// the host publishes the subject (core.SetViewSubject).
func (p *Plugin) renderCard(c *gin.Context) (template.HTML, error) {
	subject, ok := core.ViewSubject(c)
	if !ok {
		return "", nil
	}
	viewer := deps.Viewer(c)
	if viewer == nil || int64(viewer.ID) != subject {
		return "", nil
	}
	// …and not on the PUBLIC profile, even for the owner. This card carries a
	// verification token and an unlink button -- controls, not facts -- and
	// the page that says "here is what other members see" is the one place
	// they should never appear. The check above already means nobody else can
	// see them; this is so the owner is not shown their own token on a page
	// they open to check what is public.
	if core.IsPublicProfile(c) {
		return "", nil
	}
	ctx := c.Request.Context()
	userID := int(subject)

	link, _ := deps.Links.GetIRCLinkByUserID(ctx, userID)

	data := map[string]any{"Link": link}
	if link == nil {
		// Read the user fresh: the token may have been minted after the
		// session's cached row was loaded, and re-minting would invalidate the
		// one they are already looking at in another tab.
		u, err := deps.Users.GetUserByID(ctx, userID)
		if err != nil {
			return "", err
		}
		token := u.VerifyToken
		if token == "" {
			token, err = generateVerifyToken()
			if err != nil {
				return "", err
			}
			if err := deps.Links.SetIRCVerifyToken(ctx, userID, token); err != nil {
				// A token we failed to store is one the bot will reject.
				return "", err
			}
		}
		data["Token"] = token
		// Bot config drives the instructions; absent, the card says the bot
		// isn't set up rather than printing "/msg  link <token>".
		data["Server"], _ = deps.Settings.GetSetting(ctx, "irc_server")
		data["Channel"], _ = deps.Settings.GetSetting(ctx, "irc_channel")
		data["BotNick"], _ = deps.Settings.GetSetting(ctx, "irc_nick")
	}

	// The token, baked in. The comment beside the form claimed a "site-wide
	// submit listener" injected it at submit time so a rendered one would be
	// stale — there is no such listener on this host, and the unlink answered
	// 403 for every member who pressed it.
	data["CSRF"] = pluginapi.CSRFToken(p.core, c)

	var sb strings.Builder
	if err := cardTmpl.Execute(&sb, data); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

// unlink drops the viewer's IRC link and clears the verify token, so a fresh
// link attempt mints a new one rather than reusing a token that was already
// shared in a PM.
func (p *Plugin) unlink(c *gin.Context) {
	viewer := deps.Viewer(c)
	if viewer == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()
	if err := deps.Links.DeleteIRCLink(ctx, viewer.ID); err != nil {
		// The host silenced this: an unlink that says "done" while the link
		// survives is the one outcome a user cannot check for themselves.
		p.errs.Report(ctx, "irc/unlink", err)
		c.Redirect(http.StatusFound, "/profile?error=unlink+failed")
		return
	}
	// Best-effort: the link is already gone, which is what the user asked for.
	// A stale token only costs them a new one on the next link attempt.
	if err := deps.Links.SetIRCVerifyToken(ctx, viewer.ID, ""); err != nil {
		p.errs.Report(ctx, "irc/unlink-clear-token", err)
	}
	c.Redirect(http.StatusFound, "/profile")
}
