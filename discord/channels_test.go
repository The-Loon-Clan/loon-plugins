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

// Returns a Session wrapping the State, because the resolver now falls back
// from State to REST and therefore takes the session. Session.Token is left
// empty so any REST attempt fails fast rather than reaching Discord from a
// unit test — which is what we want: these tests are about the State path.
func newState(t *testing.T, chans ...*discordgo.Channel) *discordgo.Session {
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
	return &discordgo.Session{State: st}
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
	if !memberCanView(st, guildID, "", "c1") {
		t.Error("a channel with no overwrites was judged private — @everyone has " +
			"VIEW_CHANNEL by default, so this hides the entire guild")
	}
}

func TestStaffChannelIsHidden(t *testing.T) {
	st := newState(t, &discordgo.Channel{
		ID: "c2", GuildID: guildID, Type: discordgo.ChannelTypeGuildText,
		PermissionOverwrites: []*discordgo.PermissionOverwrite{denyView(guildID)},
	})
	if memberCanView(st, guildID, "", "c2") {
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
	if memberCanView(st, guildID, "", "c3") {
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
	if !memberCanView(st, guildID, "", "c4") {
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
	if memberCanView(st, guildID, "", "t1") {
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
	if !memberCanView(st, guildID, "", "t2") {
		t.Error("a thread in a public channel was hidden — help-desk threads are " +
			"the reason for this whole change")
	}
}

// Absence of evidence resolves to private, in every form it takes.
func TestUnknownsResolveToPrivate(t *testing.T) {
	st := newState(t, &discordgo.Channel{ID: "c5", GuildID: guildID, Type: discordgo.ChannelTypeGuildText})
	cases := map[string]bool{
		"nil session":     memberCanView(nil, guildID, "", "c5"),
		"unknown channel": memberCanView(st, guildID, "", "nope"),
		"empty guild":     memberCanView(st, "", "", "c5"),
		"empty channel":   memberCanView(st, guildID, "", ""),
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
	if memberCanView(st, guildID, "", "c6") {
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

// The case that made the original rule useless in practice.
//
// Run against the real guild, "can @everyone read it" returned ONE public
// channel out of nine — #verify — because this is a verification-gated server:
// @everyone sees only the gate, and a member role grants the rest. #general and
// #rules both came back private, which would have shipped a sidebar with a
// single entry.
//
// A site member IS a verified Discord member, so the member role's allow is what
// decides. Discord resolves role overwrites additively, and an ALLOW on a higher
// role overriding a DENY on @everyone is exactly how such a server is built.
func TestMemberRoleAllowOverridesEveryoneDeny(t *testing.T) {
	const memberRole = "222"
	st := newState(t, &discordgo.Channel{
		ID: "gated", GuildID: guildID, Type: discordgo.ChannelTypeGuildText,
		PermissionOverwrites: []*discordgo.PermissionOverwrite{
			denyView(guildID),     // @everyone cannot see it
			allowView(memberRole), // verified members can
		},
	})
	if !memberCanView(st, guildID, memberRole, "gated") {
		t.Error("a channel gated behind the member role was judged private — " +
			"this is #general on a verification-gated server, and hiding it " +
			"leaves the sidebar with only #verify")
	}
	// And without the member role configured, it degrades to the @everyone rule:
	// fewer channels, never more.
	if memberCanView(st, guildID, "", "gated") {
		t.Error("with no member role configured this must fall back to @everyone, " +
			"which denies the channel")
	}
}

// Staff channels deny BOTH roles and must stay hidden.
func TestStaffChannelHiddenFromMembersToo(t *testing.T) {
	const memberRole = "222"
	st := newState(t, &discordgo.Channel{
		ID: "staff", GuildID: guildID, Type: discordgo.ChannelTypeGuildText,
		PermissionOverwrites: []*discordgo.PermissionOverwrite{
			denyView(guildID), denyView(memberRole),
		},
	})
	if memberCanView(st, guildID, memberRole, "staff") {
		t.Error("a channel denying the member role was judged visible — that is " +
			"moderator discussion published to every logged-in member")
	}
}

// A channel with no overwrite for the member role must not be decided by it:
// the caller has to fall through to @everyone and then to the category.
func TestNoMemberOverwriteFallsThrough(t *testing.T) {
	const memberRole = "222"
	st := newState(t,
		&discordgo.Channel{
			ID: "cat", GuildID: guildID, Type: discordgo.ChannelTypeGuildCategory,
			PermissionOverwrites: []*discordgo.PermissionOverwrite{denyView(guildID)},
		},
		// Says nothing about either role; the category decides.
		&discordgo.Channel{ID: "inner", GuildID: guildID, ParentID: "cat",
			Type: discordgo.ChannelTypeGuildText},
	)
	if memberCanView(st, guildID, memberRole, "inner") {
		t.Error("a channel silent on both roles inside a private category was " +
			"judged visible — the category's deny was not consulted")
	}
}
