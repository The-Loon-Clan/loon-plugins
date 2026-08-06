// Package chat is the site shoutbox — the Discord/IRC chat bridge page with
// SSE live updates and webhook send-back.
//
// The chat HUB stays host-owned. It is a Redis pub/sub fan-out shared by three
// processes: the web tier's SSE subscribers, and the worker's Discord and IRC
// bots, which each hold their own instance and publish into the same channel.
// A plugin cannot own something two sibling plugins also need, so this owns
// the /chat SURFACE and the host owns the pipe.
package chat

import (
	"context"

	"github.com/gin-gonic/gin"
)

// Viewer is who is asking. AvatarPath feeds the Discord webhook's avatar
// override, so a site member's message appears in Discord as theirs.
type Viewer struct {
	ID         int
	Username   string
	AvatarPath string
}

// Deps carries what the plugin cannot do for itself.
//
// The message ENVELOPE never crosses this seam, and that is the whole design.
// The obvious move was to promote the host's ChatMessage into a shared
// contract package, because Subscribe hands back a typed channel and channel
// element types cannot be adapted. But the plugin only ever forwards a
// message: history goes straight to c.JSON, and a live message goes straight
// into an SSE frame. It reads no field, ever. So the host encodes and this
// side moves bytes — which keeps the contract change, and the churn it would
// have forced through the Discord and IRC bots, from happening at all.
type Deps struct {
	// Viewer identifies the requester; nil when anonymous.
	Viewer func(c *gin.Context) *Viewer

	// Recent is the backlog the page loads before the stream takes over.
	// Typed `any` because it is handed to the JSON encoder untouched.
	Recent func(ctx context.Context, n int) (any, error)

	// Subscribe opens a live feed of already-encoded messages and returns a
	// cancel. The cancel MUST be called or the host leaks a subscriber; the
	// SSE handler defers it. Calling it twice is safe.
	Subscribe func(username string) (msgs <-chan []byte, cancel func())

	OnlineCount func() int
	OnlineUsers func() []string

	// WebhookURL is the Discord channel webhook messages are posted to.
	// Empty means sending is not configured and the endpoint says so.
	WebhookURL func(ctx context.Context) string
	// InviteURL is the "join our Discord" link on the page. May be empty.
	InviteURL func(ctx context.Context) string
	// BaseURL is the site's public origin, for absolute avatar URLs — a
	// relative path means nothing to Discord's CDN.
	BaseURL string

	JSONOK    func(c *gin.Context, extras gin.H)
	JSONError func(c *gin.Context, code int, msg string)
}

var deps *Deps

// SetDeps hands the plugin its host adapters. Called once from the
// composition root before core.Boot.
func SetDeps(d Deps) { deps = &d }

// ok reports whether every dependency was supplied.
func (d *Deps) ok() bool {
	return d != nil &&
		d.Viewer != nil && d.Recent != nil && d.Subscribe != nil &&
		d.OnlineCount != nil && d.OnlineUsers != nil &&
		d.WebhookURL != nil && d.InviteURL != nil &&
		d.JSONOK != nil && d.JSONError != nil
}
