package communities

// CommunityRepository — Postgres-backed implementation of
// Store (the user-owned sub-forum surface
// introduced in migration 252). Mirrors the structure of the
// admin-curated forum's store (now the loon-plugins forum plugin,
// forum_categories/threads/posts) but operates on the community_*
// tables and adds the owner/mod/subscriber permission triple.

import (
	"context"
	"database/sql"
	"time"

	"github.com/jmoiron/sqlx"
)

type PGStore struct {
	db *sqlx.DB
}

func NewPGStore(db *sqlx.DB) *PGStore {
	return &PGStore{db: db}
}

var _ Store = (*PGStore)(nil)

// ── Communities ────────────────────────────────────────────────────

func (r *PGStore) CreateCommunity(ctx context.Context, c *Community) error {
	return r.db.QueryRowxContext(ctx, `
		INSERT INTO communities (
			slug, name, description, sidebar_md, banner_url, icon_url,
			accent_color, owner_user_id, release_group_id, nsfw
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id, created_at, updated_at`,
		c.Slug, c.Name, c.Description, c.SidebarMD, c.BannerURL, c.IconURL,
		c.AccentColor, c.OwnerUserID, c.ReleaseGroupID, c.NSFW,
	).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
}

// communityCols is the projection both single-row + list queries
// pull. Keeping it as a const makes column drift between paths a
// compile rather than a runtime issue.
const communityCols = `c.id, c.slug, c.name, c.description, c.sidebar_md,
	c.banner_url, COALESCE(c.banner_position, 50) AS banner_position,
	c.icon_url, c.accent_color, c.owner_user_id,
	c.release_group_id, c.nsfw,
	COALESCE(c.join_type, 'open') AS join_type,
	COALESCE(c.min_account_age_days, 0) AS min_account_age_days,
	COALESCE(c.min_role_level, 0) AS min_role_level,
	COALESCE(c.join_points_cost, 0) AS join_points_cost,
	c.hidden_at, c.hidden_by, c.hidden_reason,
	c.created_at, c.updated_at`

func (r *PGStore) GetCommunityBySlug(ctx context.Context, slug string, viewerID int) (*Community, error) {
	var c Community
	// Counts are scalar subqueries SCOPED to this community (index
	// scan on community_id) rather than GROUP-BY aggregates over the
	// whole subscribers/threads tables — single-row read, so don't
	// pay to aggregate every community on the site.
	err := r.db.GetContext(ctx, &c, `
		SELECT `+communityCols+`,
		       COALESCE(u.username, '')    AS owner_username,
		       COALESCE(u.avatar_path, '') AS owner_avatar_path,
		       (SELECT COUNT(*)::int FROM community_subscribers s WHERE s.community_id = c.id) AS subscriber_count,
		       (SELECT COUNT(*)::int FROM community_threads t
		         WHERE t.community_id = c.id AND t.hidden_at IS NULL AND t.removed_at IS NULL) AS thread_count,
		       (CASE WHEN $2 > 0 AND EXISTS (
		            SELECT 1 FROM community_subscribers vs
		             WHERE vs.community_id = c.id AND vs.user_id = $2
		        ) THEN TRUE ELSE FALSE END) AS is_subscribed
		FROM communities c
		LEFT JOIN users u ON u.id = c.owner_user_id
		WHERE c.slug = $1`, slug, viewerID)
	return &c, err
}

func (r *PGStore) GetCommunityByID(ctx context.Context, id int) (*Community, error) {
	var c Community
	err := r.db.GetContext(ctx, &c, `
		SELECT `+communityCols+`,
		       COALESCE(u.username, '')    AS owner_username,
		       COALESCE(u.avatar_path, '') AS owner_avatar_path
		FROM communities c
		LEFT JOIN users u ON u.id = c.owner_user_id
		WHERE c.id = $1`, id)
	return &c, err
}

