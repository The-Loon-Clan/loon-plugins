package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

// Handlers serves the /store buy flow and the /admin/store catalog.
type Handlers struct {
	// auth resolves the viewer. Only the id is ever used, which is why this is
	// core.Auth rather than a host session helper.
	auth    core.AuthService
	store   Store
	points  core.PointsService
	granter pluginapi.RankGranter
	// invites may be nil: it is published by the host, not a plugin, so it
	// cannot be declared in Metadata.Requires and a host without an invite
	// system is legitimate. Only invite items need it — see grantReward.
	invites pluginapi.InviteGranter
	errs    core.ErrorReporter
}

// StorePage lists purchasable items and the viewer's balance.
func (h *Handlers) StorePage(c *gin.Context) {
	ctx := c.Request.Context()
	items, err := h.store.ListItems(ctx, true)
	if err != nil {
		h.errs.Report(ctx, "store/list", err)
	}
	balance := 0
	if user, ok := h.auth.CurrentUser(c); ok && user != nil {
		balance, _ = h.points.Balance(ctx, user.ID)
	}
	c.HTML(http.StatusOK, "store.html", deps.BaseData(c, gin.H{
		"Items":   items,
		"Balance": balance,
		"Error":   c.Query("error"),
		"Ok":      c.Query("ok"),
	}))
}

// historyPageSize is the ledger rows per page on /store/history.
//
// Not a config knob: it is a layout choice for one table, not an operational
// one — nothing about throughput or cost changes with it, and the paging nav
// makes the rest reachable.
const historyPageSize = 25

// HistoryPage renders the viewer's own points ledger.
//
// This lived on /profile, where the host handler read storage directly. The
// store is the surface that spends points, so it is where a user looks to see
// what they spent — and the host page had grown a Points card that pulled
// ranks, the invite cost and the ledger into a template that is otherwise
// about the account.
//
// The ledger arrives through core.PointsService.History, the same facade the
// purchase path deducts through, so the plugin still owns no host tables.
func (h *Handlers) HistoryPage(c *gin.Context) {
	ctx := c.Request.Context()
	user, ok := h.auth.CurrentUser(c)
	if !ok || user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	offset := deps.PageOffset(page, historyPageSize)

	entries, total, err := h.points.History(ctx, user.ID, historyPageSize, offset)
	if err != nil {
		// The ledger is the whole page; an empty table here would read as
		// "you have never earned a point", which is a plausible lie.
		h.errs.Report(ctx, "store/history", err)
		c.HTML(http.StatusOK, "store_history.html", deps.BaseData(c, gin.H{
			"Error": "Could not load your points history. Try again shortly.",
		}))
		return
	}
	balance, _ := h.points.Balance(ctx, user.ID)

	c.HTML(http.StatusOK, "store_history.html", deps.BaseData(c, gin.H{
		"Entries":    entries,
		"Total":      total,
		"Balance":    balance,
		"Pagination": deps.Paginate(page, historyPageSize, total, "/store/history"),
	}))
}

// errOutOfStock is returned by purchase when the item sold out between
// page render and the buy click.
var errOutOfStock = errors.New("store: out of stock")

// BuyItem is the HTTP glue around purchase: resolve the session user and
// item, run the transaction, and map its outcome to a redirect.
func (h *Handlers) BuyItem(c *gin.Context) {
	user, ok := h.auth.CurrentUser(c)
	if !ok || user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()
	itemID, _ := strconv.Atoi(c.Param("id"))
	item, err := h.store.GetItem(ctx, itemID)
	if err != nil {
		c.Redirect(http.StatusFound, "/store?error=item+not+found")
		return
	}
	if !item.Active {
		c.Redirect(http.StatusFound, "/store?error=item+unavailable")
		return
	}

	reward, err := h.purchase(ctx, int(user.ID), item)
	switch {
	case err == nil:
		c.Redirect(http.StatusFound, "/store?ok="+neturl.QueryEscape(reward))
	case errors.Is(err, errOutOfStock):
		c.Redirect(http.StatusFound, "/store?error=out+of+stock")
	case errors.Is(err, core.ErrInsufficientPoints):
		c.Redirect(http.StatusFound, "/store?error=not+enough+points")
	default:
		// purchase already logged the specifics via h.errs.Report.
		c.Redirect(http.StatusFound, "/store?error=purchase+failed")
	}
}

