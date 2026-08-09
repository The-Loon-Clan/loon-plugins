package releasegroups

import (
	"html/template"
	"time"
)

// Full mirrors of the host rows that cross the seam — complete rather than
// narrowed, because the five templates read more fields than the handlers do
// and a missing field is a render-time error (see the requests plugin, same
// rule, same reason).

// Group is one release-group row (host: release_groups).
type Group struct {
	ID             int
	Name           string
	Slug           string
	Status         string // 'unknown' | 'confirmed'
	Source         string // 'auto' | 'manual' | 'scraped'
	WebsiteURL     string
	Description    string
	BioMarkdown    string
	LogoURL        string
	NzbCountCached int
	ManuallyEdited bool
	CreatedBy      *int
	CreatedAt      time.Time
	UpdatedAt      time.Time

	ArchiveLastRefreshAt      *time.Time
	NekobtGroupID             string
	NekobtStatus              string // 'unchecked' | 'linked' | 'not_found'
	ArchiveTorrentCountCached int
	ScrapeURL                 string
	ScrapeSource              string // '' | 'nekobt' | 'nyaa'
}

// Archive-source values for Group.ScrapeSource.
const (
	ArchiveSourceUnset  = ""
	ArchiveSourceNekobt = "nekobt"
	ArchiveSourceNyaa   = "nyaa"
)

// ArchiveTorrent is one externally-mirrored torrent row (host:
// external_release_group_torrents).
type ArchiveTorrent struct {
	NekobtTorrentID string
	ReleaseGroupID  int
	OurNzbID        *int64
	Title           string
	FilesizeBytes   int64
	InfoHash        string
	UploadedAt      time.Time
	Seeders         int
	Leechers        int
	Completed       int
	AudioLang       string
	SubLang         string
	FsubLang        string
	Level           int
	VideoCodec      int
	ImportedNyaaID  *string
	LastSeenAt      time.Time
	CreatedAt       time.Time
	HiddenAt        *time.Time
	HiddenBy        *int
	HiddenReason    string
	CoverURL        string
}

// Claim is one ownership claim (host: release_group_claims).
type Claim struct {
	ID             int64
	ReleaseGroupID int
	UserID         int
	Username       string
	Role           string // 'owner' | 'maintainer'
	ClaimMessage   string
	Status         string // 'pending' | 'approved' | 'rejected'
	ApprovedBy     *int
	ApprovedAt     *time.Time
	RejectedReason string
	CreatedAt      time.Time

	VerificationToken         string
	VerificationURL           string
	VerifiedAt                *time.Time
	VerificationAttempts      int
	LastVerificationAttemptAt *time.Time

	GroupName string
	GroupSlug string
}

const (
	RoleOwner      = "owner"
	RoleMaintainer = "maintainer"
)

// Owner is one approved owner/maintainer row.
type Owner struct {
	ID             int64
	ReleaseGroupID int
	UserID         int
	Username       string
	Role           string
	ClaimID        *int64
	CreatedAt      time.Time
}

// NewsPost is one group news post.
type NewsPost struct {
	ID             int64
	ReleaseGroupID int
	AuthorUserID   int
	AuthorName     string
	Title          string
	Body           string
	DeletedAt      *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Suggestion is one member-filed edit/new-group suggestion. The host's admin
// review queue consumes these rows; the plugin only writes them.
type Suggestion struct {
	ID          int
	GroupID     *int
	UserID      int
	Username    string
	Name        string
	WebsiteURL  string
	Description string
	LogoURL     string
	Notes       string
	Status      string
	ReviewedBy  *int
	ReviewedAt  *time.Time
	CreatedAt   time.Time
}

// ScrapedGroup is the scraper's merge input.
type ScrapedGroup struct {
	Name        string
	WebsiteURL  string
	Description string
	LogoURL     string
}

// NzbCard is one release on the detail page, pre-rendered by the host — the
// site's release card reads most of a release record and belongs to the
// host, so the host draws it and this carries the result (the lists-plugin
// pattern).
type NzbCard struct {
	Card template.HTML
}

// Notification is the follower fan-out payload.
type Notification struct {
	UserID    int
	Type      string
	ActorID   *int
	ActorName string
	Title     string
	Link      string
}

// NotificationTypeNews mirrors the host's notification vocabulary for group
// news — the /notifications template keys its icon on it.
const NotificationTypeNews = "release_group_news"

// BulkRequest is one archive row turned into a download request.
type BulkRequest struct {
	UserID    int
	Username  string
	Title     string
	Category  string
	NyaaURL   string
	InfoHash  string
	SeedCount int
	SizeBytes *int64
}

// Viewer is who is looking at the page; nil for anonymous. Mod is the one
// authority level this surface gates on (owners are data, not a role);
// what maps to it is the host's decision. ShowNSFW is the viewer's browse
// preference, applied to the group's release listing.
type Viewer struct {
	ID       int
	Username string
	Mod      bool
	ShowNSFW bool
}