func (r *PGStore) ListCommunities(ctx context.Context, limit, offset int) ([]*Community, int, error) {
	var total int
	if err := r.db.GetContext(ctx, &total,
		`SELECT COUNT(*) FROM communities WHERE hidden_at IS NULL`); err != nil {
		return nil, 0, err
	}
	var rows []*Community
	err := r.db.SelectContext(ctx, &rows, `
		SELECT `+communityCols+`,
		       COALESCE(u.username, '')    AS owner_username,
		       COALESCE(u.avatar_path, '') AS owner_avatar_path,
		       COALESCE(s.cnt, 0)          AS subscriber_count,
		       COALESCE(t.cnt, 0)          AS thread_count
		FROM communities c
		LEFT JOIN users u ON u.id = c.owner_user_id
		LEFT JOIN (
		    SELECT community_id, COUNT(*)::int AS cnt
		      FROM community_subscribers GROUP BY community_id
		) s ON s.community_id = c.id
		LEFT JOIN (
		    SELECT community_id, COUNT(*)::int AS cnt
		      FROM community_threads
		     WHERE hidden_at IS NULL AND removed_at IS NULL
		     GROUP BY community_id
		) t ON t.community_id = c.id
		WHERE c.hidden_at IS NULL
		ORDER BY COALESCE(s.cnt, 0) DESC, c.created_at DESC
		LIMIT $1 OFFSET $2`, limit, offset)
	return rows, total, err
}

func (r *PGStore) ListSubscribedCommunities(ctx context.Context, userID int) ([]*Community, error) {
	var rows []*Community
	err := r.db.SelectContext(ctx, &rows, `
		SELECT `+communityCols+`,
		       COALESCE(u.username, '')    AS owner_username,
		       COALESCE(u.avatar_path, '') AS owner_avatar_path,
		       COALESCE(s.cnt, 0)          AS subscriber_count,
		       COALESCE(t.cnt, 0)          AS thread_count,
		       TRUE                        AS is_subscribed
		FROM community_subscribers sub
		JOIN communities c ON c.id = sub.community_id
		LEFT JOIN users u ON u.id = c.owner_user_id
		LEFT JOIN (
		    SELECT community_id, COUNT(*)::int AS cnt
		      FROM community_subscribers GROUP BY community_id
		) s ON s.community_id = c.id
		LEFT JOIN (
		    SELECT community_id, COUNT(*)::int AS cnt
		      FROM community_threads
		     WHERE hidden_at IS NULL AND removed_at IS NULL
		     GROUP BY community_id
		) t ON t.community_id = c.id
		WHERE sub.user_id = $1 AND c.hidden_at IS NULL
		ORDER BY sub.subscribed_at DESC`, userID)
	return rows, err
}

func (r *PGStore) UpdateCommunityCustomization(ctx context.Context, id int, c *Community) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE communities SET
		    name            = $2,
		    description     = $3,
		    sidebar_md      = $4,
		    banner_url      = $5,
		    icon_url        = $6,
		    accent_color    = $7,
		    nsfw            = $8,
		    banner_position = $9,
		    updated_at      = NOW()
		WHERE id = $1`,
		id, c.Name, c.Description, c.SidebarMD, c.BannerURL, c.IconURL, c.AccentColor, c.NSFW, c.BannerPosition)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ── Viewer role ────────────────────────────────────────────────────

func (r *PGStore) GetCommunityViewerRole(ctx context.Context, communityID, userID int) (CommunityViewerRole, error) {
	out := CommunityViewerRole{}
	if userID == 0 {
		return out, nil
	}
	var row struct {
		IsOwner      bool `db:"is_owner"`
		IsMod        bool `db:"is_mod"`
		IsSubscriber bool `db:"is_subscriber"`
	}
	err := r.db.GetContext(ctx, &row, `
		SELECT
		    EXISTS (SELECT 1 FROM communities         WHERE id           = $1 AND owner_user_id = $2) AS is_owner,
		    EXISTS (SELECT 1 FROM community_mods      WHERE community_id = $1 AND user_id       = $2) AS is_mod,
		    EXISTS (SELECT 1 FROM community_subscribers WHERE community_id = $1 AND user_id     = $2) AS is_subscriber`,
		communityID, userID)
	if err != nil {
		return out, err
	}
	out.IsOwner = row.IsOwner
	out.IsMod = row.IsMod
	out.IsSubscriber = row.IsSubscriber
	return out, nil
}

// ── Subscribers ────────────────────────────────────────────────────

func (r *PGStore) SubscribeCommunity(ctx context.Context, communityID, userID int) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO community_subscribers (community_id, user_id)
		VALUES ($1, $2)
		ON CONFLICT (community_id, user_id) DO NOTHING`,
		communityID, userID)
	return err
}

