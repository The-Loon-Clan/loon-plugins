package discord

import (
	"github.com/bwmarrin/discordgo"
)

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
func everyoneCanView(state *discordgo.State, guildID, channelID string) bool {
	if state == nil || guildID == "" || channelID == "" {
		// No state means no evidence. Treat as private: a wrong "public" is
		// published to 3,300 members and cannot be recalled, a wrong "private"
		// is a message that does not appear.
		return false
	}
	ch, err := state.Channel(channelID)
	if err != nil || ch == nil {
		return false
	}
	// A thread has no overwrites of its own; resolve against the parent.
	if ch.ParentID != "" && isThread(ch) {
		if parent, perr := state.Channel(ch.ParentID); perr == nil && parent != nil {
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
		if parent, perr := state.Channel(ch.ParentID); perr == nil && parent != nil {
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
func channelDisplay(state *discordgo.State, channelID string) (channelID2, channelName, threadID, threadName string) {
	if state == nil || channelID == "" {
		return channelID, "", "", ""
	}
	ch, err := state.Channel(channelID)
	if err != nil || ch == nil {
		return channelID, "", "", ""
	}
	if isThread(ch) {
		threadID, threadName = ch.ID, ch.Name
		if parent, perr := state.Channel(ch.ParentID); perr == nil && parent != nil {
			return parent.ID, parent.Name, threadID, threadName
		}
		return ch.ParentID, "", threadID, threadName
	}
	return ch.ID, ch.Name, "", ""
}
