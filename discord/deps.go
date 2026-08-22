package discord

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// ErrNoInvites is the invite-mint refusal the bot turns into a friendly
// "earn more by contributing" reply. The host adapter translates its invite
// service's own error into this sentinel, so the fragile string-match lives
// next to the service that owns the string — not across a repo boundary.
var ErrNoInvites = errors.New("no invites remaining")

// Link is one verified site-user ↔ Discord-account link.
type Link struct {
	UserID      int
	DiscordID   string
	DiscordName string
	VerifiedAt  time.Time
}

// LinkWithRole carries what the role sync needs for one linked user. UserID
// lets the bot resolve badges for the whole batch through the GroupDisplay
// capability; AccountRole is the site authority axis (user / contributor /
// mod / admin), synced independently of paid ranks.
type LinkWithRole struct {
	DiscordID   string
	UserID      int
	AccountRole string
}

// User is the narrow user read the bot and card need.
type User struct {
	ID          int
	Username    string
	Role        string
	AvatarPath  string
	VerifyToken string // one-shot discord verify token, "" when none minted
}

// Viewer is the authenticated session user on the web leg. Only identity —
// the card is self-only and needs nothing else.
type Viewer struct {
	ID int
}

// LinkStore is the discord_links + verify-token storage. Method names match
// the host repository verbatim so the ported call sites did not move; the
// backing data is split across two host tables (discord_links rows, and the
// verify token as a users column), which is why this stays a host seam
// rather than plugin-owned schema.
type LinkStore interface {
	CreateDiscordLink(ctx context.Context, userID int, discordID, discordName string) error
	GetDiscordLinkByToken(ctx context.Context, token string) (*Link, *User, error)
	GetDiscordLinkByUserID(ctx context.Context, userID int) (*Link, error)
	GetDiscordLinkByDiscordID(ctx context.Context, discordID string) (*Link, error)
	DeleteDiscordLink(ctx context.Context, userID int) error
	SetDiscordVerifyToken(ctx context.Context, userID int, token string) error
	GetDiscordLinksWithRoles(ctx context.Context) ([]LinkWithRole, error)
}

// UserStore reads users. The bot only ever looks up by id.
type UserStore interface {
	GetUserByID(ctx context.Context, id int) (*User, error)
}

// Settings is the typed settings surface, matched method-for-method to the
// host's SettingsService so the concrete service satisfies it structurally —
// zero adapter. That routing is load-bearing, not convenience: the host
// caches discord_invite_url at package level for its page chrome, and only
// the real setter refreshes that cache.
type Settings interface {
	GetDiscordEnabled(ctx context.Context) bool
	SetDiscordEnabled(ctx context.Context, v bool) error
	GetDiscordBotToken(ctx context.Context) string
	SetDiscordBotToken(ctx context.Context, v string) error
	GetDiscordGuildID(ctx context.Context) string
	SetDiscordGuildID(ctx context.Context, v string) error
	GetDiscordReleasesChannelID(ctx context.Context) string
	SetDiscordReleasesChannelID(ctx context.Context, v string) error
	GetDiscordChatChannelID(ctx context.Context) string
	SetDiscordChatChannelID(ctx context.Context, v string) error
	GetDiscordVerifyChannelID(ctx context.Context) string
	SetDiscordVerifyChannelID(ctx context.Context, v string) error
	GetDiscordOpsChannelID(ctx context.Context) string
	SetDiscordOpsChannelID(ctx context.Context, v string) error
	GetDiscordChatWebhookURL(ctx context.Context) string
	SetDiscordChatWebhookURL(ctx context.Context, v string) error
	GetDiscordInviteURL(ctx context.Context) string
	SetDiscordInviteURL(ctx context.Context, v string) error
	GetDiscordMemberRoleID(ctx context.Context) string
	SetDiscordMemberRoleID(ctx context.Context, v string) error
	GetDiscordRoleID(ctx context.Context, name string) string
	SetDiscordRoleID(ctx context.Context, name, roleID string) error
}

// Deps carries the host seams. Set from the host's composition root before
// core.Boot — in the SHARED section, not the worker block: the web leg (the
// /profile card and admin settings form) provisions too, and worker-only
// staging would fail a split-mode --mode=web boot.
type Deps struct {
	Links    LinkStore
	Users    UserStore
	Settings Settings

	// SiteName is what this deployment calls itself.
	//
	// It exists because the bot's embeds and replies said "ameNZB" — the
	// name of the site this plugin was lifted out of — to people who are
	// not on this one. A plugin cannot know the name and must not guess
	// it, and there is exactly one right answer per deployment.
	//
	// Optional: absent, the bot uses the host part of BaseURL.
	SiteName func() string

	// CSRFToken answers the host middleware's per-session token for the
	// admin settings form.
	//
	// A 2026-08-09 audit reported that this form 403'd every save for want of
	// the field. That was WRONG, and the correction is recorded here because
	// the claim was repeated: the ameNZB host's csrf-js partial creates the
	// hidden input at submit time for any form lacking one, and the admin
	// settings page includes it, so the form worked. Rendering the token
	// server-side is still right — it does not depend on the host shipping
	// that partial, or on the member running JavaScript — but it fixed a
	// robustness gap, not an outage.
	CSRFToken func(c *gin.Context) string

	// NewHub constructs this bot's own chat-hub instance. The hub stays
	// host-owned; instances converge on Redis. Called once, on the worker leg.
	NewHub func() pluginapi.ChatHub

	// CreateInvite mints a site invite for a linked user and returns its
	// token. ErrNoInvites when the user's balance is spent. Nil disables the
	// /invite command (the bot answers "unavailable").
	CreateInvite func(ctx context.Context, userID int) (string, error)

	// Viewer resolves the authenticated session user, nil when anonymous.
	Viewer func(c *gin.Context) *Viewer

	BaseURL string
}

var deps *Deps

// SetDeps hands the plugin its host seams.
func SetDeps(d Deps) { deps = &d }

// generateVerifyToken mints the 8-character hex token the /profile card
// shows and the bot's verify modal consumes. Copied from the host rather
// than seamed — four random bytes need no adapter.
func generateVerifyToken() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