func (r *PGStore) UnsubscribeCommunity(ctx context.Context, communityID, userID int) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM community_subscribers WHERE community_id = $1 AND user_id = $2`,
		communityID, userID)
	return err
}

// ── Mods ───────────────────────────────────────────────────────────

func (r *PGStore) ListCommunityMods(ctx context.Context, communityID int) ([]*CommunityMod, error) {
	var rows []*CommunityMod
	err := r.db.SelectContext(ctx, &rows, `
		SELECT cm.community_id, cm.user_id, cm.added_by, cm.added_at,
		       u.username, COALESCE(u.role, 'user') AS role,
		       COALESCE(u.avatar_path, '') AS avatar_path
		FROM community_mods cm
		JOIN users u ON u.id = cm.user_id
		WHERE cm.community_id = $1
		ORDER BY cm.added_at ASC`, communityID)
	return rows, err
}

func (r *PGStore) AddCommunityMod(ctx context.Context, communityID, userID, addedBy int) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO community_mods (community_id, user_id, added_by)
		VALUES ($1, $2, $3)
		ON CONFLICT (community_id, user_id) DO NOTHING`,
		communityID, userID, addedBy)
	return err
}

func (r *PGStore) RemoveCommunityMod(ctx context.Context, communityID, userID int) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM community_mods WHERE community_id = $1 AND user_id = $2`,
		communityID, userID)
	return err
}

// ── Rules ──────────────────────────────────────────────────────────

func (r *PGStore) ListCommunityRules(ctx context.Context, communityID int) ([]*CommunityRule, error) {
	var rows []*CommunityRule
	err := r.db.SelectContext(ctx, &rows, `
		SELECT id, community_id, position, title, body, created_at
		FROM community_rules
		WHERE community_id = $1
		ORDER BY position ASC, id ASC`, communityID)
	return rows, err
}

func (r *PGStore) CreateCommunityRule(ctx context.Context, communityID, position int, title, body string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO community_rules (community_id, position, title, body)
		VALUES ($1, $2, $3, $4)`, communityID, position, title, body)
	return err
}

func (r *PGStore) DeleteCommunityRule(ctx context.Context, ruleID int) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM community_rules WHERE id = $1`, ruleID)
	return err
}

// ── Threads ────────────────────────────────────────────────────────

func (r *PGStore) CreateCommunityThread(ctx context.Context, communityID, userID int, title, body string) (*CommunityThread, error) {
	var threadID int
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO community_threads (community_id, user_id, title, body)
		VALUES ($1, $2, $3, $4)
		RETURNING id`, communityID, userID, title, body).Scan(&threadID)
	if err != nil {
		return nil, err
	}
	return r.GetCommunityThread(ctx, threadID)
}

// communityThreadCols mirrors the column projection for both
// single-thread and list reads so the JOIN shape stays in sync.
const communityThreadCols = `ct.id, ct.community_id, ct.user_id, ct.title, ct.body,
	ct.pinned, ct.locked, ct.reply_count, ct.last_post_at,
	ct.removed_at, ct.removed_by, ct.removed_reason,
	ct.hidden_at, ct.hidden_by, ct.hidden_reason,
	ct.created_at, ct.updated_at,
	u.username                                 AS username,
	COALESCE(u.role, 'user')                   AS user_role,
	COALESCE(u.reputation_tier, 0)             AS reputation_tier,
	COALESCE(u.avatar_path, '')                AS avatar_path,
	c.name                                     AS community_name,
	c.slug                                     AS community_slug`

