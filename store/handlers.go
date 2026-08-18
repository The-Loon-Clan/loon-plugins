package store

import (
	"context"
	"errors"
	"fmt"
	"log"
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
	// core is held only to Emit. Nil in tests, so every emit goes through
	// h.emit rather than touching it.
	core *core.Core
	// invites may be nil: it is published by the host, not a plugin, so it
	// cannot be declared in Metadata.Requires and a host without an invite
	// system is legitimate. Only invite items need it — see grantReward.
	invites pluginapi.InviteGranter
	// perks may be nil for the same reason invites may: a host with no tracker
	// economy is legitimate, and only perk items need it.
	perks pluginapi.PerkGranter
	flair pluginapi.FlairGranter
	// credit may be nil on the same terms: only the GB items need the
	// tracker's pluginapi.TrackerCredit, and a site without a running
	// tracker legitimately has none.
	credit pluginapi.TrackerCredit
	// medals may be nil on the same terms again: only medal items need it.
	medals pluginapi.MedalGranter
	// halves reports which site flavours are on (indexer, tracker) — the
	// host's store.flavour seam. Nil means "no flavour machinery": every
	// item shows, which is every pre-flavour host.
	halves func() (indexer, tracker bool)
	errs   core.ErrorReporter
}

// memberSentence returns a refusal's own words, empty for a failure that is
// not the member's to act on. Both sides of the seam can raise one: the
// store's own bounds check and a provider's Grant.
func memberSentence(err error) string {
	var r pluginapi.StoreRefusal
	if errors.As(err, &r) {
		return string(r)
	}
	return ""
}

// widgetFields collects a contributed type's buy control off the form. The
// wire names are prefixed so a field called "amount" can never collide with
// _csrf or with a form key the store adds later.
const widgetPrefix = "f_"

func widgetFields(c *gin.Context) map[string]string {
	form := map[string]string{}
	if c.Request == nil {
		return form
	}
	if err := c.Request.ParseForm(); err != nil {
		return form
	}
	for k, v := range c.Request.PostForm {
		if strings.HasPrefix(k, widgetPrefix) && len(v) > 0 {
			form[strings.TrimPrefix(k, widgetPrefix)] = v[0]
		}
	}
	return form
}

// itemType resolves a contributed reward type. Looked up PER CALL rather than
// cached at Provision: a provider may register in Start (games only offers
// charity where it can find need), and a cache would answer for a plugin that
// registered a moment later. One map read, on a page render and a buy.
func (h *Handlers) itemType(kind string) (pluginapi.StoreItemType, bool) {
	if h.core == nil || builtin(kind) {
		return nil, false
	}
	return pluginapi.LookupStoreItemType(h.core, kind)
}

// typeInfos is the def editor's dropdown: what the store grants itself, then
// what plugins contribute. ctx because a provider describes itself from its
// own settings (charity's bounds are an operator's numbers, not constants).
func (h *Handlers) typeInfos(ctx context.Context) []pluginapi.StoreItemTypeInfo {
	out := append([]pluginapi.StoreItemTypeInfo(nil), builtinTypes...)
	if h.core == nil {
		return out
	}
	for _, t := range pluginapi.StoreItemTypes(h.core) {
		info := t.Describe(ctx, "")
		// A provider colliding with a builtin kind would put two entries with
		// the same value in the dropdown and quietly never be reached by the
		// buy path, which dispatches builtins first. Drop it and say so.
		if info.Kind == "" || builtin(info.Kind) {
			log.Printf("store: ignoring contributed item type %q — it collides with a built-in reward type", info.Kind)
			continue
		}
		out = append(out, info)
	}
	return out
}

// itemAvailable reports whether an item can be sold here: its flavour half is
// on, and — for a contributed type — a provider is present to grant it. An
// item whose plugin is uninstalled is hidden rather than sold, on the same
// reasoning as the flavour filter: taking points for something nothing can
// deliver is worse than an item that is missing.
func (h *Handlers) itemAvailable(it *Item) bool {
	if !builtin(it.RewardType) {
		if _, ok := h.itemType(it.RewardType); !ok {
			return false
		}
	}
	if h.halves == nil {
		return true
	}
	indexer, tracker := h.halves()
	switch it.Flavour {
	case "tracker":
		return tracker
	case "indexer":
		return indexer
	}
	return true
}

