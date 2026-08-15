package forum

import "time"

type ForumCategory struct {
	ID          int    `db:"id"`
	Name        string `db:"name"`
	Description string `db:"description"`
	Ordinal     int    `db:"ordinal"`
	Color       string `db:"color"`
	Icon        string `db:"icon"`
	// Access gates — see access.go for the model (role OR tier per gate).
	SeeRole     string     `db:"see_role"`
	ReadRole    string     `db:"read_role"`
	WriteRole   string     `db:"write_role"`
	SeeTier     int        `db:"see_tier"`
	ReadTier    int        `db:"read_tier"`
	WriteTier   int        `db:"write_tier"`
	ThreadCount int        `db:"thread_count"`
	PostCount   int        `db:"post_count"`
	LastPostAt  *time.Time `db:"last_post_at"`
	CreatedAt   time.Time  `db:"created_at"`

	// Latest activity preview (populated by GetForumCategories only).
	LastThreadID    *int    `db:"last_thread_id"`
	LastThreadTitle *string `db:"last_thread_title"`
	LastPostUser    *string `db:"last_post_user"`
}

// ForumActivityItem is a single recent post for the sidebar feed.
// Carries the poster's avatar + role so the sidebar can render the
// AnimeZ-style avatar-circle + role-colored name without N+1 lookups.
// Category icon/color piggy-back on the same JOIN so each row in
// the Recent Activity card shows which category the post belongs to
// (matches the category-list icons one card up the sidebar).
type ForumActivityItem struct {
	PostID        int64     `db:"post_id"`
	ThreadID      int       `db:"thread_id"`
	ThreadTitle   string    `db:"thread_title"`
	UserID        int       `db:"user_id"`
	Username      string    `db:"username"`
	Role          string    `db:"role"`
	AvatarPath    string    `db:"avatar_path"`
	CategoryID    int       `db:"category_id"`
	CategoryIcon  string    `db:"category_icon"`
	CategoryColor string    `db:"category_color"`
	CreatedAt     time.Time `db:"created_at"`
}

// ForumContributor is a single user in the Top Contributors sidebar card.
type ForumContributor struct {
	UserID     int    `db:"user_id"`
	Username   string `db:"username"`
	Role       string `db:"role"`
	AvatarPath string `db:"avatar_path"`
	PostCount  int    `db:"post_count"`
}

// ForumThreadType* enumerates the allowed values for
// forum_threads.thread_type (mig 251). String constants so callers
// reference the same literal as the DB CHECK constraint.
const (
	ForumThreadTypeDiscussion  = "discussion"
	ForumThreadTypeRecruitment = "recruitment"
)

type ForumThread struct {
	ID         int    `db:"id"`
	CategoryID int    `db:"category_id"`
	UserID     int    `db:"user_id"`
	Username   string `db:"username"`
	// ThreadType controls reply visibility. "discussion" (default)
	// shows every reply to every viewer. "recruitment" hides replies
	// from other applicants — only the reply author, the thread
	// author (the recruiter), and admins see the full reply list.
	// See migration 251.
	ThreadType string `db:"thread_type"`
	// Role + AvatarPath for the OP — drives the avatar circle and
	// role-colored name on the category thread list (mirrors the
	// AnimeZ "BA · baiduzhe" presentation).
	Role       string    `db:"role"`
	AvatarPath string    `db:"avatar_path"`
	Title      string    `db:"title"`
	Pinned     bool      `db:"pinned"`
	Locked     bool      `db:"locked"`
	ReplyCount int       `db:"reply_count"`
	LastPostAt time.Time `db:"last_post_at"`
	CreatedAt  time.Time `db:"created_at"`
	// Last-reply user — pulled via LATERAL subquery so we don't N+1
	// the thread list. Empty when the thread has no replies yet
	// (the OP IS the last post — same person, but we leave the
	// last-reply slot empty so the template knows to skip the
	// "last reply by X" badge in that case).
	LastPostUserID     *int    `db:"last_post_user_id"`
	LastPostUsername   *string `db:"last_post_username"`
	LastPostRole       *string `db:"last_post_role"`
	LastPostAvatarPath *string `db:"last_post_avatar_path"`
	// category name for breadcrumbs
	CategoryName string `db:"category_name"`
}

type ForumPost struct {
	ID       int64  `db:"id"`
	ThreadID int    `db:"thread_id"`
	UserID   int    `db:"user_id"`
	Username string `db:"username"`
	UserRole string `db:"user_role"`
	// ReputationTier is the author's earned reputation ladder (migration 266),
	// rendered as a badge next to their name. Populated by GetForumPosts from
	// users.reputation_tier — MUST stay selected there or the community_thread
	// template errors on {{repBadge .ReputationTier}} and aborts the render.
	ReputationTier int    `db:"reputation_tier"`
	AvatarPath     string `db:"avatar_path"`
	// UserJoinedAt and UserPostCount are the author card's two stats — the
	// "Joined" and "Posts" lines every forum shows beside a post, and the
	// cheapest signal a reader has for how much weight to give an answer.
	//
	// UserJoinedAt comes off user_display (the host's view, which is what keeps
	// this plugin off the users table); UserPostCount is a batched second query
	// in GetForumPosts, one row per AUTHOR rather than a correlated subquery
	// per post.
	//
	// Both are zero for a store that does not populate them — the in-memory one
	// does not — and the template renders each line only when it has a value,
	// so a host on the memory store gets an author card without stats rather
	// than one claiming everybody joined in year 1.
	UserJoinedAt  time.Time  `db:"user_joined_at"`
	UserPostCount int        `db:"-"`
	Body          string     `db:"body"`
	EditedAt      *time.Time `db:"edited_at"`
	CreatedAt     time.Time  `db:"created_at"`

	// Quote-reply: when set, the post is a reply to another post and
	// the UI shows a small "in reply to @user" snippet at the top.
	// Populated by GetForumPosts via a left join — nil when this post
	// doesn't quote anything or when the quoted post has been deleted
	// (FK is ON DELETE SET NULL so the reply survives the deletion).
	QuotedPostID      *int64  `db:"quoted_post_id"`
	QuotedUsername    *string `db:"quoted_username"`
	QuotedBodyExcerpt *string `db:"quoted_body_excerpt"`

	// Reactions: populated by GetForumPosts as a second batch query.
	// Counts is "all reactions on this post by emoji"; MyReactions is
	// the subset the viewing user has added (so the UI can highlight
	// the buttons they've already clicked).
	Reactions   []ForumReactionCount `db:"-"`
	MyReactions []string             `db:"-"`
}

// ForumReactionCount is one emoji's tally on a single post.
type ForumReactionCount struct {
	Emoji string `db:"emoji"`
	Count int    `db:"count"`
}