func (r *PGStore) ListCommunityThreads(ctx context.Context, communityID, limit, offset int, includeRemoved bool) ([]*CommunityThread, int, error) {
	// Removed/hidden threads are excluded unless the caller is a mod
	// asking for the queue. The filter is the same on the COUNT to
	// keep pagination honest.
	visibilityFilter := ` AND ct.hidden_at IS NULL AND ct.removed_at IS NULL`
	if includeRemoved {
		visibilityFilter = ``
	}

	var total int
	// sqllint:allow visibilityFilter is a fixed literal — one of two hard-coded strings, never user-supplied
	if err := r.db.GetContext(ctx, &total,
		`SELECT COUNT(*) FROM community_threads ct WHERE ct.community_id = $1`+visibilityFilter,
		communityID); err != nil {
		return nil, 0, err
	}
	var rows []*CommunityThread
	// sqllint:allow communityThreadCols is a package const; visibilityFilter is a fixed literal — values flow through $N
	err := r.db.SelectContext(ctx, &rows, `
		SELECT `+communityThreadCols+`
		FROM community_threads ct
		JOIN users u       ON u.id = ct.user_id
		JOIN communities c ON c.id = ct.community_id
		WHERE ct.community_id = $1`+visibilityFilter+`
		ORDER BY ct.pinned DESC, ct.last_post_at DESC
		LIMIT $2 OFFSET $3`, communityID, limit, offset)
	return rows, total, err
}

func (r *PGStore) GetCommunityThread(ctx context.Context, threadID int) (*CommunityThread, error) {
	var t CommunityThread
	err := r.db.GetContext(ctx, &t, `
		SELECT `+communityThreadCols+`
		FROM community_threads ct
		JOIN users u       ON u.id = ct.user_id
		JOIN communities c ON c.id = ct.community_id
		WHERE ct.id = $1`, threadID)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *PGStore) SetCommunityThreadPinned(ctx context.Context, threadID int, pinned bool) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE community_threads SET pinned = $2, updated_at = NOW() WHERE id = $1`,
		threadID, pinned)
	return err
}

func (r *PGStore) SetCommunityThreadLocked(ctx context.Context, threadID int, locked bool) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE community_threads SET locked = $2, updated_at = NOW() WHERE id = $1`,
		threadID, locked)
	return err
}

func (r *PGStore) RemoveCommunityThread(ctx context.Context, threadID, byUserID int, reason string) error {
	if len(reason) > 256 {
		reason = reason[:256]
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE community_threads
		   SET removed_at = NOW(), removed_by = $2, removed_reason = $3, updated_at = NOW()
		 WHERE id = $1`, threadID, byUserID, reason)
	return err
}

// ── Posts ──────────────────────────────────────────────────────────

func (r *PGStore) CreateCommunityPost(ctx context.Context, threadID, userID int, body string, quotedPostID *int64) (*CommunityPost, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var p CommunityPost
	if err := tx.QueryRowxContext(ctx, `
		INSERT INTO community_posts (thread_id, user_id, body, quoted_post_id)
		VALUES ($1, $2, $3, $4)
		RETURNING id, thread_id, user_id, body, quoted_post_id,
		          removed_at, removed_by, removed_reason,
		          hidden_at, hidden_by, hidden_reason,
		          edited_at, created_at`,
		threadID, userID, body, quotedPostID).StructScan(&p); err != nil {
		return nil, err
	}
	// Bump the parent thread's reply counter + last_post_at so the
	// community thread list sorts the new activity to the top.
	if _, err := tx.ExecContext(ctx, `
		UPDATE community_threads
		   SET reply_count = reply_count + 1,
		       last_post_at = NOW(),
		       updated_at   = NOW()
		 WHERE id = $1`, threadID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *PGStore) ListCommunityPosts(ctx context.Context, threadID, limit, offset int) ([]*CommunityPost, int, error) {
	var total int
	if err := r.db.GetContext(ctx, &total,
		`SELECT COUNT(*) FROM community_posts
		  WHERE thread_id = $1 AND hidden_at IS NULL AND removed_at IS NULL`,
		threadID); err != nil {
		return nil, 0, err
	}
	var rows []*CommunityPost
	err := r.db.SelectContext(ctx, &rows, `
		SELECT cp.id, cp.thread_id, cp.user_id, cp.body, cp.quoted_post_id,
		       cp.removed_at, cp.removed_by, cp.removed_reason,
		       cp.hidden_at, cp.hidden_by, cp.hidden_reason,
		       cp.edited_at, cp.created_at,
		       u.username                          AS username,
		       COALESCE(u.role, 'user')            AS user_role,
		       COALESCE(u.reputation_tier, 0)      AS reputation_tier,
		       COALESCE(u.avatar_path, '')         AS avatar_path
		FROM community_posts cp
		JOIN users u ON u.id = cp.user_id
		WHERE cp.thread_id = $1 AND cp.hidden_at IS NULL AND cp.removed_at IS NULL
		ORDER BY cp.created_at ASC
		LIMIT $2 OFFSET $3`, threadID, limit, offset)
	return rows, total, err
}

func (r *PGStore) UpdateCommunityPost(ctx context.Context, postID int64, userID int, body string) error {
	// Owner-only edit: row must match (id, user_id) so a crafted
	// post_id can't let user A edit user B's reply.
	res, err := r.db.ExecContext(ctx, `
		UPDATE community_posts
		   SET body = $3, edited_at = NOW()
		 WHERE id = $1 AND user_id = $2`,
		postID, userID, body)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *PGStore) RemoveCommunityPost(ctx context.Context, postID int64, byUserID int, reason string) error {
	if len(reason) > 256 {
		reason = reason[:256]
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE community_posts
		   SET removed_at = NOW(), removed_by = $2, removed_reason = $3
		 WHERE id = $1`, postID, byUserID, reason)
	return err
}

