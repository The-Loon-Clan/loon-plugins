package news

import "time"

// NewsPost represents a site news article.
type NewsPost struct {
	ID        int64     `db:"id"`
	Title     string    `db:"title"`
	Slug      string    `db:"slug"`
	Body      string    `db:"body"`
	Published bool      `db:"published"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}
