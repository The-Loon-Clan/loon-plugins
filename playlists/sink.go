package playlists

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The collection sink — where the host's cart empties.
//
// The host lets a member tick rows across a listing and accumulate them; one of
// the things it can then do with them is put them in a collection, without
// knowing that collections are playlists or that this plugin exists. This is
// that seam (pluginapi.CollectionSink), and it is deliberately the narrowest
// one that works: name the member's own collections, and take a batch.

type sink struct{ p *Plugin }

var _ pluginapi.CollectionSink = sink{}

// CollectionsOf lists the playlists this member may add to.
//
// THEIR OWN ONLY, and that is the access control rather than a convenience: the
// host renders whatever comes back as choices, so anything listed here is
// something the member is being invited to write to. A public playlist belonging
// to somebody else is readable and must not be in this list.
func (s sink) CollectionsOf(ctx context.Context, userID int64) ([]pluginapi.Collection, error) {
	if userID <= 0 {
		return nil, nil
	}
	var rows []struct {
		Slug  string `db:"slug"`
		Name  string `db:"name"`
		Count int    `db:"n"`
	}
	err := s.p.pg.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, `
			SELECT p.slug, p.name,
			       (SELECT count(*) FROM items i WHERE i.playlist_id = p.id)::int AS n
			  FROM playlists p
			 WHERE p.user_id = $1
			 ORDER BY p.updated_at DESC`, userID)
	})
	if err != nil {
		return nil, fmt.Errorf("playlists: collections of: %w", err)
	}
	out := make([]pluginapi.Collection, 0, len(rows))
	for _, r := range rows {
		out = append(out, pluginapi.Collection{Slug: r.Slug, Name: r.Name, Count: r.Count})
	}
	return out, nil
}

// AddToCollection puts a batch of releases into one playlist.
//
// ONE STATEMENT for the whole batch rather than a loop over AddItem, because
// the caller is a cart and a cart holds forty things — forty round trips, each
// re-reading MAX(position), is a page load somebody notices. The positions come
// from the same generated series, so the batch lands in the order it was ticked.
//
// The ownership check is IN the statement: the INSERT selects the playlist by
// slug AND user_id, so a slug belonging to somebody else matches no row and
// writes nothing. There is no separate read to race against.
func (s sink) AddToCollection(ctx context.Context, userID int64, slug string, releaseIDs []int64) (int, error) {
	if userID <= 0 || slug == "" || len(releaseIDs) == 0 {
		return 0, nil
	}
	var n int64
	err := s.p.pg.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			WITH target AS (
				SELECT id FROM playlists WHERE slug = $2 AND user_id = $1
			), base AS (
				SELECT COALESCE(MAX(i.position) + 1, 0) AS pos
				  FROM items i JOIN target t ON i.playlist_id = t.id
			), incoming AS (
				SELECT rid, row_number() OVER () - 1 AS n
				  FROM unnest($3::bigint[]) AS rid
			)
			INSERT INTO items (playlist_id, release_id, position, note)
			SELECT t.id, i.rid, b.pos + i.n, ''
			  FROM target t, base b, incoming i
			-- Already there is not an error and not a second copy: a member who
			-- ticks something they saved last week should simply keep the one
			-- they have, which is why the count returned is what was ADDED.
			ON CONFLICT (playlist_id, release_id) DO NOTHING`,
			userID, slug, pq.Array(releaseIDs))
		if err != nil {
			return err
		}
		if n, err = res.RowsAffected(); err != nil {
			return err
		}
		// Only when something actually landed. Bumping updated_at for a batch
		// that was entirely duplicates would push the playlist to the top of
		// the index for a change that did not happen.
		if n > 0 {
			_, err = tx.ExecContext(ctx,
				`UPDATE playlists SET updated_at = now() WHERE slug = $1 AND user_id = $2`,
				slug, userID)
		}
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("playlists: add %d to %q: %w", len(releaseIDs), slug, err)
	}
	return int(n), nil
}
