package playlists

import "time"

// Playlist is one user-curated collection of releases.
type Playlist struct {
	ID          int64     `db:"id"`
	UserID      int64     `db:"user_id"`
	Slug        string    `db:"slug"`
	Name        string    `db:"name"`
	Description string    `db:"description"`
	CoverURL    string    `db:"cover_url"`
	Public      bool      `db:"public"`
	CreatedAt   time.Time `db:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"`

	// Joined view columns, populated by the list/detail queries and zero
	// otherwise. Username comes from the HOST via Deps.LookupUsername rather
	// than a join, because a portable plugin cannot assume the shape of a
	// host's users table — that assumption is exactly what leaves a plugin
	// unwirable on a host whose column types differ.
	Username  string `db:"-"`
	ItemCount int    `db:"item_count"`
}

// Item is one release in a playlist.
//
// Release is filled at READ time from the host's index via Deps.LookupReleases
// and is nil when that release no longer exists — retention removes releases,
// and a collection outliving its contents is normal rather than exceptional.
// Templates must handle a nil Release; see the unavailable branch.
type Item struct {
	ID         int64     `db:"id"`
	PlaylistID int64     `db:"playlist_id"`
	ReleaseID  int64     `db:"release_id"`
	Position   int       `db:"position"`
	Note       string    `db:"note"`
	AddedAt    time.Time `db:"added_at"`

	Release *Release `db:"-"`
}

// Release is the subset of a host's release a playlist needs to render a row.
//
// Deliberately NOT the host's own release type: this plugin must not import a
// host's models, and these five fields are the whole surface. The host maps its
// own shape into this in Deps.LookupReleases.
type Release struct {
	ID       int64
	Title    string
	Size     string // already human-readable; the host owns the formatting
	Category string
	CoverURL string
}
