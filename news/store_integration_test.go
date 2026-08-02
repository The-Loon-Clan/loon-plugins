//go:build integration

package news

import (
	"context"
	"os"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

// news_posts belongs to the host, so this creates it rather than assuming a
// migrated database — the plugin ships no migration of its own and the table
// is four columns and a unique index.
func testStore(t *testing.T) *PGStore {
	t.Helper()
	dsn := os.Getenv("NEWS_TEST_DSN")
	if dsn == "" {
		dsn = os.Getenv("USENET_TEST_DSN")
	}
	if dsn == "" {
		t.Skip("set NEWS_TEST_DSN (or USENET_TEST_DSN) to run the news integration tests")
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS news_posts (
			id         BIGSERIAL PRIMARY KEY,
			title      TEXT NOT NULL,
			slug       TEXT NOT NULL UNIQUE,
			body       TEXT NOT NULL DEFAULT '',
			published  BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`TRUNCATE news_posts RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return NewPGStore(db)
}

// An unpublished post must not be reachable by slug.
//
// It was: the public /news/:slug page is the only caller and the query had no
// published filter, so anyone who knew or guessed a slug could read a draft an
// admin was still writing. That was already wrong; it is load-bearing now that
// an agent can create drafts, because the entire propose tier rests on a draft
// being inert until a human approves it.
func TestUnpublishedPostIsNotReachableBySlug(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	draft, err := s.CreateNewsPost(ctx, "Weekly update (draft)", "weekly-update", "not ready", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetNewsPostBySlug(ctx, "weekly-update"); err == nil {
		t.Fatal("an unpublished post was served by slug — a draft is not inert")
	}

	// Publishing the same row makes it reachable, or the filter would just be
	// breaking the page.
	if err := s.UpdateNewsPost(ctx, draft.ID, draft.Title, draft.Slug, draft.Body, true); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNewsPostBySlug(ctx, "weekly-update")
	if err != nil {
		t.Fatalf("a published post is not reachable by slug: %v", err)
	}
	if got.ID != draft.ID || !got.Published {
		t.Errorf("got %+v, want the published row", got)
	}
}

// The admin path reads by ID and must keep seeing drafts — that is where a
// human reviews one.
func TestAdminReadsStillSeeDrafts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	draft, err := s.CreateNewsPost(ctx, "Draft", "draft-post", "body", false)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetNewsPostByID(ctx, draft.ID); err != nil || got.Published {
		t.Errorf("admin read by ID lost the draft: %+v err=%v", got, err)
	}
	all, err := s.GetAllNewsPosts(ctx)
	if err != nil || len(all) != 1 {
		t.Errorf("admin listing returned %d post(s), err=%v", len(all), err)
	}
	// And the public listing must not carry it.
	pub, err := s.GetPublishedNewsPosts(ctx, 10)
	if err != nil || len(pub) != 0 {
		t.Errorf("public listing carried %d draft(s), err=%v", len(pub), err)
	}
}