// StorePage lists purchasable items and the viewer's balance.
func (h *Handlers) StorePage(c *gin.Context) {
	ctx := c.Request.Context()
	items, err := h.store.ListItems(ctx, true)
	if err == nil {
		// Hide what this site's flavour cannot honour — a GB-of-upload item
		// with no tracker would take points for a number nothing displays.
		kept := items[:0]
		for _, it := range items {
			if h.itemAvailable(it) {
				kept = append(kept, it)
			}
		}
		items = kept
	}
	if err != nil {
		h.errs.Report(ctx, "store/list", err)
	}
	balance := 0
	if user, ok := h.auth.CurrentUser(c); ok && user != nil {
		balance, _ = h.points.Balance(ctx, user.ID)
	}
	// The buy controls, per ITEM rather than per kind: a contributed type
	// shapes its widget from the def's own reward_ref, so two charity items
	// with different bands are two different controls. Absent key = the
	// store's plain Buy button, which is every builtin item.
	widgets := map[int]*pluginapi.StoreItemTypeInfo{}
	for _, it := range items {
		if t, ok := h.itemType(it.RewardType); ok {
			info := t.Describe(ctx, it.RewardRef)
			widgets[it.ID] = &info
		}
	}
	renderPage(c, "Store", "store.html", gin.H{
		"Items":   items,
		"Widgets": widgets,
		"Balance": balance,
		"Error":   c.Query("error"),
		"Ok":      c.Query("ok"),
	})
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
		renderPage(c, "Purchase history", "store_history.html", gin.H{
			"Error": "Could not load your points history. Try again shortly.",
		})
		return
	}
	balance, _ := h.points.Balance(ctx, user.ID)

	renderPage(c, "Purchase history", "store_history.html", gin.H{
		"Entries":        entries,
		"Total":          total,
		"Balance":        balance,
		"PaginationHTML": deps.RenderPagination(page, historyPageSize, total, "/store/history?"),
	})
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
	if !item.Active || !h.itemAvailable(item) {
		// The flavour check matches the listing's: an item the shop hides
		// must refuse a hand-crafted POST too, or the filter is decoration.
		c.Redirect(http.StatusFound, "/store?error=item+unavailable")
		return
	}

	reward, cost, err := h.purchase(ctx, int(user.ID), item, widgetFields(c))
	switch {
	case err == nil:
		// After the purchase committed, and only on the success leg. The
		// out-of-stock and insufficient-points branches below are members who
		// bought nothing, and announcing those would credit them for it.
		if h.core != nil {
			h.core.Emit(ctx, core.Event{
				Name: EventPurchased, UserID: user.ID, Subject: strconv.Itoa(item.ID),
				// Cost is what was actually paid, not the catalog figure: a
				// variable-cost item prices itself from the buyer's own input,
				// and announcing the def's number would report a sale that did
				// not happen to every subscriber counting points spent.
				Data: Purchased{ItemID: item.ID, Name: item.Name, Cost: cost, Reward: reward},
			})
		}
		c.Redirect(http.StatusFound, "/store?ok="+neturl.QueryEscape(reward))
	case errors.Is(err, errOutOfStock):
		c.Redirect(http.StatusFound, "/store?error=out+of+stock")
	case errors.Is(err, core.ErrInsufficientPoints):
		c.Redirect(http.StatusFound, "/store?error=not+enough+points")
	case memberSentence(err) != "":
		// A refusal the buyer can act on — the amount was outside the item's
		// bounds, or a contributed type had nothing to give. Shown as written.
		c.Redirect(http.StatusFound, "/store?error="+neturl.QueryEscape(memberSentence(err)))
	default:
		// purchase already logged the specifics via h.errs.Report.
		c.Redirect(http.StatusFound, "/store?error=purchase+failed")
	}
}

