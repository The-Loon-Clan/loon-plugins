package irc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// ErrNoInvites is the invite-mint refusal the bot turns into a friendly PM.
// The host adapter translates its invite service's own error into this
// sentinel so the string-match lives next to the string's owner.
var ErrNoInvites = errors.New("no invites remaining")

// Link is one verified site-user ↔ IRC-nick link. IRCNick is the active
// /NICK; AccountName is the NickServ/Atheme/Ergo services account when the
// user has registered one, or "".
type Link struct {
	UserID      int
	IRCNick     string
	AccountName string
	VerifiedAt  time.Time
}

// User is the narrow user read the bot and card need. Points and InviteCount
// feed the PM `stats` command.
type User struct {
	ID          int
	Username    string
	Role        string
	AvatarPath  string
	Points      int
	InviteCount int
	VerifyToken string // one-shot irc verify token, "" when none minted
}

// Viewer is the authenticated session user on the web leg. Only identity —
// the card is self-only and needs nothing else.
type Viewer struct {
	ID int
}

// LinkStore is the irc_links + verify-token storage. Method names match the
// host repository verbatim so the ported call sites did not move; the verify
// token is a host users column, which is why this stays a host seam rather
// than plugin-owned schema.
type LinkStore interface {
	CreateIRCLink(ctx context.Context, userID int, ircNick, accountName string) error
	GetIRCLinkByToken(ctx context.Context, token string) (*Link, *User, error)
	GetIRCLinkByUserID(ctx context.Context, userID int) (*Link, error)
	// GetIRCLinkByNick must be case-insensitive at the store level — IRC
	// nicks are canonically case-insensitive though case-preserved, and the
	// chat bridge enrichment depends on it.
	GetIRCLinkByNick(ctx context.Context, ircNick string) (*Link, error)
	DeleteIRCLink(ctx context.Context, userID int) error
	SetIRCVerifyToken(ctx context.Context, userID int, token string) error
}

// UserStore reads users — by id for enrichment and stats, by username for
// the whisper command's recipient lookup.
type UserStore interface {
	GetUserByID(ctx context.Context, id int) (*User, error)
	GetUserByUsername(ctx context.Context, username string) (*User, error)
}

// SettingReader is one keyed settings read. The host's setting repository
// satisfies it structurally. All 12 irc_* rows are read live through this —
// every connect attempt and every bridged message — so admin changes apply
// without a restart.
type SettingReader interface {
	GetSetting(ctx context.Context, key string) (string, error)
}

// DMStore persists whispers into the site's DM system. Signatures match the
// host repository verbatim, so it too crosses structurally.
type DMStore interface {
	EnsureDMThread(ctx context.Context, userA, userB int) (int64, bool, error)
	CreateDMMessage(ctx context.Context, threadID int64, senderID int, body string) (int64, error)
}

// Deps carries the host seams. Set from the host's composition root before
// core.Boot — in the SHARED section, not the worker block: the web leg (the
// /profile card) provisions too, and worker-only staging would fail a
// split-mode --mode=web boot.
type Deps struct {
	DMs      DMStore
	Links    LinkStore
	Settings SettingReader
	Users    UserStore

	// NewHub constructs this bot's own chat-hub instance. The hub stays
	// host-owned; instances converge on Redis. Called once, on the worker leg.
	NewHub func() pluginapi.ChatHub

	// CreateInvite mints a site invite for a linked user and returns its
	// token. ErrNoInvites when the user's balance is spent. Nil disables the
	// PM invite command.
	CreateInvite func(ctx context.Context, userID int) (string, error)

	// Viewer resolves the authenticated session user, nil when anonymous.
	Viewer func(c *gin.Context) *Viewer

	BaseURL string
}

var deps *Deps

// SetDeps hands the plugin its host seams.
func SetDeps(d Deps) { deps = &d }

// generateVerifyToken mints the 32-character hex token the /profile card
// shows and the bot's `link` command consumes. Copied from the host rather
// than seamed — sixteen random bytes need no adapter.
func generateVerifyToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
