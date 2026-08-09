package releasegroups

import (
	"context"
	"errors"
	"html/template"

	"github.com/gin-gonic/gin"
)

// The group tables stay host-owned — the host's review-vote claim
// resolution, release-page owner badges, profile pages, and tag service all
// read them — so the store crosses as an adapted interface and the claim /
// archive-scrape / logo machinery (shared with the host's admin surface)
// crosses as functions. One copy of every rule, on the side that owns it.

// GroupStore is the plugin's slice of the host's release-group repository —
// the 30 methods the handlers and jobs actually call, out of the host's 50.
// Method names deliberately match the host repository (the requests-lift
// convention: identical names keep a lift type-level, and then they are the
// contract).
type GroupStore interface {
	// Directory + detail
	GetReleaseGroupBySlug(ctx context.Context, slug string) (*Group, error)
	ListReleaseGroups(ctx context.Context, status, query string, limit, offset int) ([]*Group, int, error)
	CountReleaseGroupsByStatus(ctx context.Context, status string) (int, error)

	// Suggestions
	CreateReleaseGroupSuggestion(ctx context.Context, sug *Suggestion) (int, error)

	// Claims
	CreateReleaseGroupClaim(ctx context.Context, c *Claim) (int64, error)
	GetUserPendingClaimForGroup(ctx context.Context, groupID, userID int) (*Claim, error)
	GetReleaseGroupSlugByClaimToken(ctx context.Context, token string) (string, error)

	// Owners + followers
	GetReleaseGroupOwners(ctx context.Context, groupID int) ([]*Owner, error)
	IsReleaseGroupOwner(ctx context.Context, groupID, userID int) (bool, error)
	FollowReleaseGroup(ctx context.Context, groupID, userID int) (bool, error)
	UnfollowReleaseGroup(ctx context.Context, groupID, userID int) (bool, error)
	IsUserFollowingReleaseGroup(ctx context.Context, groupID, userID int) (bool, error)
	CountReleaseGroupFollowers(ctx context.Context, groupID int) (int, error)
	ListReleaseGroupFollowerIDs(ctx context.Context, groupID int) ([]int, error)

	// News
	CreateReleaseGroupNewsPost(ctx context.Context, p *NewsPost) (int64, error)
	ListReleaseGroupNews(ctx context.Context, groupID, limit, offset int) ([]*NewsPost, int, error)
	GetReleaseGroupNewsPost(ctx context.Context, id int64) (*NewsPost, error)
	SoftDeleteReleaseGroupNewsPost(ctx context.Context, id int64) error

	// Bio
	SetReleaseGroupBio(ctx context.Context, groupID int, markdown string) error

	// Archive
	ListExternalReleaseGroupTorrents(ctx context.Context, groupID, limit, offset int) ([]*ArchiveTorrent, int, error)
	HideExternalReleaseGroupTorrent(ctx context.Context, groupID int, nekobtTorrentID string, byUserID int, reason string) (int64, error)

	// Scraper + sweep (worker leg)
	MergeScrapedReleaseGroup(ctx context.Context, in ScrapedGroup) (int, bool, error)
	SetReleaseGroupNekobtID(ctx context.Context, groupID int, nekobtID string) error
	SetReleaseGroupNekobtStatus(ctx context.Context, groupID int, status string) error
	MarkConfirmedGroupsNotFoundOnNekobt(ctx context.Context, seenSlugs []string) (int64, error)
	RefreshReleaseGroupNzbCounts(ctx context.Context) error
	ListGroupsForArchiveSweep(ctx context.Context) ([]*Group, error)
}

// Verify outcomes the claim flow distinguishes. The host's verifier maps its
// own sentinels onto these at the seam.
var (
	ErrVerifyTooSoon       = errors.New("releasegroups: verification attempted too soon")
	ErrVerifyTokenNotFound = errors.New("releasegroups: verification token not found on the page")
)