// purchase runs the buy transaction and returns the granted reward's label
// and what it actually cost. The steps run in an order that keeps the economy
// consistent without a cross-service transaction: price the sale, claim stock
// (so we never charge for a sold-out item), then debit points, then grant —
// unwinding prior steps (restore stock, refund points) on any later failure.
//
// fields are a contributed type's buy control, empty for every builtin item.
func (h *Handlers) purchase(ctx context.Context, userID int, item *Item, fields map[string]string) (string, int, error) {
	// 0) Price it, BEFORE anything moves. A builtin item costs what the
	// catalog says; a contributed type may price itself from the member's own
	// input, and its bounds are checked here so a refusal claims no stock and
	// spends no points.
	cost := item.PointsCost
	reason := "spend_store_purchase"
	note := fmt.Sprintf("Bought %q from the store", item.Name)
	var pur *pluginapi.StorePurchase
	if t, ok := h.itemType(item.RewardType); ok {
		info := t.Describe(ctx, item.RewardRef)
		c, resolved, err := pluginapi.PrepareStorePurchase(info, item.PointsCost, fields)
		if err != nil {
			// A wiring bug in the provider's own description is the operator's
			// to fix and invisible to them otherwise — report it. A member's
			// bad amount is neither, and reporting it would fill the log with
			// people typing 10 into a box that starts at 1000.
			if memberSentence(err) == "" {
				h.errs.Report(ctx, "store/describe", err)
			}
			return "", 0, err
		}
		cost = c
		// The receipt keeps the transaction's own meaning: charity bought
		// through the shop still reads as charity in the member's history,
		// under the same ledger code the charity page has always written.
		if info.Reason != "" {
			reason = info.Reason
		}
		if info.LedgerNote != "" {
			note = info.LedgerNote
		}
		pur = &pluginapi.StorePurchase{
			UserID: int64(userID), ItemID: item.ID, Ref: item.RewardRef,
			Days: item.RewardDays, Cost: cost, Fields: resolved,
		}
	}

	// 1) Claim a unit atomically (no-op success for unlimited stock).
	claimed, err := h.store.ClaimStock(ctx, item.ID)
	if err != nil {
		h.errs.Report(ctx, "store/claim-stock", err)
		return "", 0, err
	}
	if !claimed {
		return "", 0, errOutOfStock
	}

	// 2) Debit points. Insufficient balance is a normal outcome, not an
	// error to log — restore the claimed unit and surface it as-is.
	if _, err := h.points.Deduct(ctx, int64(userID), cost, reason, note, int64(item.ID)); err != nil {
		_ = h.store.RestoreStock(ctx, item.ID)
		if !errors.Is(err, core.ErrInsufficientPoints) {
			h.errs.Report(ctx, "store/deduct", err)
		}
		return "", 0, err
	}

	// 3) Grant the reward. On failure, unwind BOTH prior steps so the
	// user keeps their points and the unit returns to stock.
	reward, err := h.grantReward(ctx, userID, item, pur)
	if err != nil {
		h.errs.Report(ctx, "store/grant", err)
		if _, rerr := h.points.Refund(ctx, int64(userID), cost, "refund_store_purchase",
			fmt.Sprintf("Refund: %q could not be granted", item.Name), int64(item.ID)); rerr != nil {
			h.errs.Report(ctx, "store/refund", rerr)
		}
		_ = h.store.RestoreStock(ctx, item.ID)
		return "", 0, err
	}

	// 4) Record the sale in the store's own ledger. The economic
	// transaction already completed (points spent, reward granted), so a
	// failure here is logged loudly but not rolled back — undoing a
	// granted rank would be worse than a missing audit row.
	if err := h.store.RecordPurchase(ctx, userID, item.ID, cost); err != nil {
		h.errs.Report(ctx, "store/record-purchase", err)
	}
	return reward, cost, nil
}

