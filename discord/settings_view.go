package discord

import (
	"context"
	"html/template"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// The Discord section of /admin/settings, owned by the Discord plugin.
//
// It was a hardcoded card in the host's admin_settings.html, read by the host's
// Settings handler and written by a `_section == "discord"` branch in the
// host's UpdateSettings — so every Discord field was declared in three host
// files. It is a loon SlotAdminSettings view now: the host supplies the page
// and the card chrome, the plugin supplies the form and handles its own save.
//
// The host still owns Registration, SMTP and Agent Defaults on the same page.
// Those are site configuration, not a plugin's — "no plugin code on the site"
// is the goal, not an empty host.

var settingsTmpl = template.Must(template.New("discord-settings").Parse(`
                <div style="font-size:0.8rem;color:var(--text-muted);margin-bottom:0.75rem;">
                    Configure the Discord bot for account linking, rank role sync, and release notifications.
                </div>
                <form method="POST" action="/admin/settings/discord/save">
                    <div class="row g-3 mb-3">
                        <div class="col-sm-6">
                            <label class="form-label" style="font-size:0.78rem;">Bot Token</label>
                            <input type="password" name="discord_bot_token" class="form-control form-control-sm" value="" autocomplete="off" placeholder="{{if .BotTokenSet}}configured — leave blank to keep{{else}}Bot token from Discord Developer Portal{{end}}">
                        </div>
                        <div class="col-sm-3">
                            <label class="form-label" style="font-size:0.78rem;">Guild (Server) ID</label>
                            <input type="text" name="discord_guild_id" class="form-control form-control-sm" value="{{.GuildID}}" placeholder="Server ID">
                        </div>
                        <div class="col-sm-3">
                            <label class="form-label" style="font-size:0.78rem;">Releases Channel ID</label>
                            <input type="text" name="discord_releases_channel_id" class="form-control form-control-sm" value="{{.ReleasesChannelID}}" placeholder="Channel ID">
                        </div>
                    </div>
                    <div class="row g-3 mb-3">
                        <div class="col-sm-6">
                            <label class="form-label" style="font-size:0.78rem;">Chat Channel ID</label>
                            <input type="text" name="discord_chat_channel_id" class="form-control form-control-sm" value="{{.ChatChannelID}}" placeholder="Channel ID for the site /chat bridge">
                            <div style="font-size:0.7rem;color:var(--text-muted);margin-top:0.2rem;">Messages posted in this channel show up live on the site Chat page. Bot needs the "Message Content" privileged intent enabled.</div>
                        </div>
                        <div class="col-sm-6">
                            <label class="form-label" style="font-size:0.78rem;">Chat Webhook URL <span style="color:var(--text-muted);">(Phase 2)</span></label>
                            <input type="password" name="discord_chat_webhook_url" class="form-control form-control-sm" value="" autocomplete="off" placeholder="{{if .WebhookSet}}configured — leave blank to keep{{else}}https://discord.com/api/webhooks/...{{end}}">
                            <div style="font-size:0.7rem;color:var(--text-muted);margin-top:0.2rem;">Used by Phase 2 to post site users' messages back to Discord with their site username and avatar.</div>
                        </div>
                    </div>
                    <div class="row g-3 mb-3">
                        <div class="col-sm-6">
                            <label class="form-label" style="font-size:0.78rem;">Invite URL</label>
                            <input type="text" name="discord_invite_url" class="form-control form-control-sm" value="{{.InviteURL}}" placeholder="https://discord.gg/...">
                            <div style="font-size:0.7rem;color:var(--text-muted);margin-top:0.2rem;">Public invite link shown on the site Chat page so users can hop into the Discord server. Leave blank to hide the link.</div>
                        </div>
                    </div>
                    <div class="row g-3 mb-3">
                        <div class="col-sm-6">
                            <label class="form-label" style="font-size:0.78rem;">Verify Channel ID</label>
                            <input type="text" name="discord_verify_channel_id" class="form-control form-control-sm" value="{{.VerifyChannelID}}" placeholder="Bot-only channel for the Verify button">
                            <div style="font-size:0.7rem;color:var(--text-muted);margin-top:0.2rem;">Channel where /setup-verify posts the Verify button. Use a bot-only channel: each /setup-verify run wipes prior bot messages so updates don't leave duplicates. Restrict member send/manage permissions in Discord so only the bot can post here.</div>
                        </div>
                        <div class="col-sm-6">
                            <label class="form-label" style="font-size:0.78rem;">Ops Channel ID</label>
                            <input type="text" name="discord_ops_channel_id" class="form-control form-control-sm" value="{{.OpsChannelID}}" placeholder="Staff channel for operational digests">
                            <div style="font-size:0.7rem;color:var(--text-muted);margin-top:0.2rem;">Daily job digests (season curation, review alerts) land here. Leave empty to disable Discord delivery; the admin pages carry the same information.</div>
                        </div>
                    </div>
                    <div style="font-size:0.78rem;color:var(--text-muted);margin-bottom:0.5rem;">Role IDs (create roles in Discord, right-click &rarr; Copy Role ID)</div>
                    <div class="row g-3 mb-3">
                        <div class="col-sm-6">
                            <label class="form-label" style="font-size:0.78rem;">Member Role <span style="color:var(--bs-success);">(baseline)</span></label>
                            <input type="text" name="discord_role_member" class="form-control form-control-sm" value="{{.RoleMember}}" placeholder="Role ID — granted to every linked user">
                            <div style="font-size:0.7rem;color:var(--text-muted);margin-top:0.2rem;">Granted automatically when a user links their account via the Verify button. Independent of every other role — added on top, never removed by the role sync. Use this as the "you've verified, you can see the server" gate: deny @everyone read access to your channels, allow Member.</div>
                        </div>
                    </div>
                    <div style="font-size:0.78rem;color:var(--text-muted);margin-bottom:0.3rem;margin-top:0.6rem;">Account roles &mdash; site authority. Synced independently of paid ranks: an admin who's also a Kirisame keeps both roles.</div>
                    <div class="row g-3 mb-3">
                        <div class="col-sm-4">
                            <label class="form-label" style="font-size:0.78rem;">Contributor Role</label>
                            <input type="text" name="discord_role_contributor" class="form-control form-control-sm" value="{{.RoleContributor}}" placeholder="Role ID">
                        </div>
                        <div class="col-sm-4">
                            <label class="form-label" style="font-size:0.78rem;">Moderator Role</label>
                            <input type="text" name="discord_role_moderator" class="form-control form-control-sm" value="{{.RoleModerator}}" placeholder="Role ID">
                        </div>
                        <div class="col-sm-4">
                            <label class="form-label" style="font-size:0.78rem;">Admin Role</label>
                            <input type="text" name="discord_role_admin" class="form-control form-control-sm" value="{{.RoleAdmin}}" placeholder="Role ID">
                        </div>
                    </div>
                    <div style="font-size:0.78rem;color:var(--text-muted);margin-bottom:0.3rem;margin-top:0.6rem;">Paid ranks &mdash; rotates with the active subscription.</div>
                    {{/* One field per paid rank, from the group catalog — not a
                         fixed four. A rank added in the groups admin appears here
                         automatically; before, it had no field and could never
                         be synced to Discord. */}}
                    <div class="row g-3 mb-3">
                        {{range .RankRoles}}
                        <div class="col-sm-3">
                            <label class="form-label" style="font-size:0.78rem;">{{.Label}} Role</label>
                            <input type="text" name="discord_role_{{.Key}}" class="form-control form-control-sm" value="{{.Value}}" placeholder="Role ID">
                        </div>
                        {{else}}
                        <div class="col-12" style="font-size:0.78rem;color:var(--text-muted);">
                            No paid ranks configured — add one in <a href="/admin/p/groups">Groups &amp; Ranks</a> and its Discord role field appears here.
                        </div>
                        {{end}}
                    </div>
                    <div class="form-check mb-3">
                        <input class="form-check-input" type="checkbox" name="discord_enabled" value="1" id="discord-enabled"
                               {{if .Enabled}}checked{{end}}>
                        <label class="form-check-label" for="discord-enabled" style="font-size:0.85rem;">Enable Discord Bot</label>
                    </div>
                    <button type="submit" class="btn btn-primary btn-sm">Save Discord Settings</button>
                </form>
`))

// rankRole is one rank's Discord role-id field.
//
// Rank names come from the group catalog, never a literal: the four-rank
// hardcode this replaced meant a rank created later could never sync (see
// bot.go's getRankRoleMap).
type rankRole struct {
	Key   string
	Label string
	Value string
}

// renderSettings is the SlotAdminSettings Render. The host gates the page on
// admin, so anyone reaching here is already authorised.
//
// The two masked fields cross as booleans, not values: the template only
// needs "is it configured" for the placeholder text, and the host version
// passed the secrets under keys the template never read — so the
// "configured — leave blank to keep" hint could never display.
func (p *Plugin) renderSettings(c *gin.Context) (template.HTML, error) {
	ctx := c.Request.Context()
	s := deps.Settings

	var sb strings.Builder
	if err := settingsTmpl.Execute(&sb, map[string]any{
		"BotTokenSet":       s.GetDiscordBotToken(ctx) != "",
		"GuildID":           s.GetDiscordGuildID(ctx),
		"ReleasesChannelID": s.GetDiscordReleasesChannelID(ctx),
		"ChatChannelID":     s.GetDiscordChatChannelID(ctx),
		"VerifyChannelID":   s.GetDiscordVerifyChannelID(ctx),
		"OpsChannelID":      s.GetDiscordOpsChannelID(ctx),
		"WebhookSet":        s.GetDiscordChatWebhookURL(ctx) != "",
		"InviteURL":         s.GetDiscordInviteURL(ctx),
		"RoleMember":        s.GetDiscordMemberRoleID(ctx),
		"RoleContributor":   s.GetDiscordRoleID(ctx, "contributor"),
		"RoleModerator":     s.GetDiscordRoleID(ctx, "moderator"),
		"RoleAdmin":         s.GetDiscordRoleID(ctx, "admin"),
		"RankRoles":         p.rankRoles(ctx),
		"Enabled":           s.GetDiscordEnabled(ctx),
	}); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

// rankRoles lists the groups with their configured Discord role ids, keyed by
// SLUG — the same key getRankRoleMap looks up, so the form and the bot cannot
// disagree. Slugs survive a rename where lowercased names did not; host
// migration 280 re-keys the settings rows.
//
// Every VISIBLE group is listed, where this used to filter to paid tiers by
// monthly cost. The badge contract carries no price, and the filter is not
// worth a contract field: an unconfigured group maps to "", which reconcileAxis
// already reads as "no role on this axis", so a free tier simply gains a field
// that does nothing until an admin fills it. Hidden groups stay out, because
// Catalog excludes them by contract — a group with no badge must not be
// projectable into a Discord role.
func (p *Plugin) rankRoles(ctx context.Context) []rankRole {
	if p.display == nil {
		return nil // no ranks plugin: only the account-role fields render
	}
	groups, err := p.display.Catalog(ctx)
	if err != nil {
		p.errs.Report(ctx, "discord/settings-ranks", err)
		return nil
	}
	out := make([]rankRole, 0, len(groups))
	for _, g := range groups {
		out = append(out, rankRole{
			Key:   g.Slug,
			Label: g.Name,
			Value: deps.Settings.GetDiscordRoleID(ctx, g.Slug),
		})
	}
	return out
}

// saveSettings handles POST /admin/settings/discord/save.
//
// Returns ("", nil) after redirecting: there is nothing to preserve on success,
// and the settings page re-renders from stored state.
func (p *Plugin) saveSettings(c *gin.Context) (template.HTML, error) {
	ctx := c.Request.Context()
	s := deps.Settings

	// The token is masked in the form, so an empty field means "unchanged",
	// not "clear it" — otherwise saving any other field would blank the bot's
	// credentials (audit R40). Same for the webhook URL.
	if v := strings.TrimSpace(c.PostForm("discord_bot_token")); v != "" {
		if err := s.SetDiscordBotToken(ctx, v); err != nil {
			return "", err
		}
	}
	if v := strings.TrimSpace(c.PostForm("discord_chat_webhook_url")); v != "" {
		if err := s.SetDiscordChatWebhookURL(ctx, v); err != nil {
			return "", err
		}
	}

	for _, f := range []struct {
		set func(context.Context, string) error
		val string
	}{
		{s.SetDiscordGuildID, c.PostForm("discord_guild_id")},
		{s.SetDiscordReleasesChannelID, c.PostForm("discord_releases_channel_id")},
		{s.SetDiscordChatChannelID, c.PostForm("discord_chat_channel_id")},
		{s.SetDiscordVerifyChannelID, c.PostForm("discord_verify_channel_id")},
		{s.SetDiscordOpsChannelID, c.PostForm("discord_ops_channel_id")},
		{s.SetDiscordInviteURL, c.PostForm("discord_invite_url")},
		{s.SetDiscordMemberRoleID, c.PostForm("discord_role_member")},
	} {
		if err := f.set(ctx, strings.TrimSpace(f.val)); err != nil {
			return "", err
		}
	}

	for _, r := range []string{"contributor", "moderator", "admin"} {
		if err := s.SetDiscordRoleID(ctx, r, strings.TrimSpace(c.PostForm("discord_role_"+r))); err != nil {
			return "", err
		}
	}
	// Ranks are data — read the list back rather than naming them, so a rank
	// added after this code was written is still settable.
	for _, r := range p.rankRoles(ctx) {
		if err := s.SetDiscordRoleID(ctx, r.Key, strings.TrimSpace(c.PostForm("discord_role_"+r.Key))); err != nil {
			return "", err
		}
	}
	if err := s.SetDiscordEnabled(ctx, c.PostForm("discord_enabled") == "1"); err != nil {
		return "", err
	}

	c.Redirect(http.StatusFound, "/admin/settings")
	return "", nil
}
