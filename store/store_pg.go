package store

import (
	"context"

	"github.com/jmoiron/sqlx"
)

const itemCols = `id, name, description, points_cost, reward_type, reward_ref, reward_days, stock, active, sort_order, created_at`

// PGStore is the Postgres impl of Store over the dedicated "store"
// schema (store.items / store.purchases), created by
// core.RunPluginMigrations at boot. Self-contained: built at Provision
// from Core.Storage.DB(). The queries schema-qualify every table because
// the shared pool runs with the default (public) search_path — never
// SET search_path on a pooled connection.
type PGStore struct {
	db *sqlx.DB
}

func NewPGStore(db *sqlx.DB) *PGStore { return &PGStore{db: db} }

func (r *PGStore) ListItems(ctx context.Context, activeOnly bool) ([]*Item, error) {
	var rows []*Item
	q := `SELECT ` + itemCols + ` FROM store.items`
	if activeOnly {
		q += ` WHERE active`
	}
	q += ` ORDER BY sort_order, id`
	err := r.db.SelectContext(ctx, &rows, q)
	return rows, err
}

func (r *PGStore) GetItem(ctx context.Context, id int) (*Item, error) {
	var it Item
	err := r.db.QueryRowxContext(ctx,
		`SELECT `+itemCols+` FROM store.items WHERE id=$1`, id).StructScan(&it)
	return &it, err
}

func (r *PGStore) CreateItem(ctx context.Context, it *Item) error {
	return r.db.QueryRowxContext(ctx,
		`INSERT INTO store.items (name, description, points_cost, reward_type, reward_ref, reward_days, stock, active, sort_order)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 RETURNING id, created_at`,
		it.Name, it.Description, it.PointsCost, it.RewardType, it.RewardRef,
		it.RewardDays, it.Stock, it.Active, it.SortOrder,
	).Scan(&it.ID, &it.CreatedAt)
}

func (r *PGStore) UpdateItem(ctx context.Context, it *Item) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE store.items
		    SET name=$2, description=$3, points_cost=$4, reward_type=$5, reward_ref=$6,
		        reward_days=$7, stock=$8, active=$9, sort_order=$10
		  WHERE id=$1`,
		it.ID, it.Name, it.Description, it.PointsCost, it.RewardType, it.RewardRef,
		it.RewardDays, it.Stock, it.Active, it.SortOrder)
	return err
}

func (r *PGStore) DeleteItem(ctx context.Context, id int) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM store.items WHERE id=$1`, id)
	return err
}

// ClaimStock atomically takes one unit. The WHERE guard (stock < 0 OR
// stock > 0) means unlimited items always match and limited items only
// match while units remain, so two concurrent buyers can't both claim
// the last one. rows-affected = 0 means sold out.
func (r *PGStore) ClaimStock(ctx context.Context, itemID int) (bool, error) {
	res, err := r.db.ExecContext(ctx,
		`UPDATE store.items
		    SET stock = stock - 1
		  WHERE id=$1 AND (stock < 0 OR stock > 0)`, itemID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// RestoreStock returns a claimed unit; the WHERE stock >= 0 guard keeps
// unlimited items (stock = -1) untouched so the compensation is a no-op
// for them.
func (r *PGStore) RestoreStock(ctx context.Context, itemID int) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE store.items SET stock = stock + 1 WHERE id=$1 AND stock >= 0`, itemID)
	return err
}

func (r *PGStore) RecordPurchase(ctx context.Context, userID, itemID, pointsSpent int) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO store.purchases (user_id, item_id, points_spent) VALUES ($1,$2,$3)`,
		userID, itemID, pointsSpent)
	return err
}