// ── Join settings + requests + invites (migration 253) ─────────────

func (r *PGStore) UpdateCommunityJoinSettings(ctx context.Context, id int, joinType string, minAgeDays, minRoleLevel, pointsCost int) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE communities SET
		    join_type            = $2,
		    min_account_age_days = $3,
		    min_role_level       = $4,
		    join_points_cost     = $5,
		    updated_at           = NOW()
		WHERE id = $1`,
		id, joinType, minAgeDays, minRoleLevel, pointsCost)
	return err
}

func (r *PGStore) CreateJoinRequest(ctx context.Context, communityID, userID int, message string, pointsHeld int) error {
	if len(message) > 1000 {
		message = message[:1000]
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO community_join_requests (community_id, user_id, message, points_held)
		VALUES ($1, $2, $3, $4)`,
		communityID, userID, message, pointsHeld)
	return err
}

func (r *PGStore) GetUserJoinRequest(ctx context.Context, communityID, userID int) (*CommunityJoinRequest, error) {
	var jr CommunityJoinRequest
	err := r.db.GetContext(ctx, &jr, `
		SELECT id, community_id, user_id, message, status, response_message,
		       points_held, decided_by, decided_at, created_at
		FROM community_join_requests
		WHERE community_id = $1 AND user_id = $2
		ORDER BY created_at DESC
		LIMIT 1`, communityID, userID)
	if err != nil {
		return nil, noRowsAsNil(err)
	}
	return &jr, nil
}

func (r *PGStore) ListPendingJoinRequests(ctx context.Context, communityID int) ([]*CommunityJoinRequest, error) {
	var rows []*CommunityJoinRequest
	err := r.db.SelectContext(ctx, &rows, `
		SELECT jr.id, jr.community_id, jr.user_id, jr.message, jr.status,
		       jr.response_message, jr.points_held, jr.decided_by,
		       jr.decided_at, jr.created_at,
		       u.username                          AS username,
		       COALESCE(u.role, 'user')            AS role,
		       COALESCE(u.avatar_path, '')         AS avatar_path,
		       u.created_at                        AS account_created,
		       COALESCE(u.points, 0)               AS user_points
		FROM community_join_requests jr
		JOIN users u ON u.id = jr.user_id
		WHERE jr.community_id = $1 AND jr.status = 'pending'
		ORDER BY jr.created_at ASC`, communityID)
	return rows, err
}

