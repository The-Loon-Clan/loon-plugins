package discord

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

// The visibility rule, which is the part of the all-channels bridge that must
// not be wrong.
//
// /chat on the site is PolicyMembers, so a channel wrongly judged public is
// published to every logged-in member and cannot be recalled. A channel wrongly
// judged private is a message that does not appear. The tests below are written
// around that asymmetry: every ambiguous case must resolve to private.

const guildID = "111" // @everyone's role id IS the guild id, in Discord's model

func newState(t *testing.T, chans ...*discordgo.Channel) *discordgo.State {
	t.Helper()
	st := discordgo.NewState()
	// The guild must exist before its channels: State keeps channels under
	// their guild, and ChannelAdd on an unknown guild returns "state cache not
	// found" rather than creating one.
	if err := st.GuildAdd(&discordgo.Guild{ID: guildID}); err != nil {
		t.Fatalf("GuildAdd: %v", err)
	}
	for _, ch := range chans {
		if err := st.ChannelAdd(ch); err != nil {
			t.Fatalf("ChannelAdd(%s): %v", ch.ID, err)
		}
	}
	return st
}

func denyView(roleID string) *discordgo.PermissionOverwrite {
	return &discordgo.PermissionOverwrite{
		ID: roleID, Type: discordgo.PermissionOverwriteTypeRole,
		Deny: discordgo.PermissionViewChannel,
	}
}

func allowView(roleID string) *discordgo.PermissionOverwrite {
	return &discordgo.PermissionOverwrite{
		ID: roleID, Type: discordgo.PermissionOverwriteTypeRole,
		Allow: discordgo.PermissionViewChannel,
	}
}

func TestPublicChannelIsVisible(t *testing.T) {
	st := newState(t, &discordgo.Channel{ID: "c1", GuildID: guildID, Type: discordgo.ChannelTypeGuildText})
	if !everyoneCanView(st, guildID, "c1") {
		t.Error("a channel with no overwrites was judged private — @everyone has " +
			"VIEW_CHANNEL by default, so this hides the entire guild")
	}
}

func TestStaffChannelIsHidden(t *testing.T) {
	st := newState(t, &discordgo.Channel{
		ID: "c2", GuildID: guildID, Type: discordgo.ChannelTypeGuildText,
		PermissionOverwrites: []*discordgo.PermissionOverwrite{denyView(guildID)},
	})
	if everyoneCanView(st, guildID, "c2") {
		t.Error("a channel denying @everyone was judged PUBLIC — this is the " +
			"case that publishes moderator discussion to 3,300 members")
	}
}

// The common shape of a private SECTION: one deny on the category, nothing on
// the channels inside it. Reading only the channel calls every one public.
func TestChannelInheritsAPrivateCategory(t *testing.T) {
	st := newState(t,
		&discordgo.Channel{
			ID: "cat", GuildID: guildID, Type: discordgo.ChannelTypeGuildCategory,
			PermissionOverwrites: []*discordgo.PermissionOverwrite{denyView(guildID)},
		},
		&discordgo.Channel{ID: "c3", GuildID: guildID, ParentID: "cat", Type: discordgo.ChannelTypeGuildText},
	)
	if everyoneCanView(st, guildID, "c3") {
		t.Error("a channel inside a private category was judged public — this is " +
			"how a whole staff section leaks at once")
	}
}

// A channel can re-open itself inside a private category with an explicit allow.
func TestChannelOverridesAPrivateCategory(t *testing.T) {
	st := newState(t,
		&discordgo.Channel{
			ID: "cat", GuildID: guildID, Type: discordgo.ChannelTypeGuildCategory,
			PermissionOverwrites: []*discordgo.PermissionOverwrite{denyView(guildID)},
		},
		&discordgo.Channel{
			ID: "c4", GuildID: guildID, ParentID: "cat", Type: discordgo.ChannelTypeGuildText,
			PermissionOverwrites: []*discordgo.PermissionOverwrite{allowView(guildID)},
		},
	)
	if !everyoneCanView(st, guildID, "c4") {
		t.Error("an explicit allow on the channel did not override its category")
	}
}