// grantReward dispatches on the item's reward type. The store's own rewards
// are the cases below — each one a capability looked up at Provision or Start.
// Anything else belongs to a plugin that contributed the type, and arrives as
// pur: see the default arm.
func (h *Handlers) grantReward(ctx context.Context, userID int, item *Item, pur *pluginapi.StorePurchase) (string, error) {
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
	case RewardPerk:
		// Same shape as invites: a host with no tracker economy is legitimate,
		// so an absent capability fails THIS purchase rather than boot, and the
		// caller unwinds the points.
		if h.perks == nil {
			return "", fmt.Errorf("perk item %d: no %s registered on this host",
				item.ID, pluginapi.PerkGranterName)
		}
		// No default here, unlike invites. A perk kind is not a quantity — a
		// wrong one has no sensible fallback, and guessing "freeleech" because
		// an admin typed "freeleach" would sell a member something they did not
		// choose. The perks plugin rejects an unknown kind, which is the check
		// that matters; this only makes the empty case say so plainly.
		if item.RewardRef == "" {
			return "", fmt.Errorf("perk item %d has no reward_ref (the perk kind)", item.ID)
		}
		if err := h.perks.GrantPerk(ctx, int64(userID), item.RewardRef); err != nil {
			return "", fmt.Errorf("grant perk %q: %w", item.RewardRef, err)
		}
		return item.RewardRef + " token", nil
	case RewardMedalGrant:
		if h.medals == nil {
			return "", fmt.Errorf("medal item %d: no %s registered on this host",
				item.ID, pluginapi.MedalGranterName)
		}
		if item.RewardRef == "" {
			return "", fmt.Errorf("medal item %d has no reward_ref (the medal slug)", item.ID)
		}
		if err := h.medals.GrantMedal(ctx, int64(userID), item.RewardRef); err != nil {
			return "", fmt.Errorf("grant medal %q: %w", item.RewardRef, err)
		}
		return item.RewardRef + " medal", nil
	case RewardUpload, RewardDownload:
		// The tracker's transfer credit. Same terms as perks: an absent
		// capability fails THIS purchase and the caller unwinds the points.
		if h.credit == nil {
			return "", fmt.Errorf("credit item %d: no %s registered on this host",
				item.ID, pluginapi.TrackerCreditName)
		}
		gb, err := strconv.Atoi(item.RewardRef)
		if err != nil || gb <= 0 {
			return "", fmt.Errorf("credit item %d has invalid reward_ref %q (whole GB)", item.ID, item.RewardRef)
		}
		bytes := int64(gb) << 30
		if RewardType(item.RewardType) == RewardUpload {
			if err := h.credit.CreditUpload(ctx, int64(userID), bytes); err != nil {
				return "", fmt.Errorf("credit upload: %w", err)
			}
			return fmt.Sprintf("%d GB added to your uploaded", gb), nil
		}
		if err := h.credit.CreditDownload(ctx, int64(userID), bytes); err != nil {
			return "", fmt.Errorf("credit download: %w", err)
		}
		return fmt.Sprintf("%d GB wiped from your downloaded", gb), nil
	case RewardFlair:
		// Same terms as perks: an absent capability fails THIS purchase and
		// the caller unwinds the points. reward_ref is the flair id and gets
		// no fallback — the pointstore rejects an unknown id, and guessing
		// would equip a member with something they did not choose.
		if h.flair == nil {
			return "", fmt.Errorf("flair item %d: no %s registered on this host",
				item.ID, pluginapi.FlairGranterName)
		}
		if item.RewardRef == "" {
			return "", fmt.Errorf("flair item %d has no reward_ref (the flair id)", item.ID)
		}
		name, err := h.flair.EquipFlair(ctx, int64(userID), item.RewardRef)
		if err != nil {
			return "", fmt.Errorf("equip flair %q: %w", item.RewardRef, err)
		}
		return name + " flair equipped", nil
	default:
		// A type another plugin contributed. The points are already gone, so
		// the provider only hands over — and an error here unwinds the sale
		// the same way a missing rank does, which is why a provider that
		// cannot deliver must say so rather than half-succeed.
		if pur == nil {
			return "", fmt.Errorf("unknown reward_type %q on item %d", item.RewardType, item.ID)
		}
		t, ok := h.itemType(item.RewardType)
		if !ok {
			// Between pricing and granting, in the same request. Vanishingly
			// unlikely and still the honest failure: refuse and refund rather
			// than guess what the departed plugin would have done.
			return "", fmt.Errorf("item %d: no provider for reward type %q", item.ID, item.RewardType)
		}
		return t.Grant(ctx, *pur)
	}
}

