package pluginapi

import (
	"context"
	"time"
)

// ChatMessage is one chat line, and its field set + json tags ARE the wire
// format: messages marshal onto a Redis ring ('chat:messages', newest first)
// and a pub/sub channel ('chat:broadcast') that the host's SSE fan-out and
// the discord/irc bridge bots all speak. Changing a tag is a protocol break
// for every process at once — in-flight messages from an old worker must
// still parse on a new web tier mid-deploy.
//
// This is the envelope the chat page plugin deliberately avoided promoting
// (it crosses encoded []byte frames and never reads a field). The bridge
// bots construct every field on publish and read Source/Author/Body on
// subscribe, so for them the type itself is the contract.
type ChatMessage struct {
	ID        string    `json:"id"`         // Discord snowflake, or "irc-<nano>-<nick>"
	Author    string    `json:"author"`     // display name
	AvatarURL string    `json:"avatar_url"` // CDN URL or site-absolute avatar
	Body      string    `json:"body"`       // message text (sanitized client-side)
	At        time.Time `json:"at"`
	Source    string    `json:"source"`               // "discord", "irc", or "site"
	Role      string    `json:"role,omitempty"`       // site role: "admin", "mod", "contributor", "user"
	RankName  string    `json:"rank_name,omitempty"`  // e.g. "Kirisame"
	RankColor string    `json:"rank_color,omitempty"` // hex color for username display

	// ChannelID / ChannelName identify where the message was said. Empty on
	// site-originated messages, which have no upstream channel.
	//
	// Present because the bridge stopped being one channel. It used to mirror a
	// single configured id, so everything else said in the guild — including
	// help-desk threads members were waiting in — was dropped at the handler and
	// invisible to the site entirely.
	ChannelID   string `json:"channel_id,omitempty"`
	ChannelName string `json:"channel_name,omitempty"`
	// ThreadID is set when the message was posted in a thread, with ChannelID
	// naming the thread's PARENT. That pair is what a reply needs: Discord
	// addresses a thread by its own id, while permissions and display grouping
	// belong to the parent.
	ThreadID   string `json:"thread_id,omitempty"`
	ThreadName string `json:"thread_name,omitempty"`
	// Public records that @everyone could read this channel in Discord at the
	// time the message arrived.
	//
	// The site's /chat page is PolicyMembers — every logged-in member — so
	// mirroring a staff-only channel would publish moderator discussion to 3,300
	// people. Rather than a hand-maintained allowlist, the bridge asks Discord:
	// if @everyone can read it there, members can read it here. A channel whose
	// permissions change is followed automatically, and nothing needs
	// configuring when a channel is added.
	Public bool `json:"public"`
}

// ChatHub is the narrow surface of the host-owned chat hub that bridge bots
// consume. The hub itself stays host-owned — it is one Redis-backed bus
// shared by the web tier's SSE subscribers and every bridge bot, and a
// plugin cannot own something two sibling plugins also need. Each bot
// receives its OWN instance via a Deps constructor func; instances cohere
// only through Redis, which is the real cross-process contract.
//
// Subscribe's channel is closed by Unsubscribe; a subscriber that ranges
// over it exits when unsubscribed.
type ChatHub interface {
	Start()
	Publish(ctx context.Context, m ChatMessage) error
	Recent(ctx context.Context, n int) ([]ChatMessage, error)
	Subscribe(username string) chan ChatMessage
	Unsubscribe(ch chan ChatMessage)

	// SyncChannels records the channels the bridge can see, so the site can
	// list them.
	//
	// Needed because the sidebar was derived from captured MESSAGES, which
	// means a channel appears only after somebody speaks in it — a guild's
	// quiet channels were invisible, and a freshly deployed bridge showed an
	// empty list until the first message arrived.
	//
	// The bot runs on the worker and the page renders on web, so this crosses
	// processes through the host rather than through the bot's own memory.
	SyncChannels(ctx context.Context, chans []ChatChannel) error
}

// ChatChannel is one channel the bridge can see.
type ChatChannel struct {
	ID   string
	Name string
	// Position is Discord's own ordering, so the sidebar can match the order a
	// member sees in Discord rather than inventing one.
	Position int
	// Public: @everyone can read it. A private channel is still SYNCED — the
	// site needs to know it exists to avoid re-adding it as unknown — but the
	// member-facing list filters on this, because a staff channel's name is a
	// leak even with its messages withheld.
	Public bool
}