// Threads carry no overwrites and inherit from the parent. Resolving a thread
// against itself would find no denies and call every thread of a private channel
// public — the leak being hardest to notice, since the channel itself is hidden.
func TestThreadInheritsItsParent(t *testing.T) {
	st := newState(t,
		&discordgo.Channel{
			ID: "priv", GuildID: guildID, Type: discordgo.ChannelTypeGuildText,
			PermissionOverwrites: []*discordgo.PermissionOverwrite{denyView(guildID)},
		},
		&discordgo.Channel{ID: "t1", GuildID: guildID, ParentID: "priv",
			Type: discordgo.ChannelTypeGuildPublicThread},
	)
	if everyoneCanView(st, guildID, "t1") {
		t.Error("a thread in a PRIVATE channel was judged public — the parent is " +
			"hidden, so this leaks exactly the conversations nobody would check")
	}
}

func TestThreadInPublicChannelIsVisible(t *testing.T) {
	st := newState(t,
		&discordgo.Channel{ID: "pub", GuildID: guildID, Type: discordgo.ChannelTypeGuildText},
		&discordgo.Channel{ID: "t2", GuildID: guildID, ParentID: "pub",
			Type: discordgo.ChannelTypeGuildPublicThread},
	)
	if !everyoneCanView(st, guildID, "t2") {
		t.Error("a thread in a public channel was hidden — help-desk threads are " +
			"the reason for this whole change")
	}
}

// Absence of evidence resolves to private, in every form it takes.
func TestUnknownsResolveToPrivate(t *testing.T) {
	st := newState(t, &discordgo.Channel{ID: "c5", GuildID: guildID, Type: discordgo.ChannelTypeGuildText})
	cases := map[string]bool{
		"nil state":       everyoneCanView(nil, guildID, "c5"),
		"unknown channel": everyoneCanView(st, guildID, "nope"),
		"empty guild":     everyoneCanView(st, "", "c5"),
		"empty channel":   everyoneCanView(st, guildID, ""),
	}
	for name, got := range cases {
		if got {
			t.Errorf("%s was judged PUBLIC — an unknown must never publish", name)
		}
	}
}

// A category ParentID is not a thread ParentID. Treating every channel with a
// parent as a thread would resolve normal channels against their category and
// discard their own overwrites.
func TestCategoryParentIsNotAThread(t *testing.T) {
	st := newState(t,
		&discordgo.Channel{ID: "cat2", GuildID: guildID, Type: discordgo.ChannelTypeGuildCategory},
		&discordgo.Channel{
			ID: "c6", GuildID: guildID, ParentID: "cat2", Type: discordgo.ChannelTypeGuildText,
			PermissionOverwrites: []*discordgo.PermissionOverwrite{denyView(guildID)},
		},
	)
	if everyoneCanView(st, guildID, "c6") {
		t.Error("a channel's own deny was ignored in favour of its category — " +
			"per-channel overwrites are being discarded")
	}
}

// Display groups a thread under its parent, so a conversation appears in the
// channel it belongs to rather than as its own top-level room.
func TestChannelDisplayGroupsThreadsUnderTheParent(t *testing.T) {
	st := newState(t,
		&discordgo.Channel{ID: "pub", GuildID: guildID, Name: "help", Type: discordgo.ChannelTypeGuildText},
		&discordgo.Channel{ID: "t3", GuildID: guildID, ParentID: "pub", Name: "cannot log in",
			Type: discordgo.ChannelTypeGuildPublicThread},
	)
	chID, chName, thID, thName := channelDisplay(st, "t3")
	if chID != "pub" || chName != "help" {
		t.Errorf("thread reported channel %s/%s, want pub/help", chID, chName)
	}
	if thID != "t3" || thName != "cannot log in" {
		t.Errorf("thread reported %s/%s, want t3/'cannot log in'", thID, thName)
	}
}

func TestChannelDisplayLeavesPlainChannelsAlone(t *testing.T) {
	st := newState(t, &discordgo.Channel{ID: "pub", GuildID: guildID, Name: "general",
		Type: discordgo.ChannelTypeGuildText})
	chID, chName, thID, thName := channelDisplay(st, "pub")
	if chID != "pub" || chName != "general" || thID != "" || thName != "" {
		t.Errorf("plain channel reported %s/%s thread=%s/%s", chID, chName, thID, thName)
	}
}
