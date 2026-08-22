package discord

import (
	"context"
	"log"
	"sync/atomic"

	"github.com/bwmarrin/discordgo"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// channelLookupMisses counts channels neither the State cache nor the REST API
// could resolve. Every one of those is a message stored PRIVATE and therefore
// invisible on the site — so a rising number here is the signal that the bridge
// is quietly swallowing conversations rather than mirroring them.
var channelLookupMisses atomic.Int64

// ChannelLookupMisses reports that counter for the admin surface.
func ChannelLookupMisses() int64 { return channelLookupMisses.Load() }

// resolveChannel finds a channel, falling back from the State cache to REST.
//
// State.Channel is a plain map lookup, and the map is populated by GUILD_CREATE
// and THREAD_CREATE. A thread the bot has not seen created — an older one, or
// one started while the bot was disconnected — is simply absent, and the lookup
// fails.
//
// That mattered because a failed lookup resolves to "private", which is the
// correct default for an unknown channel but the WRONG answer for a thread that
// is perfectly public. The bridge was added to capture help-desk threads; a
// State miss would have stored them and hidden them, with nothing reporting it.
//
// REST is the fallback rather than the primary because it is a network call per
// message. Misses should be rare and self-limiting: discordgo caches nothing
// from REST, but a live thread produces a THREAD_CREATE or arrives in
// GUILD_CREATE, so the steady state is a cache hit.
func resolveChannel(s *discordgo.Session, channelID string) *discordgo.Channel {
	if s == nil || channelID == "" {
		return nil
	}
	if s.State != nil {
		if ch, err := s.State.Channel(channelID); err == nil && ch != nil {
			return ch
		}
	}
	// REST needs a fully constructed session. discordgo.New sets up the rate
	// limiter; a zero-value Session has none, and Session.Channel dereferences
	// it — so calling REST on one panics.
	//
	// This is not only a test concern. resolveChannel runs inside the message
	// handler, and a panic there takes the whole bot down with it. Guarding
	// costs a nil check and turns "the bridge died" into "one message was
	// treated as private and counted".
	if s.Ratelimiter == nil {
		channelLookupMisses.Add(1)
		return nil
	}
	ch, err := s.Channel(channelID)
	if err != nil || ch == nil {
		channelLookupMisses.Add(1)
		return nil
	}
	return ch
}

// Which channels the bridge mirrors, and which of them members may see.
//
// The bridge used to mirror ONE channel, named by discord_chat_channel_id.
// Everything else said in the guild was dropped at the handler — including
// help-desk threads where members were waiting, which made them invisible to the
// site and to anything reading the database.
//
// Mirroring everything raises the question the single id was quietly answering:
// the site's /chat page is PolicyMembers, so every logged-in member sees what
// lands there. Copying a staff-only channel into it would publish moderator
// discussion to the whole membership.
//
// The rule is therefore taken from Discord rather than from configuration: if
// @everyone can read a channel there, members can read it here. That needs no
// allowlist to maintain, follows a channel whose permissions change, and needs
// nothing done when a channel is created — which is the point, because an
// allowlist is exactly the thing that goes stale and then leaks.

// memberCanView reports whether a SITE MEMBER should see a channel.
//
// The rule was "can @everyone read it", which is faithful to Discord and was
// useless here. Run against the real guild it returned ONE public channel out of
// nine — #verify — because this is a verification-gated server: @everyone sees
// only the gate, and a member role grants everything else. #general, #rules and
// #announcements all came back private.
//
// So the mapping is the honest one: a site member corresponds to a VERIFIED
// Discord member, not to @everyone. A channel is visible if @everyone can read
// it, OR if the configured member role can — which is exactly the set a logged-in
// member would see had they joined the Discord.
//
// memberRoleID may be empty (unconfigured), in which case this degrades to the
// @everyone rule. That is the safe direction: fewer channels, never more.
//
// Discord resolves role overwrites additively, so an ALLOW on the member role
// overrides a DENY on @everyone. That is precisely how a gated server is built,
// and reading only the deny is what got this wrong.
func memberCanView(s *discordgo.Session, guildID, memberRoleID, channelID string) bool {
	if s == nil || guildID == "" || channelID == "" {
		// No evidence. Private: a wrong "public" reaches 3,300 members and
		// cannot be recalled, a wrong "private" is a message that does not show.
		return false
	}
	ch := resolveChannel(s, channelID)
	if ch == nil {
		return false
	}
	// Threads carry no overwrites; resolve against the parent.
	if ch.ParentID != "" && isThread(ch) {
		if parent := resolveChannel(s, ch.ParentID); parent != nil {
			ch = parent
		} else {
			return false
		}
	}
	if v, decided := roleCanView(ch, memberRoleID); decided {
		return v
	}
	if v, decided := roleCanView(ch, guildID); decided {
		return v
	}
	// Nothing on the channel decides it. Fall through to the CATEGORY, where a
	// gated or private section is usually configured with a single overwrite.
	if ch.ParentID != "" {
		if parent := resolveChannel(s, ch.ParentID); parent != nil {
			if v, decided := roleCanView(parent, memberRoleID); decided {
				return v
			}
			if v, decided := roleCanView(parent, guildID); decided {
				return v
			}
		}
	}
	// Nothing denies it anywhere. @everyone holds VIEW_CHANNEL by default.
	return true
}

// roleCanView reads one role's overwrite on a channel.
//
// decided is false when the role has no overwrite there, which is what lets the
// caller fall through to the next role and then to the category — a distinction
// a plain bool cannot carry, and conflating "no opinion" with "denied" is what
// would hide a channel that merely says nothing about this role.
//
// Allow wins over deny WITHIN a single overwrite: an overwrite carrying both is
// how a gated channel re-opens itself, and it is the shape this whole function
// exists to read correctly.
func roleCanView(ch *discordgo.Channel, roleID string) (allowed, decided bool) {
	if ch == nil || roleID == "" {
		return false, false
	}
	for _, ow := range ch.PermissionOverwrites {
		if ow.Type != discordgo.PermissionOverwriteTypeRole || ow.ID != roleID {
			continue
		}
		if ow.Allow&discordgo.PermissionViewChannel != 0 {
			return true, true
		}
		if ow.Deny&discordgo.PermissionViewChannel != 0 {
			return false, true
		}
	}
	return false, false
}

// isThread reports whether a channel is one of Discord's thread types.
//
// Listed explicitly rather than inferred from ParentID being set: a normal text
// channel inside a CATEGORY also has a ParentID, and treating those as threads
// would resolve every channel against its category and lose per-channel
// overwrites entirely.
func isThread(ch *discordgo.Channel) bool {
	switch ch.Type {
	case discordgo.ChannelTypeGuildPublicThread,
		discordgo.ChannelTypeGuildPrivateThread,
		discordgo.ChannelTypeGuildNewsThread:
		return true
	}
	return false
}

// channelDisplay returns the channel and thread names for a message.
//
// For a thread it reports the PARENT as the channel and the thread separately,
// so the site can group a conversation under the channel it belongs to instead
// of showing every thread as its own top-level room.
func channelDisplay(s *discordgo.Session, channelID string) (channelID2, channelName, threadID, threadName string) {
	if s == nil || channelID == "" {
		return channelID, "", "", ""
	}
	ch := resolveChannel(s, channelID)
	if ch == nil {
		return channelID, "", "", ""
	}
	if isThread(ch) {
		threadID, threadName = ch.ID, ch.Name
		if parent := resolveChannel(s, ch.ParentID); parent != nil {
			return parent.ID, parent.Name, threadID, threadName
		}
		return ch.ParentID, "", threadID, threadName
	}
	return ch.ID, ch.Name, "", ""
}

// syncChannels tells the host which channels exist.
//
// The sidebar was derived from captured MESSAGES, so a channel appeared only
// after somebody spoke in it. Most channels are quiet at any given moment, and a
// freshly deployed bridge therefore showed an empty list — which is exactly what
// it did show, because the guild had been quiet since the columns landed.
//
// Enumerating instead means a channel appears because it EXISTS. Private ones
// are sent too, with Public false: the host needs to know they exist rather than
// meeting them as unknown on every sync, and its member-facing query filters
// them out.
//
// Threads are deliberately not enumerated. GuildChannels does not return them,
// and fetching every thread of every channel on connect is a lot of API calls
// for rooms that are transient by nature — a thread earns a place in the sidebar
// by carrying a message, which the message-derived list already handles.
func (d *DiscordBotService) syncChannels(ctx context.Context, s *discordgo.Session, guildID string) {
	memberRole := d.settings.GetDiscordMemberRoleID(ctx)
	if d.chat == nil || s == nil || guildID == "" {
		return
	}
	chans, err := s.GuildChannels(guildID)
	if err != nil {
		log.Printf("discord bot: channel sync failed: %v", err)
		return
	}
	out := make([]pluginapi.ChatChannel, 0, len(chans))
	for _, ch := range chans {
		if ch == nil {
			continue
		}
		// Text channels only. Voice, categories and stages carry no messages
		// the bridge could mirror, so listing them would be a sidebar of rooms
		// that can never have content.
		if ch.Type != discordgo.ChannelTypeGuildText && ch.Type != discordgo.ChannelTypeGuildNews {
			continue
		}
		public := memberCanView(s, guildID, memberRole, ch.ID)
		// Webhooks only for channels members can see. Creating one in a staff
		// channel would be a send path into a room the site never shows.
		hook := ""
		if public {
			hook = d.webhookFor(s, ch)
		}
		out = append(out, pluginapi.ChatChannel{
			ID:         ch.ID,
			Name:       ch.Name,
			Position:   ch.Position,
			Public:     public,
			WebhookURL: hook,
		})
	}
	if err := d.chat.SyncChannels(ctx, out); err != nil {
		log.Printf("discord bot: channel sync store failed: %v", err)
		return
	}
	var public int
	for _, ch := range out {
		if ch.Public {
			public++
		}
	}
	log.Printf("discord bot: synced %d text channel(s), %d public", len(out), public)
}

// webhookFor returns a send URL for a channel, creating one if needed.
//
// Sending used to go through a single configured webhook, which is bound to one
// channel — so a member's message landed there no matter which channel they were
// reading, and every new channel would have needed an operator to make another
// one by hand. That is the hardcoding this change set exists to remove.
//
// Reuse before create, keyed on the bot's own application id: a guild often has
// webhooks from other integrations, and adding one per sync would litter the
// channel with duplicates named after us.
//
// A webhook rather than ChannelMessageSend because only a webhook carries a
// per-message username and avatar override — which is what makes a site member's
// message appear in Discord as that member rather than as the bot.
func (d *DiscordBotService) webhookFor(s *discordgo.Session, ch *discordgo.Channel) string {
	if s == nil || ch == nil {
		return ""
	}
	hooks, err := s.ChannelWebhooks(ch.ID)
	if err != nil {
		// Usually Missing Permissions (MANAGE_WEBHOOKS). Not fatal and not
		// worth failing the sync: the host keeps any URL it already had, and
		// sending falls back to the single configured webhook — which is the
		// previous behaviour, so this degrades rather than breaks.
		//
		// But it is LOGGED, because the fix is a permission only the operator
		// can grant. Deployed silent, this returned "" for all six public
		// channels and the log said nothing at all; the sync line reported
		// success and the feature simply did not appear.
		log.Printf("discord bot: cannot list webhooks in #%s (%s) — "+
			"grant MANAGE_WEBHOOKS to send there: %v", ch.Name, ch.ID, err)
		return ""
	}
	var appID string
	if s.State != nil && s.State.User != nil {
		appID = s.State.User.ID
	}
	for _, h := range hooks {
		if h == nil || h.Token == "" {
			continue
		}
		// Ours: created by this bot. h.User is the creator.
		if appID != "" && h.User != nil && h.User.ID == appID {
			return webhookURL(h)
		}
	}
	created, err := s.WebhookCreate(ch.ID, d.site(), "")
	if err != nil || created == nil {
		log.Printf("discord bot: cannot create a webhook in #%s (%s) — "+
			"grant MANAGE_WEBHOOKS to send there: %v", ch.Name, ch.ID, err)
		return ""
	}
	// Worth a line: this happens once per channel, ever, and it is the
	// difference between "sending works here" and "sending lands in the one
	// configured channel whatever the member had open".
	log.Printf("discord bot: created a send webhook for #%s", ch.Name)
	return webhookURL(created)
}

// webhookURL builds the execute URL. discordgo returns the id and token but no
// assembled URL, and the token is what makes it usable — so this value is a
// credential from here on.
func webhookURL(h *discordgo.Webhook) string {
	if h == nil || h.ID == "" || h.Token == "" {
		return ""
	}
	return "https://discord.com/api/webhooks/" + h.ID + "/" + h.Token
}