// Deps are the web-leg host seams.
type Deps struct {
	Groups GroupStore

	// CachedStat reads one hourly stat_cache value; ok=false falls back to a
	// live count (the original, slower path — still correct).
	CachedStat func(ctx context.Context, key string) (int64, bool)
	// NzbIDByInfoHash answers "is this hash in the catalog right now"
	// (0 = no live release).
	NzbIDByInfoHash func(ctx context.Context, infoHash string) (int64, error)
	// RequestExistsByInfoHash reports whether an open request already
	// carries the hash.
	RequestExistsByInfoHash func(ctx context.Context, infoHash string) (bool, error)
	// CreateRequest files one download request (the bulk-archive path).
	CreateRequest func(ctx context.Context, r BulkRequest) error
	// Notify inserts one notification row (news fan-out).
	Notify func(ctx context.Context, n Notification) error

	// GroupNzbCards returns one page of the group's releases as
	// host-rendered cards plus the live total.
	GroupNzbCards func(ctx context.Context, groupID int, showNSFW bool, limit, offset int) ([]NzbCard, int, error)

	// Chrome.
	RenderPage       func(c *gin.Context, status int, title string, body template.HTML)
	RenderPagination func(page, totalPages int, baseURL string) template.HTML
	// RenderPaginationParam is the from-total variant with a custom page
	// query param (the detail page's in-tab archive pager uses "tpage" so it
	// doesn't collide with the release pager's "page").
	RenderPaginationParam func(page, pageSize, totalItems int, baseURL, paramName string) template.HTML
	NzbCardCSS            func() template.HTML
	CSRFToken             func(c *gin.Context) string
	// Markdown renders + SANITISES member text (news bodies);
	// RenderBioMarkdown is the host's bio-policy renderer (goldmark +
	// bio allow-list, bounded). Both cross so each allow-list keeps one copy.
	Markdown          func(s string) template.HTML
	RenderBioMarkdown func(s string) template.HTML
	// RelativeTime is the site's wording for "3 days ago" — named so every
	// surface phrases it identically.
	RelativeTime func(v any) string

	Viewer func(c *gin.Context) *Viewer

	// BaseURL builds absolute claim-verification links (the fragment shows
	// the backlink the claimant must publish).
	BaseURL string

	// The claim-verification machinery, host-owned (its scraper and rules
	// are shared with the admin review surface).
	NewClaimToken func() (string, error)
	// Backlink returns the exact substring the verifier will look for on
	// the claimant's page — host-owned so claim UI and verifier can never
	// drift.
	Backlink func(slug string, userID int) string
	// VerifyClaim scrapes the claim's URL for the token + backlink and
	// resolves the claim on success. Returns ErrVerifyTooSoon /
	// ErrVerifyTokenNotFound (mapped by the host) for the flows the UI
	// distinguishes.
	VerifyClaim func(ctx context.Context, claim *Claim, expectedBacklink string) error

	// ScrapeArchive pulls one group's external archive from its configured
	// upstream (nekoBT and/or nyaa). Host-owned: the same scraper serves the
	// admin surface, and it needs repo methods beyond this plugin's slice.
	ScrapeArchive func(ctx context.Context, groupID int) (int, error)
}

func (d *Deps) ok() bool {
	return d != nil && d.Groups != nil &&
		d.CachedStat != nil && d.NzbIDByInfoHash != nil &&
		d.RequestExistsByInfoHash != nil && d.CreateRequest != nil && d.Notify != nil &&
		d.GroupNzbCards != nil &&
		d.RenderPage != nil && d.RenderPagination != nil && d.RenderPaginationParam != nil &&
		d.NzbCardCSS != nil && d.CSRFToken != nil &&
		d.Markdown != nil && d.RenderBioMarkdown != nil && d.RelativeTime != nil &&
		d.Viewer != nil && d.BaseURL != "" &&
		d.NewClaimToken != nil && d.Backlink != nil && d.VerifyClaim != nil &&
		d.ScrapeArchive != nil
}

var deps *Deps

// SetDeps hands the plugin its web-leg seams. Called once, before core.Boot,
// in web/all processes.
func SetDeps(d Deps) { deps = &d }

// JobDeps are the worker-leg seams for the two loops.
type JobDeps struct {
	Groups GroupStore
	// FetchLogo downloads + re-encodes a group logo into the host's static
	// tree, returning the local URL. Host-owned (image re-encode stack).
	FetchLogo func(ctx context.Context, logoURL, slugForFilename string) (string, error)
	// Slugify is the host's canonical group-name → slug keying rule.
	Slugify func(name string) string
	// ScrapeArchive — same seam as the web leg's on-demand refresh.
	ScrapeArchive func(ctx context.Context, groupID int) (int, error)
}

func (d *JobDeps) ok() bool {
	return d != nil && d.Groups != nil && d.FetchLogo != nil &&
		d.Slugify != nil && d.ScrapeArchive != nil
}

var jobDeps *JobDeps

// SetJobDeps hands the plugin its worker-leg seams. Called once, before
// core.Boot, in worker/all processes.
func SetJobDeps(d JobDeps) { jobDeps = &d }