// --- admin catalog ---

func (h *Handlers) AdminStorePage(c *gin.Context) {
	items, err := h.store.ListItems(c.Request.Context(), false)
	if err != nil {
		h.errs.Report(c.Request.Context(), "store/admin-list", err)
	}
	renderPage(c, "Store — admin", "admin_store.html", gin.H{
		"Items": items,
		// The def editor's Reward dropdown, built rather than written out: a
		// plugin that contributes a type appears here without the store
		// knowing it exists, and a hand-kept <option> list cannot go stale
		// against the code that grants.
		"Types": h.typeInfos(c.Request.Context()),
		"Error": c.Query("error"),
		"Ok":    c.Query("ok") == "1",
	})
}

func (h *Handlers) CreateItem(c *gin.Context) {
	it := itemFromForm(c)
	if err := h.validateItem(c.Request.Context(), it); err != nil {
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
	if err := h.validateItem(c.Request.Context(), it); err != nil {
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
		Flavour:     strings.TrimSpace(c.PostForm("flavour")),
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
	// Flavour: empty means both (the pre-flavour default); anything else
	// must be one of the three, or the shop's filter has a row it cannot
	// classify.
	switch it.Flavour {
	case "":
		it.Flavour = "both"
	case "both", "tracker", "indexer":
	default:
		return fmt.Errorf("unknown flavour %q (both, tracker or indexer)", it.Flavour)
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
	case RewardPerk:
		// The kind gets no fallback (see grantReward): the perks plugin
		// rejects unknowns at buy time, but an empty ref is knowably wrong
		// NOW, and now is when the admin is looking at the form.
		if it.RewardRef == "" {
			return errors.New("perk reward needs the perk kind in reward ref")
		}
		return nil
	case RewardFlair:
		if it.RewardRef == "" {
			return errors.New("flair reward needs the flair id in reward ref")
		}
		return nil
	case RewardUpload, RewardDownload:
		if n, err := strconv.Atoi(it.RewardRef); err != nil || n <= 0 {
			return errors.New("credit reward needs whole GB in reward ref")
		}
		return nil
	case RewardMedalGrant:
		if it.RewardRef == "" {
			return errors.New("medal reward needs the medal slug in reward ref")
		}
		return nil
	default:
		return fmt.Errorf("%w %q", errUnknownRewardType, it.RewardType)
	}
}

// errUnknownRewardType marks a type the store does not grant itself — which
// is a contributed type if a provider is registered, and a mis-typed row if
// not. validItem cannot tell the two apart (it holds no registry), so it says
// which question it could not answer and validateItem answers it.
var errUnknownRewardType = errors.New("unsupported reward type")

// validateItem gates a def at the admin form: the store's own rules first,
// then the contributing plugin's for a type the store does not grant. The
// provider validates because only it knows what its reward_ref means — and it
// validates HERE, where the person who can fix a typo is looking, rather than
// at some member's purchase hours later.
func (h *Handlers) validateItem(ctx context.Context, it *Item) error {
	err := validItem(it)
	if !errors.Is(err, errUnknownRewardType) {
		return err
	}
	t, ok := h.itemType(it.RewardType)
	if !ok {
		return fmt.Errorf("unsupported reward type %q", it.RewardType)
	}
	return t.Validate(ctx, it.RewardRef, it.RewardDays)
}