// purchase runs the buy transaction and returns the granted reward's
// label. The steps run in an order that keeps the economy consistent
// without a cross-service transaction: claim stock first (so we never
// charge for a sold-out item), then debit points, then grant — unwinding
// prior steps (restore stock, refund points) on any later failure.
func (h *Handlers) purchase(ctx context.Context, userID int, item *Item) (string, error) {
	// 1) Claim a unit atomically (no-op success for unlimited stock).
	claimed, err := h.store.ClaimStock(ctx, item.ID)
	if err != nil {
		h.errs.Report(ctx, "store/claim-stock", err)
		return "", err
	}
	if !claimed {
		return "", errOutOfStock
	}

	// 2) Debit points. Insufficient balance is a normal outcome, not an
	// error to log — restore the claimed unit and surface it as-is.
	if _, err := h.points.Deduct(ctx, int64(userID), item.PointsCost, "spend_store_purchase",
		fmt.Sprintf("Bought %q from the store", item.Name), int64(item.ID)); err != nil {
		_ = h.store.RestoreStock(ctx, item.ID)
		if !errors.Is(err, core.ErrInsufficientPoints) {
			h.errs.Report(ctx, "store/deduct", err)
		}
		return "", err
	}

	// 3) Grant the reward. On failure, unwind BOTH prior steps so the
	// user keeps their points and the unit returns to stock.
	reward, err := h.grantReward(ctx, userID, item)
	if err != nil {
		h.errs.Report(ctx, "store/grant", err)
		if _, err := h.points.Refund(ctx, int64(userID), item.PointsCost, "refund_store_purchase",
			fmt.Sprintf("Refund: %q could not be granted", item.Name), int64(item.ID)); err != nil {
			h.errs.Report(ctx, "store/refund", err)
		}
		_ = h.store.RestoreStock(ctx, item.ID)
		return "", err
	}

	// 4) Record the sale in the store's own ledger. The economic
	// transaction already completed (points spent, reward granted), so a
	// failure here is logged loudly but not rolled back — undoing a
	// granted rank would be worse than a missing audit row.
	if err := h.store.RecordPurchase(ctx, userID, item.ID, item.PointsCost); err != nil {
		h.errs.Report(ctx, "store/record-purchase", err)
	}
	return reward, nil
}

// grantReward dispatches on the item's reward type. Adding a reward
// (points bonus, freeleech, invites) is one new case here plus its
// capability lookup in Provision.
func (h *Handlers) grantReward(ctx context.Context, userID int, item *Item) (string, error) {
	switch RewardType(item.RewardType) {
	case RewardRank:
		rankID, err := strconv.Atoi(item.RewardRef)
		if err != nil || rankID <= 0 {
			return "", fmt.Errorf("rank item %d has invalid reward_ref %q", item.ID, item.RewardRef)
		}
		name, err := h.granter.GrantRank(ctx, userID, rankID, time.Duration(item.RewardDays)*24*time.Hour)
		if err != nil {
			return "", fmt.Errorf("grant rank %d: %w", rankID, err)
		}
		return name + " rank", nil
	case RewardInvite:
		// Absent capability is a purchase-time failure, not a boot-time one:
		// a host with no invite system is legitimate, and this only matters
		// if someone put an invite item in the catalog. The caller unwinds
		// the points, so the user is made whole either way.
		if h.invites == nil {
			return "", fmt.Errorf("invite item %d: no %s registered on this host",
				item.ID, pluginapi.InviteGranterName)
		}
		// Unset/garbage reward_ref means one invite — the only quantity this
		// has ever been sold in, and a mis-typed catalog row should not hand
		// out zero invites for real points.
		n := 1
		if item.RewardRef != "" {
			if parsed, err := strconv.Atoi(item.RewardRef); err == nil && parsed > 0 {
				n = parsed
			}
		}
		label, err := h.invites.GrantInvites(ctx, userID, n)
		if err != nil {
			return "", fmt.Errorf("grant invites: %w", err)
		}
		return label, nil
	default:
		return "", fmt.Errorf("unknown reward_type %q on item %d", item.RewardType, item.ID)
	}
}

// --- admin catalog ---