func (r *PGStore) GetJoinRequest(ctx context.Context, requestID int) (*CommunityJoinRequest, error) {
	var jr CommunityJoinRequest
	err := r.db.GetContext(ctx, &jr, `
		SELECT id, community_id, user_id, message, status, response_message,
		       points_held, decided_by, decided_at, created_at
		FROM community_join_requests
		WHERE id = $1`, requestID)
	if err != nil {
		return nil, err
	}
	return &jr, nil
}

func (r *PGStore) ApproveJoinRequest(ctx context.Context, requestID, deciderID int, responseMessage string) error {
	if len(responseMessage) > 1000 {
		responseMessage = responseMessage[:1000]
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Flip to approved only if still pending; capture the user so we
	// can insert the subscriber row. No rows → already decided, bail
	// without touching subscribers.
	var communityID, userID int
	err = tx.QueryRowContext(ctx, `
		UPDATE community_join_requests
		   SET status = 'approved', decided_by = $2, decided_at = NOW(),
		       response_message = $3
		 WHERE id = $1 AND status = 'pending'
		RETURNING community_id, user_id`,
		requestID, deciderID, responseMessage).Scan(&communityID, &userID)
	if err == sql.ErrNoRows {
		return nil // already decided; idempotent no-op
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO community_subscribers (community_id, user_id)
		VALUES ($1, $2)
		ON CONFLICT (community_id, user_id) DO NOTHING`,
		communityID, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *PGStore) DenyJoinRequest(ctx context.Context, requestID, deciderID int, responseMessage string) (int, int, error) {
	if len(responseMessage) > 1000 {
		responseMessage = responseMessage[:1000]
	}
	var userID, pointsHeld int
	err := r.db.QueryRowContext(ctx, `
		UPDATE community_join_requests
		   SET status = 'denied', decided_by = $2, decided_at = NOW(),
		       response_message = $3
		 WHERE id = $1 AND status = 'pending'
		RETURNING user_id, points_held`,
		requestID, deciderID, responseMessage).Scan(&userID, &pointsHeld)
	if err == sql.ErrNoRows {
		return 0, 0, nil // already decided
	}
	if err != nil {
		return 0, 0, err
	}
	return userID, pointsHeld, nil
}

func (r *PGStore) CreateCommunityInvite(ctx context.Context, communityID, createdBy int, code, note string, maxUses int, expiresAt *time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO community_invites (community_id, created_by, code, note, max_uses, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		communityID, createdBy, code, note, maxUses, expiresAt)
	return err
}

func (r *PGStore) GetCommunityInviteByCode(ctx context.Context, code string) (*CommunityInvite, error) {
	var inv CommunityInvite
	err := r.db.GetContext(ctx, &inv, `
		SELECT id, community_id, code, note, created_by, max_uses, use_count, expires_at, created_at
		FROM community_invites WHERE code = $1`, code)
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

func (r *PGStore) ListCommunityInvites(ctx context.Context, communityID int) ([]*CommunityInvite, error) {
	var rows []*CommunityInvite
	err := r.db.SelectContext(ctx, &rows, `
		SELECT id, community_id, code, note, created_by, max_uses, use_count, expires_at, created_at
		FROM community_invites
		WHERE community_id = $1
		ORDER BY created_at DESC`, communityID)
	return rows, err
}

func (r *PGStore) RedeemCommunityInvite(ctx context.Context, code string, userID int) (int, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// Row-lock the invite so two concurrent redemptions can't both
	// pass the use-cap check. The WHERE enforces not-expired AND
	// (unlimited OR under-cap) atomically with the increment.
	var communityID int
	err = tx.QueryRowContext(ctx, `
		UPDATE community_invites
		   SET use_count = use_count + 1
		 WHERE code = $1
		   AND (expires_at IS NULL OR expires_at > NOW())
		   AND (max_uses = 0 OR use_count < max_uses)
		RETURNING community_id`, code).Scan(&communityID)
	if err == sql.ErrNoRows {
		return 0, ErrInviteUnusable
	}
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO community_subscribers (community_id, user_id)
		VALUES ($1, $2)
		ON CONFLICT (community_id, user_id) DO NOTHING`,
		communityID, userID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return communityID, nil
}
