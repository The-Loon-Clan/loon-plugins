package communities

import "time"

// CommunityJoinType* enumerates communities.join_type (migration
// 253). String constants mirror the DB CHECK constraint.
const (
	CommunityJoinTypeOpen       = "open"        // instant join
	CommunityJoinTypeRequest    = "request"     // owner/mods approve a queued request
	CommunityJoinTypeInviteOnly = "invite_only" // join only via an issued invite
)

// CommunityJoinRequestStatus* enumerates the request lifecycle.
const (
	JoinRequestPending   = "pending"
	JoinRequestApproved  = "approved"
	JoinRequestDenied    = "denied"
	JoinRequestWithdrawn = "withdrawn"
)

// Community is one user-owned sub-forum (a "community" in the Reddit
// sense). Schema lives in migration 252. The creator is the
// permanent owner (`OwnerUserID`) and is implicit — they aren't
// duplicated in the `community_mods` table.
//
// View-only joined columns (OwnerUsername, OwnerAvatarPath,
// SubscriberCount, ThreadCount, IsSubscribed) are populated by
// specific list / detail SELECTs; not every read fills them in.
type Community struct {
	ID          int    `db:"id"`
	Slug        string `db:"slug"`
	Name        string `db:"name"`
	Description string `db:"description"`
	SidebarMD   string `db:"sidebar_md"`
	BannerURL   string `db:"banner_url"`
	// BannerPosition is the vertical focal point (0–100) the header
	// renders the banner at via background-position-y. 50 = centred
	// (default). Migration 255.
	BannerPosition int    `db:"banner_position"`
	IconURL        string `db:"icon_url"`
	AccentColor    string `db:"accent_color"`
	OwnerUserID    int    `db:"owner_user_id"`
	ReleaseGroupID *int   `db:"release_group_id"`
	NSFW           bool   `db:"nsfw"`
	// Join gating (migration 253). JoinType is one of the
	// CommunityJoin* constants; the three requirement fields are the
	// gates checked before an open join or an accepted request (and
	// bypassed by invites).
	JoinType          string     `db:"join_type"`
	MinAccountAgeDays int        `db:"min_account_age_days"`
	MinRoleLevel      int        `db:"min_role_level"`
	JoinPointsCost    int        `db:"join_points_cost"`
	HiddenAt          *time.Time `db:"hidden_at"`
	HiddenBy          *int       `db:"hidden_by"`
	HiddenReason      string     `db:"hidden_reason"`
	CreatedAt         time.Time  `db:"created_at"`
	UpdatedAt         time.Time  `db:"updated_at"`

	// Joined view columns — populated by specific SELECTs (community
	// detail, communities index), zero/empty otherwise.
	OwnerUsername   string `db:"owner_username"`
	OwnerAvatarPath string `db:"owner_avatar_path"`
	SubscriberCount int    `db:"subscriber_count"`
	ThreadCount     int    `db:"thread_count"`
	IsSubscribed    bool   `db:"is_subscribed"`
}

// CommunityMod is one extra moderator promotion (owner is implicit).
// Populated for the mod-list sidebar render.
type CommunityMod struct {
	CommunityID int       `db:"community_id"`
	UserID      int       `db:"user_id"`
	Username    string    `db:"username"`
	Role        string    `db:"role"`
	AvatarPath  string    `db:"avatar_path"`
	AddedBy     *int      `db:"added_by"`
	AddedAt     time.Time `db:"added_at"`
}

// CommunityRule is one entry in the sidebar rule list. Body is
// plain text (no markdown) to keep the list scan-readable.
type CommunityRule struct {
	ID          int       `db:"id"`
	CommunityID int       `db:"community_id"`
	Position    int       `db:"position"`
	Title       string    `db:"title"`
	Body        string    `db:"body"`
	CreatedAt   time.Time `db:"created_at"`
}

// CommunityThread mirrors ForumThread for the community-side table
// (mig 252). Mod-removal fields are separate from the admin hide
// path so the UI can render "removed by <mod>" with the right
// context instead of conflating with site-level moderation.
type CommunityThread struct {
	ID            int        `db:"id"`
	CommunityID   int        `db:"community_id"`
	UserID        int        `db:"user_id"`
	Title         string     `db:"title"`
	Body          string     `db:"body"`
	Pinned        bool       `db:"pinned"`
	Locked        bool       `db:"locked"`
	ReplyCount    int        `db:"reply_count"`
	LastPostAt    time.Time  `db:"last_post_at"`
	RemovedAt     *time.Time `db:"removed_at"`
	RemovedBy     *int       `db:"removed_by"`
	RemovedReason string     `db:"removed_reason"`
	HiddenAt      *time.Time `db:"hidden_at"`
	HiddenBy      *int       `db:"hidden_by"`
	HiddenReason  string     `db:"hidden_reason"`
	CreatedAt     time.Time  `db:"created_at"`
	UpdatedAt     time.Time  `db:"updated_at"`

	// Joined columns for list / detail rendering.
	Username       string `db:"username"`
	UserRole       string `db:"user_role"`
	ReputationTier int    `db:"reputation_tier"`
	AvatarPath     string `db:"avatar_path"`
	CommunityName  string `db:"community_name"`
	CommunitySlug  string `db:"community_slug"`
}

