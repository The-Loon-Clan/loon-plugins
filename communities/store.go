package communities

import (
	"context"
	"errors"
	"time"
)

// ErrInviteUnusable is returned by RedeemCommunityInvite when the
// code is expired or has hit its use cap. The handler maps it to a
// friendly "this invite is no longer valid" message.
var ErrInviteUnusable = errors.New("community invite is expired or fully used")

// Store owns the user-owned sub-forum surface
// (migration 252): communities, mods, rules, subscribers, threads,
// posts. Separate from the existing admin-curated forum_categories /
// forum_threads / forum_posts so the two domains can evolve without
// stepping on each other.
//
// Phase 1 ships the methods needed for the create / view / post /
// subscribe flow. Phase 2 layers on bans, modmail, wiki, links.
//
// Method grouping matches the schema sections in migration 252.
type Store interface {
	// ── Communities ───────────────────────────────────────────────

	// CreateCommunity inserts a new community row. The caller has
	// already validated the slug shape (URL-safe, unique). The row's
	// id + created_at are returned via the input pointer being
	// updated in place. Slug uniqueness is enforced at the DB level
	// via the schema's UNIQUE constraint; conflicts surface as a pq
	// error the handler can map to a friendly message.
	CreateCommunity(ctx context.Context, c *Community) error

	// GetCommunityBySlug returns one community by URL slug, with the
	// owner's username + avatar joined for the header render and an
	// IsSubscribed flag for the supplied viewer (pass 0 for anon).
	// Returns sql.ErrNoRows on miss; hidden communities also miss
	// for non-admin / non-owner viewers (filter happens at the
	// handler layer with the role lookup below).
	GetCommunityBySlug(ctx context.Context, slug string, viewerID int) (*Community, error)

	// GetCommunityByID is the id-keyed twin of GetCommunityBySlug.
	// Same join shape; no IsSubscribed flag (id lookups happen in
	// admin / internal paths that don't need it).
	GetCommunityByID(ctx context.Context, id int) (*Community, error)

	// ListCommunities returns one page of non-hidden communities
	// ordered by subscriber count DESC for the /c index. Carries
	// joined SubscriberCount + ThreadCount so the card grid renders
	// without per-row lookups.
	ListCommunities(ctx context.Context, limit, offset int) ([]*Community, int, error)

	// ListSubscribedCommunities returns every non-hidden community
	// the user has joined, newest-subscription first. Backs the
	// account-settings "Following" tab. Carries the same joined
	// counts as ListCommunities.
	ListSubscribedCommunities(ctx context.Context, userID int) ([]*Community, error)

	// UpdateCommunityCustomization patches the customisation columns
	// (name / description / sidebar_md / banner_url / icon_url /
	// accent_color / nsfw). Owner-only at the handler layer. Returns
	// the row count for an "updated zero rows" sanity check.
	UpdateCommunityCustomization(ctx context.Context, id int, c *Community) (int64, error)

	// UpdateCommunityJoinSettings patches the join-gating columns
	// (join_type + the three requirement gates). Owner-only.
	UpdateCommunityJoinSettings(ctx context.Context, id int, joinType string, minAgeDays, minRoleLevel, pointsCost int) error

	// ── Join requests (migration 253) ─────────────────────────────

	// CreateJoinRequest inserts a pending request. pointsHeld is the
	// amount escrowed at request time (0 when the community has no
	// points cost). The partial unique index rejects a second
	// pending request from the same user — the handler maps the
	// conflict to "already requested".
	CreateJoinRequest(ctx context.Context, communityID, userID int, message string, pointsHeld int) error

	// GetUserJoinRequest returns the user's most-recent request for a
	// community (any status), or (nil, nil) when none exists. Lets
	// the view handler render "Requested" / "Denied" states.
	GetUserJoinRequest(ctx context.Context, communityID, userID int) (*CommunityJoinRequest, error)

	// ListPendingJoinRequests returns the mod queue for a community —
	// pending rows with the applicant's username / role / avatar /
	// account-age / points joined for the requirement-at-a-glance
	// display.
	ListPendingJoinRequests(ctx context.Context, communityID int) ([]*CommunityJoinRequest, error)

	// GetJoinRequest fetches one request by id (for the approve/deny
	// action, which needs community_id + points_held + user_id).
	GetJoinRequest(ctx context.Context, requestID int) (*CommunityJoinRequest, error)

	// ApproveJoinRequest marks the request approved + inserts the
	// subscriber row in one transaction. Points stay spent (the
	// escrow becomes the join fee). No-op if the row isn't pending.
	ApproveJoinRequest(ctx context.Context, requestID, deciderID int, responseMessage string) error

	// DenyJoinRequest marks the request denied. Returns the
	// (userID, pointsHeld) so the handler can refund the escrow via
	// the points repo. No-op if the row isn't pending (returns 0,0).
	DenyJoinRequest(ctx context.Context, requestID, deciderID int, responseMessage string) (userID, pointsHeld int, err error)

	// ── Invites (migration 253) ───────────────────────────────────

	CreateCommunityInvite(ctx context.Context, communityID, createdBy int, code, note string, maxUses int, expiresAt *time.Time) error
	GetCommunityInviteByCode(ctx context.Context, code string) (*CommunityInvite, error)
	ListCommunityInvites(ctx context.Context, communityID int) ([]*CommunityInvite, error)

	// RedeemCommunityInvite bumps use_count + inserts the subscriber
	// row in one transaction, guarding the use cap so two concurrent
	// redemptions can't both slip past max_uses. Returns
	// ErrInviteUnusable when expired / capped.
	RedeemCommunityInvite(ctx context.Context, code string, userID int) (communityID int, err error)

	// ── Viewer role ───────────────────────────────────────────────

	// GetCommunityViewerRole resolves the (owner, mod, subscriber)
	// triple for one (community, user) pair in a single round-trip.
	// userID==0 returns all-false. Admins should set the IsMod field
	// on the returned struct after the call — this method only
	// inspects community-scoped state.
	GetCommunityViewerRole(ctx context.Context, communityID, userID int) (CommunityViewerRole, error)

	// ── Subscribers ───────────────────────────────────────────────

	SubscribeCommunity(ctx context.Context, communityID, userID int) error
	UnsubscribeCommunity(ctx context.Context, communityID, userID int) error

	// ── Mods ──────────────────────────────────────────────────────

	ListCommunityMods(ctx context.Context, communityID int) ([]*CommunityMod, error)
	AddCommunityMod(ctx context.Context, communityID, userID, addedBy int) error
	RemoveCommunityMod(ctx context.Context, communityID, userID int) error

	// ── Rules ─────────────────────────────────────────────────────

	ListCommunityRules(ctx context.Context, communityID int) ([]*CommunityRule, error)
	CreateCommunityRule(ctx context.Context, communityID, position int, title, body string) error
	DeleteCommunityRule(ctx context.Context, ruleID int) error

	// ── Threads ───────────────────────────────────────────────────

	// CreateCommunityThread inserts a thread + its first post is
	// expected to be the thread body itself; replies are separate
	// rows in community_posts. Returns the inserted thread fetched
	// back via GetCommunityThread so the joined OP user columns are
	// populated for the redirect target.
	CreateCommunityThread(ctx context.Context, communityID, userID int, title, body string) (*CommunityThread, error)

	// ListCommunityThreads returns one page of threads inside a
	// community, ordered pinned DESC then last_post_at DESC. Excludes
	// soft-removed (removed_at IS NOT NULL) and admin-hidden
	// (hidden_at IS NOT NULL) rows by default — mods can fetch with
	// the includeRemoved flag for the queue.
	ListCommunityThreads(ctx context.Context, communityID, limit, offset int, includeRemoved bool) ([]*CommunityThread, int, error)

	// GetCommunityThread is the single-thread fetch with joined OP
	// columns + community slug/name for breadcrumbs. Returns
	// sql.ErrNoRows on miss; the handler maps removed/hidden rows
	// per the viewer's role.
	GetCommunityThread(ctx context.Context, threadID int) (*CommunityThread, error)

	// The three thread-moderation writes take communityID and scope the row to
	// it: any member can create a community and moderate it, so a handler that
	// only checked "you moderate the community in the URL" while updating by a
	// bare thread id let a mod of one community act on threads in every other.
	// The community predicate makes a foreign thread id a no-op.
	SetCommunityThreadPinned(ctx context.Context, threadID, communityID int, pinned bool) error
	SetCommunityThreadLocked(ctx context.Context, threadID, communityID int, locked bool) error
	RemoveCommunityThread(ctx context.Context, threadID, communityID, byUserID int, reason string) error

	// ── Posts ─────────────────────────────────────────────────────

	CreateCommunityPost(ctx context.Context, threadID, userID int, body string, quotedPostID *int64) (*CommunityPost, error)
	ListCommunityPosts(ctx context.Context, threadID, limit, offset int) ([]*CommunityPost, int, error)
	UpdateCommunityPost(ctx context.Context, postID int64, userID int, body string) error
	// communityID scopes the removal through the post's thread — same
	// cross-community guard as the thread writes above.
	RemoveCommunityPost(ctx context.Context, postID int64, communityID, byUserID int, reason string) error
}