func (h *Handlers) AdminStorePage(c *gin.Context) {
	items, err := h.store.ListItems(c.Request.Context(), false)
	if err != nil {
		h.errs.Report(c.Request.Context(), "store/admin-list", err)
	}
	c.HTML(http.StatusOK, "admin_store.html", deps.BaseData(c, gin.H{
		"Items": items,
		"Error": c.Query("error"),
		"Ok":    c.Query("ok") == "1",
	}))
}

func (h *Handlers) CreateItem(c *gin.Context) {
	it := itemFromForm(c)
	if err := validItem(it); err != nil {
		c.Redirect(http.StatusFound, "/admin/store?error="+neturl.QueryEscape(err.Error()))
		return
	}
	if err := h.store.CreateItem(c.Request.Context(), it); err != nil {
		h.errs.Report(c.Request.Context(), "store/create", err)
		c.Redirect(http.StatusFound, "/admin/store?error=create+failed")
		return
	}
	c.Redirect(http.StatusFound, "/admin/store?ok=1")
}

func (h *Handlers) UpdateItem(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	it := itemFromForm(c)
	it.ID = id
	if err := validItem(it); err != nil {
		c.Redirect(http.StatusFound, "/admin/store?error="+neturl.QueryEscape(err.Error()))
		return
	}
	if err := h.store.UpdateItem(c.Request.Context(), it); err != nil {
		h.errs.Report(c.Request.Context(), "store/update", err)
		c.Redirect(http.StatusFound, "/admin/store?error=update+failed")
		return
	}
	c.Redirect(http.StatusFound, "/admin/store?ok=1")
}

func (h *Handlers) DeleteItem(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := h.store.DeleteItem(c.Request.Context(), id); err != nil {
		h.errs.Report(c.Request.Context(), "store/delete", err)
		c.Redirect(http.StatusFound, "/admin/store?error=delete+failed")
		return
	}
	c.Redirect(http.StatusFound, "/admin/store?ok=1")
}

// itemFromForm reads a catalog item off an admin form. Missing/blank
// stock means unlimited (-1).
func itemFromForm(c *gin.Context) *Item {
	cost, _ := strconv.Atoi(c.PostForm("points_cost"))
	days, _ := strconv.Atoi(c.PostForm("reward_days"))
	so, _ := strconv.Atoi(c.PostForm("sort_order"))
	stock := -1
	if s := strings.TrimSpace(c.PostForm("stock")); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			stock = n
		}
	}
	active := c.PostForm("active")
	return &Item{
		Name:        strings.TrimSpace(c.PostForm("name")),
		Description: strings.TrimSpace(c.PostForm("description")),
		PointsCost:  cost,
		RewardType:  strings.TrimSpace(c.PostForm("reward_type")),
		RewardRef:   strings.TrimSpace(c.PostForm("reward_ref")),
		RewardDays:  days,
		Stock:       stock,
		Active:      active == "on" || active == "1" || active == "true",
		SortOrder:   so,
	}
}

// validItem gates creation/update: shared shape checks plus per-reward
// validation so a mis-configured item can't be saved (and later fail at
// buy time).
func validItem(it *Item) error {
	if it.Name == "" {
		return errors.New("name is required")
	}
	if it.PointsCost <= 0 {
		return errors.New("points cost must be positive")
	}
	switch RewardType(it.RewardType) {
	case RewardRank:
		if n, err := strconv.Atoi(it.RewardRef); err != nil || n <= 0 {
			return errors.New("rank reward needs a numeric rank id in reward ref")
		}
		return nil
	case RewardInvite:
		// RewardInvite was declared and documented, and the BUY path already
		// handles it (including the "no InviteGranter registered" case), but
		// this switch had no branch for it — so the item could never be
		// created and that buy path was unreachable.
		//
		// RewardRef is how many invites, and the type's own documentation says
		// "empty or unparseable means 1", so an unreadable value is a default
		// rather than an error. A negative or zero count is still rejected:
		// that is a mis-configured item, not an unstated default.
		if it.RewardRef != "" {
			if n, err := strconv.Atoi(it.RewardRef); err == nil && n <= 0 {
				return errors.New("invite reward count must be positive")
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported reward type %q", it.RewardType)
	}
}