// CommunityPost is one reply inside a community thread. Mirrors the
// ForumPost shape with the same dual mod-removal / admin-hide
// pattern as CommunityThread.
type CommunityPost struct {
	ID            int64      `db:"id"`
	ThreadID      int        `db:"thread_id"`
	UserID        int        `db:"user_id"`
	Body          string     `db:"body"`
	QuotedPostID  *int64     `db:"quoted_post_id"`
	RemovedAt     *time.Time `db:"removed_at"`
	RemovedBy     *int       `db:"removed_by"`
	RemovedReason string     `db:"removed_reason"`
	HiddenAt      *time.Time `db:"hidden_at"`
	HiddenBy      *int       `db:"hidden_by"`
	HiddenReason  string     `db:"hidden_reason"`
	EditedAt      *time.Time `db:"edited_at"`
	CreatedAt     time.Time  `db:"created_at"`

	// Joined display columns — populated by ListCommunityPosts.
	Username       string `db:"username"`
	UserRole       string `db:"user_role"`
	ReputationTier int    `db:"reputation_tier"`
	AvatarPath     string `db:"avatar_path"`
}

// CommunityViewerRole captures the viewer's relationship to a
// community for permission checks at the handler layer. Computed
// once per request from GetCommunityViewerRole and threaded through
// templates so mod-only controls (lock/delete/remove) render only
// when applicable.
type CommunityViewerRole struct {
	IsOwner      bool
	IsMod        bool // explicit community_mods row OR site admin
	IsSubscriber bool
	// HasPendingRequest is set by the community-view handler when the
	// viewer has an outstanding join request on a 'request'-type
	// community, so the template renders "Requested" instead of a
	// Join button.
	HasPendingRequest bool
}

// CanModerate is the cheap "can perform mod actions" check —
// owners, explicit mods, and admins all qualify. Owner+mod fields
// are set independently so callers that need fine-grained
// distinctions (only the owner can edit banner/rules) still can.
func (r CommunityViewerRole) CanModerate() bool {
	return r.IsOwner || r.IsMod
}

// CommunityJoinRequest is one applicant's request to join a
// 'request'-type community (migration 253). Joined Username /
// AvatarPath / Role columns are populated by the queue list query.
type CommunityJoinRequest struct {
	ID              int        `db:"id"`
	CommunityID     int        `db:"community_id"`
	UserID          int        `db:"user_id"`
	Message         string     `db:"message"`
	Status          string     `db:"status"`
	ResponseMessage string     `db:"response_message"`
	PointsHeld      int        `db:"points_held"`
	DecidedBy       *int       `db:"decided_by"`
	DecidedAt       *time.Time `db:"decided_at"`
	CreatedAt       time.Time  `db:"created_at"`

	// Joined display columns (queue render).
	Username       string    `db:"username"`
	Role           string    `db:"role"`
	AvatarPath     string    `db:"avatar_path"`
	AccountCreated time.Time `db:"account_created"`
	UserPoints     int       `db:"user_points"`
}

// CommunityInvite is an owner/mod-issued invite code for an
// invite-gated (or otherwise restricted) community. Redeeming
// bypasses the requirement gates.
type CommunityInvite struct {
	ID          int        `db:"id"`
	CommunityID int        `db:"community_id"`
	Code        string     `db:"code"`
	Note        string     `db:"note"`
	CreatedBy   *int       `db:"created_by"`
	MaxUses     int        `db:"max_uses"`
	UseCount    int        `db:"use_count"`
	ExpiresAt   *time.Time `db:"expires_at"`
	CreatedAt   time.Time  `db:"created_at"`
}

// IsUsable reports whether the invite can still be redeemed — not
// expired and under its use cap (max_uses==0 means unlimited).
func (i *CommunityInvite) IsUsable(now time.Time) bool {
	if i.ExpiresAt != nil && now.After(*i.ExpiresAt) {
		return false
	}
	if i.MaxUses > 0 && i.UseCount >= i.MaxUses {
		return false
	}
	return true
}
