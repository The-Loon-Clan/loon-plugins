// Package wiki is the site's knowledge base — topics containing
// long-form markdown posts (Help, FAQ, contributor guides). It is
// the FIRST real pkg/core plugin (PLUGIN-ARCHITECTURE.md § 5's
// POC): everything the wiki needs — models, storage, handlers,
// routes — lives in this one package, wired to the host through
// the Core mediator at Provision time plus a small set of exported
// web/handlers helpers (BaseData for template chrome,
// RenderWikiMarkdown for the shared markdown pipeline).
//
// NOT in this package: the wiki-EDIT review framework
// (wiki_edits / wiki_edit_changes — community metadata corrections
// on releases and the catalog). That is a cross-entity moderation
// surface owned by the host; see storage.WikiEditRepository.
//
// Tables (wiki_topics, wiki_posts) currently live in the public
// schema via the numbered core migrations — the move to a
// dedicated `wiki` schema is deferred to the PG17 baseline
// consolidation (MIGRATION-CONSOLIDATION.md), at which point
// Metadata.Migrations takes over.
package wiki

import "time"

// Topic is one folder in the knowledge base.
type Topic struct {
	ID          int       `db:"id"`
	Name        string    `db:"name"`
	Slug        string    `db:"slug"`
	Description string    `db:"description"`
	SortOrder   int       `db:"sort_order"`
	CreatedAt   time.Time `db:"created_at"`
	PostCount   int       `db:"post_count"` // populated by join query
}

// Post is one article. ViewCount is bumped best-effort on every
// GET /wiki/:topic/:post; queries that omit the column leave the
// zero value.
type Post struct {
	ID        int       `db:"id"`
	TopicID   int       `db:"topic_id"`
	Title     string    `db:"title"`
	Slug      string    `db:"slug"`
	Content   string    `db:"content"`
	CreatedBy int       `db:"created_by"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
	ViewCount int64     `db:"view_count"`
}

// RecentPost is the denormalised row shape behind the landing
// page's "Recent posts" / "Popular Articles" panels and the
// Random shortcut. Carries topic name + slug + author username so
// the template renders `[topic] post-title by <user> · N views`
// rows without a second lookup per post.
type RecentPost struct {
	ID                int       `db:"id"`
	TopicID           int       `db:"topic_id"`
	Title             string    `db:"title"`
	Slug              string    `db:"slug"`
	CreatedAt         time.Time `db:"created_at"`
	UpdatedAt         time.Time `db:"updated_at"`
	TopicName         string    `db:"topic_name"`
	TopicSlug         string    `db:"topic_slug"`
	CreatedByUsername string    `db:"created_by_username"`
	ViewCount         int64     `db:"view_count"`
}
