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

// everyoneCanView reports whether the @everyone role can read a channel.
//
// Discord models the default role as a role whose ID equals the GUILD id, so
// the check is: does this channel deny VIEW_CHANNEL to that role?
//
// Deny beats allow in Discord's resolution for a single role, and an explicit
// allow on the same overwrite is how a channel re-opens itself after a
// category-level deny — so both halves are read, not just the deny.
//
// Threads inherit their parent's permissions and carry no overwrites of their
// own, so a thread is resolved against its parent. Getting that wrong would
// leak a private channel's threads while correctly hiding the channel itself.
func everyoneCanView(s *discordgo.Session, guildID, channelID string) bool {
	if s == nil || guildID == "" || channelID == "" {
		// No state means no evidence. Treat as private: a wrong "public" is
		// published to 3,300 members and cannot be recalled, a wrong "private"
		// is a message that does not appear.
		return false
	}
	ch := resolveChannel(s, channelID)
	if ch == nil {
		return false
	}
	// A thread has no overwrites of its own; resolve against the parent.
	if ch.ParentID != "" && isThread(ch) {
		if parent := resolveChannel(s, ch.ParentID); parent != nil {
			ch = parent
		} else {
			return false
		}
	}
	for _, ow := range ch.PermissionOverwrites {
		if ow.Type != discordgo.PermissionOverwriteTypeRole || ow.ID != guildID {
			continue
		}
		if ow.Deny&discordgo.PermissionViewChannel != 0 {
			return false
		}
		if ow.Allow&discordgo.PermissionViewChannel != 0 {
			return true
		}
	}
	// No overwrite for @everyone on this channel. Fall through to the category,
	// which is where a private SECTION is usually configured — one deny on the
	// category, nothing on the channels inside it. Reading only the channel
	// would call every one of them public.
	if ch.ParentID != "" {
		if parent := resolveChannel(s, ch.ParentID); parent != nil {
			for _, ow := range parent.PermissionOverwrites {
				if ow.Type != discordgo.PermissionOverwriteTypeRole || ow.ID != guildID {
					continue
				}
				if ow.Deny&discordgo.PermissionViewChannel != 0 {
					return false
				}
			}
		}
	}
	// Nothing denies it. The guild-level @everyone role grants VIEW_CHANNEL by
	// default, and a guild that revoked it wholesale would have no public
	// channels at all — a configuration this bridge has no business guessing
	// around.
	return true
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
		out = append(out, pluginapi.ChatChannel{
			ID:       ch.ID,
			Name:     ch.Name,
			Position: ch.Position,
			Public:   everyoneCanView(s, guildID, ch.ID),
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
