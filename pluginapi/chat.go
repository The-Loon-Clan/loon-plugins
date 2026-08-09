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
}
