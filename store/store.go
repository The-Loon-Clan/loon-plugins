package store

import "context"

// Store is the store plugin's data surface (store_items / store_purchases
// in the public schema). The concrete impl is *PGStore, built at
// Provision from Core.Storage.DB().
type Store interface {
	// ListItems returns the catalog ordered by sort_order; activeOnly
	// filters to purchasable items (the user-facing shop).
	ListItems(ctx context.Context, activeOnly bool) ([]*Item, error)
	GetItem(ctx context.Context, id int) (*Item, error)
	CreateItem(ctx context.Context, it *Item) error
	UpdateItem(ctx context.Context, it *Item) error
	DeleteItem(ctx context.Context, id int) error

	// ClaimStock atomically decrements a limited item's stock and
	// reports whether a unit was available (always true for unlimited
	// stock = -1). Call BEFORE debiting points so two buyers can't
	// oversell the last unit; pair with RestoreStock on any later
	// failure.
	ClaimStock(ctx context.Context, itemID int) (bool, error)
	// RestoreStock returns a claimed unit to a limited item — the
	// compensation when a purchase fails after ClaimStock.
	RestoreStock(ctx context.Context, itemID int) error

	RecordPurchase(ctx context.Context, userID, itemID, pointsSpent int) error
}
