package donations

// Admin pages for the donation system. As of 2026-05-10 all four
// sections (Costs, Points, Log, Wallet/BTCPay) live on one
// /admin/donate page; the old /admin/donate/{costs,points,log}
// routes still exist but render the same unified template anchored
// to their section (so external links keep working).
//
//   GET  /admin/donate                — unified page (all sections)
//   POST /admin/donate/costs          — create or update one cost
//   POST /admin/donate/costs/:id/del  — delete one
//   POST /admin/donate/points         — save formula + toggle
//   POST /admin/donate/log            — record a manual donation
//   POST /admin/donate/wallet         — save BTC/ETH/XMR addresses + BTCPay settings
//
// The public /help/donate page reads from the same tables / settings;
// there's no caching layer between the two so saves here apply on the
// next public render.

import (
	"fmt"
	"io"
	"net/http"
	urlpkg "net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/httpclient"
)

// DonatePage renders the unified /admin/donate page with all four
// sections — Costs, Points & Toggle, Log, Wallet/BTCPay. Each section
// gathers its data from the same place as the old single-purpose
// pages did, so we keep one source of truth. The page renders with
// an anchor (#costs / #points / #log / #wallet) from the query
// string so post-redirect navigation lands on the section that was
// just saved.
//
// The old per-section route handlers (DonateCostsPage etc.) now
// redirect to /admin/donate with the matching anchor, so external
// links keep working.
func (h *Handlers) AdminDonatePage(c *gin.Context) {
	ctx := c.Request.Context()

	// ── Costs ──
	costs, err := h.store.ListSiteCosts(ctx, true /* include inactive */)
	if err != nil {
		h.errs.HandlerError(c, "admin/donate", err)
		return
	}

	// ── Points + locking groups + master toggle ──
	getF := func(k string, fallback float64) float64 {
		v, _ := h.deps.Settings.GetSetting(ctx, k)
		if v == "" {
			return fallback
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fallback
		}
		return f
	}
	cfg := DonationPointsConfig{
		PointsPerDollar:     getF("donate_points_per_dollar", 1.0),
		MultiplierPer10:     getF("donate_multiplier_per_10", 1.2),
		DonatorThresholdUSD: getF("donate_donator_threshold_usd", 5),
	}
	lockingGroups, _ := h.deps.Settings.GetSetting(ctx, "donate_locking_groups")
	if lockingGroups == "" {
		lockingGroups = "site"
	}
	preview := []DonationPointsRow{}
	for _, d := range []float64{1, 5, 10, 20, 30, 50, 75, 100, 150, 250, 500} {
		preview = append(preview, DonationPointsRow{Dollars: d, Points: cfg.PointsForDollars(d)})
	}

	// ── Donation log + donor-username resolution ──
	donations, err := h.store.ListRecentDonations(ctx, 200)
	if err != nil {
		h.errs.HandlerError(c, "admin/donate-log", err)
		return
	}
	donorIDs := make([]int, 0, len(donations))
	seen := map[int]bool{}
	for _, d := range donations {
		if d.DonorUserID != nil && *d.DonorUserID > 0 && !seen[*d.DonorUserID] {
			seen[*d.DonorUserID] = true
			donorIDs = append(donorIDs, *d.DonorUserID)
		}
	}
	usernames := map[int]string{}
	for _, uid := range donorIDs {
		if name, ok := h.deps.LookupUsername(ctx, uid); ok {
			usernames[uid] = name
		}
	}

	// ── Wallet + BTCPay settings ──
	get := func(k string) string {
		v, _ := h.deps.Settings.GetSetting(ctx, k)
		return v
	}
	wallet := map[string]string{
		"btc":             get("donate_addr_btc"),
		"eth":             get("donate_addr_eth"),
		"xmr":             get("donate_addr_xmr"),
		"btcpay_url":      get("btcpay_url"),
		"btcpay_store_id": get("btcpay_store_id"),
		"btcpay_api_key":  get("btcpay_api_key"),
		"btcpay_secret":   get("btcpay_webhook_secret"),
	}

	// ── Tip-jar goal settings ──
	// Two slots, each carrying name + target_usd + raised_usd. The
	// admin form is a small block at the bottom of the page; the
	// public donate page (help_donate.html) reads the same keys.
	tipJar := map[string]string{
		"goal_1_name":       get("tipjar_goal_1_name"),
		"goal_1_target_usd": get("tipjar_goal_1_target_usd"),
		"goal_1_raised_usd": get("tipjar_goal_1_raised_usd"),
		"goal_2_name":       get("tipjar_goal_2_name"),
		"goal_2_target_usd": get("tipjar_goal_2_target_usd"),
		"goal_2_raised_usd": get("tipjar_goal_2_raised_usd"),
	}

	// ── Donation packages (migration 261) ──
	// includeInactive=true so the admin can see + restore retired ones.
	pkgs, perr := h.store.ListDonationPackages(ctx, true)
	if perr != nil {
		h.errs.HandlerError(c, "admin/donate-packages", perr)
		return
	}
	// pickPackageByQueryID lets the admin form open an existing row for
	// editing via /admin/donate?edit_pkg=<id>#packages.
	var editPkg *DonationPackage
	if idStr := c.Query("edit_pkg"); idStr != "" {
		if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
			for _, p := range pkgs {
				if p.ID == id {
					editPkg = p
					break
				}
			}
		}
	}

	// IsAdmin gates the wallet/BTCPay-health/manual-log forms in the
	// template: those actions are admin-only routes (see Provision), so
	// a mod viewing this page must not be shown controls that would
	// 403 on submit.
	viewer, _ := h.auth.CurrentUser(c)
	isAdmin := viewer.AtLeast(core.RoleAdmin)

	c.HTML(http.StatusOK, "admin_donate.html", h.deps.BaseData(c, gin.H{
		"PageTitle":     "Donate (admin)",
		"IsAdmin":       isAdmin,
		"Costs":         costs,
		"Edit":          pickCostByQueryID(costs, c.Query("edit")),
		"Config":        cfg,
		"LockingGroups": lockingGroups,
		"Preview":       preview,
		"DonateEnabled": h.deps.IsDonateEnabled(),
		"Donations":     donations,
		"Usernames":     usernames,
		"Wallet":        wallet,
		"TipJar":        tipJar,
		"Packages":      pkgs,
		"EditPkg":       editPkg,
		"Saved":         c.Query("ok"),
		"ErrCode":       c.Query("err"),
		"BTCPayTest":    c.Query("btcpay_test"),
		"BTCPayMsg":     c.Query("btcpay_msg"),
	}))
}

// SaveDonatePackage upserts one donation package. POST body fields:
//
//	id           — empty / "0" creates; otherwise updates that row
//	label        — required, free-form
//	amount_usd   — required, > 0
//	stock_total  — required, > 0
//	reward       — optional, free-form (what the donor gets)
//	description  — optional, free-form
//	sort_order   — integer, 0 default
//	active       — checkbox; missing/empty = inactive
//
// Invalid inputs redirect back with ?err=pkg-<reason>#packages so the
// admin form's flash area can show a specific message; we don't
// return JSONInternalError because the form is server-rendered, not
// AJAX.
func (h *Handlers) SaveDonatePackage(c *gin.Context) {
	ctx := c.Request.Context()
	parseF := func(s string) float64 {
		f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return f
	}
	parseI := func(s string) int {
		n, _ := strconv.Atoi(strings.TrimSpace(s))
		return n
	}

	label := strings.TrimSpace(c.PostForm("label"))
	amount := parseF(c.PostForm("amount_usd"))
	stock := parseI(c.PostForm("stock_total"))
	if label == "" || amount <= 0 || stock <= 0 {
		c.Redirect(http.StatusFound, "/admin/donate?err=pkg-invalid#packages")
		return
	}

	p := &DonationPackage{
		Label:       label,
		AmountUSD:   amount,
		StockTotal:  stock,
		Reward:      strings.TrimSpace(c.PostForm("reward")),
		Description: strings.TrimSpace(c.PostForm("description")),
		ResetPeriod: "yearly", // only valid value at migration 261
		SortOrder:   parseI(c.PostForm("sort_order")),
		Active:      c.PostForm("active") != "",
	}
	idStr := strings.TrimSpace(c.PostForm("id"))
	if idStr == "" || idStr == "0" {
		if err := h.store.CreateDonationPackage(ctx, p); err != nil {
			h.errs.HandlerError(c, "admin/donate-package-create", err)
			return
		}
	} else {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			c.Redirect(http.StatusFound, "/admin/donate?err=pkg-bad-id#packages")
			return
		}
		p.ID = id
		if err := h.store.UpdateDonationPackage(ctx, p); err != nil {
			h.errs.HandlerError(c, "admin/donate-package-update", err)
			return
		}
	}
	c.Redirect(http.StatusFound, "/admin/donate?ok=pkg#packages")
}

// DeleteDonatePackage hard-deletes one package. Linked donations
// keep package_id as NULL (ON DELETE SET NULL in migration 261) so
// the donations ledger stays intact — those donations look like
// tip-jar contributions retroactively. Admins should prefer the
// "active" checkbox for reversibility; this endpoint exists for
// "I created it by mistake" cleanup.
func (h *Handlers) DeleteDonatePackage(c *gin.Context) {
	ctx := c.Request.Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.Redirect(http.StatusFound, "/admin/donate?err=pkg-bad-id#packages")
		return
	}
	if err := h.store.DeleteDonationPackage(ctx, id); err != nil {
		h.errs.HandlerError(c, "admin/donate-package-delete", err)
		return
	}
	c.Redirect(http.StatusFound, "/admin/donate?ok=pkg-deleted#packages")
}

// SaveDonateTipJar persists the two tip-jar goal slots. Each slot
// has three free-form settings keys (name, target_usd, raised_usd);
// empty values clear the slot, which makes loadTipJarGoals skip it
// and the public panel hide that card. Numeric fields are stored as
// strings — the read path ParseFloat's them — so admin can clear a
// slot by submitting blanks without us having to model "null".
func (h *Handlers) SaveDonateTipJar(c *gin.Context) {
	ctx := c.Request.Context()
	set := func(key, val string) {
		_ = h.deps.Settings.SetSetting(ctx, key, strings.TrimSpace(val))
	}
	for slot := 1; slot <= 2; slot++ {
		s := strconv.Itoa(slot)
		set("tipjar_goal_"+s+"_name", c.PostForm("goal_"+s+"_name"))
		set("tipjar_goal_"+s+"_target_usd", c.PostForm("goal_"+s+"_target_usd"))
		set("tipjar_goal_"+s+"_raised_usd", c.PostForm("goal_"+s+"_raised_usd"))
	}
	c.Redirect(http.StatusFound, "/admin/donate?ok=tipjar#tipjar")
}

// SaveDonateWallet persists the BTC/ETH/XMR receive addresses and
// the BTCPay-server credentials. All are free-form strings stored
// in site_settings; empty values clear the row (the BTCPay webhook
// handler fails closed when btcpay_webhook_secret is empty, so a
// blank secret means "webhook off").
func (h *Handlers) SaveDonateWallet(c *gin.Context) {
	ctx := c.Request.Context()
	set := func(key, val string) {
		_ = h.deps.Settings.SetSetting(ctx, key, strings.TrimSpace(val))
	}
	set("donate_addr_btc", c.PostForm("addr_btc"))
	set("donate_addr_eth", c.PostForm("addr_eth"))
	set("donate_addr_xmr", c.PostForm("addr_xmr"))
	set("btcpay_url", c.PostForm("btcpay_url"))
	set("btcpay_store_id", c.PostForm("btcpay_store_id"))
	// Secrets are masked in the form (audit R41) — only overwrite when a new
	// value is submitted, so saving the wallet doesn't wipe the stored key.
	setSecret := func(key, val string) {
		if v := strings.TrimSpace(val); v != "" {
			_ = h.deps.Settings.SetSetting(ctx, key, v)
		}
	}
	setSecret("btcpay_api_key", c.PostForm("btcpay_api_key"))
	setSecret("btcpay_webhook_secret", c.PostForm("btcpay_webhook_secret"))
	c.Redirect(http.StatusFound, "/admin/donate?ok=wallet#wallet")
}

// BTCPayHealthCheck makes a live GET request to the configured
// BTCPay store using the saved credentials, and reports the result
// inline so admin can confirm the URL/store ID/API key all work
// BEFORE a real user clicks "Claim this slot →" and discovers a
// misconfigured field via a 502.
//
// Test call: GET /api/v1/stores/{storeId}/invoices?take=1
//   - 200 → credentials valid, "View Invoices" permission present
//   - 401/403 → API key wrong or missing permission
//   - 404 → store ID wrong
//   - 5xx / connection refused → URL wrong, BTCPay down, or DNS issue
//
// Reads the same settings keys the public ClaimPackage handler does,
// so passing this check is the same gate the real flow uses minus
// the "Create Invoices" permission (which we test by trying to
// create a tiny dummy invoice — caught later in the live flow). The
// missing permission shows up there as a clear 4xx with the BTCPay
// error message bubbled to /admin/errors via LogServiceError.
func (h *Handlers) BTCPayHealthCheck(c *gin.Context) {
	ctx := c.Request.Context()
	base, _ := h.deps.Settings.GetSetting(ctx, "btcpay_url")
	storeID, _ := h.deps.Settings.GetSetting(ctx, "btcpay_store_id")
	apiKey, _ := h.deps.Settings.GetSetting(ctx, "btcpay_api_key")
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	storeID = strings.TrimSpace(storeID)
	apiKey = strings.TrimSpace(apiKey)
	if base == "" || storeID == "" || apiKey == "" {
		c.Redirect(http.StatusFound, "/admin/donate?btcpay_test=missing#wallet")
		return
	}

	url := base + "/api/v1/stores/" + storeID + "/invoices?take=1"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/donate?btcpay_test=err&btcpay_msg="+
			urlQueryEscape("build request: "+err.Error())+"#wallet")
		return
	}
	req.Header.Set("Authorization", "token "+apiKey)
	req.Header.Set("Accept", "application/json")
	// SSRF-block the admin-entered BTCPay base URL (can't be aimed at
	// internal / cloud-metadata addresses).
	client := httpclient.NewSafeFetch(8 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/donate?btcpay_test=err&btcpay_msg="+
			urlQueryEscape("connect: "+err.Error())+"#wallet")
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// Success — log it as an info event so the audit trail shows
		// "admin verified BTCPay at <time>" alongside the security
		// events panel.
		c.Redirect(http.StatusFound, "/admin/donate?btcpay_test=ok#wallet")
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		c.Redirect(http.StatusFound, "/admin/donate?btcpay_test=auth&btcpay_msg="+
			urlQueryEscape(fmt.Sprintf("API key rejected (%d). Check the key is correct and has 'View Invoices' permission. Response: %s",
				resp.StatusCode, truncateForQuery(string(body))))+"#wallet")
	case resp.StatusCode == 404:
		c.Redirect(http.StatusFound, "/admin/donate?btcpay_test=store&btcpay_msg="+
			urlQueryEscape(fmt.Sprintf("Store not found (%d). Check the Store ID matches a store on this BTCPay instance.",
				resp.StatusCode))+"#wallet")
	default:
		c.Redirect(http.StatusFound, "/admin/donate?btcpay_test=err&btcpay_msg="+
			urlQueryEscape(fmt.Sprintf("BTCPay returned %d: %s",
				resp.StatusCode, truncateForQuery(string(body))))+"#wallet")
	}
}

// urlQueryEscape is url.QueryEscape exported as a tiny local helper
// so the redirect-building code stays readable inline.
func urlQueryEscape(s string) string {
	return urlpkg.QueryEscape(s)
}

// truncateForQuery clips a string to a length that fits comfortably
// in a redirect URL. BTCPay error bodies can include stack-shaped
// content; we don't need all of it for the admin flash.
func truncateForQuery(s string) string {
	const max = 200
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// adminDonateCosts (legacy) — redirects to the unified page anchored
// to #costs. Kept so old admin bookmarks and the /admin nav link
// continue to work.
func (h *Handlers) DonateCostsPage(c *gin.Context) {
	q := c.Request.URL.RawQuery
	if q != "" {
		c.Redirect(http.StatusFound, "/admin/donate?"+q+"#costs")
		return
	}
	c.Redirect(http.StatusFound, "/admin/donate#costs")
}

func pickCostByQueryID(costs []*SiteCost, idStr string) *SiteCost {
	if idStr == "" {
		return nil
	}
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return nil
	}
	for _, c := range costs {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// SaveDonateCost handles the create/update form post. id="" → insert;
// id=N → update. Anything that fails redirects back with ?err=… so
// the same template can show inline feedback.
func (h *Handlers) SaveDonateCost(c *gin.Context) {
	ctx := c.Request.Context()
	cost := &SiteCost{
		Label:     strings.TrimSpace(c.PostForm("label")),
		Category:  strings.TrimSpace(c.PostForm("category")),
		GoalGroup: strings.TrimSpace(c.PostForm("goal_group")),
		Period:    strings.TrimSpace(c.PostForm("period")),
		Notes:     strings.TrimSpace(c.PostForm("notes")),
		Active:    c.PostForm("active") == "1",
	}
	if cost.Category == "" {
		cost.Category = "other"
	}
	if cost.GoalGroup == "" {
		cost.GoalGroup = "site"
	}
	if cost.Period != "monthly" && cost.Period != "yearly" {
		c.Redirect(http.StatusFound, "/admin/donate?err=bad_period#costs")
		return
	}
	amount, err := strconv.ParseFloat(c.PostForm("amount_usd"), 64)
	if err != nil || amount < 0 {
		c.Redirect(http.StatusFound, "/admin/donate?err=bad_amount#costs")
		return
	}
	cost.AmountUSD = amount
	if v := c.PostForm("sort_order"); v != "" {
		if n, perr := strconv.Atoi(v); perr == nil {
			cost.SortOrder = n
		}
	}
	if cost.Label == "" {
		c.Redirect(http.StatusFound, "/admin/donate?err=missing_label#costs")
		return
	}

	if idStr := c.PostForm("id"); idStr != "" {
		id, perr := strconv.Atoi(idStr)
		if perr != nil || id <= 0 {
			c.Redirect(http.StatusFound, "/admin/donate?err=bad_id#costs")
			return
		}
		cost.ID = id
		if err := h.store.UpdateSiteCost(ctx, cost); err != nil {
			h.errs.HandlerError(c, "admin/donate-cost-update", err)
			return
		}
	} else {
		if err := h.store.CreateSiteCost(ctx, cost); err != nil {
			h.errs.HandlerError(c, "admin/donate-cost-create", err)
			return
		}
	}
	c.Redirect(http.StatusFound, "/admin/donate?ok=costs#costs")
}

// DeleteDonateCost — hard delete. Donations recorded earlier aren't
// affected; only the goal denominator drops.
func (h *Handlers) DeleteDonateCost(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.Redirect(http.StatusFound, "/admin/donate?err=bad_id#costs")
		return
	}
	if err := h.store.DeleteSiteCost(c.Request.Context(), id); err != nil {
		h.errs.HandlerError(c, "admin/donate-cost-delete", err)
		return
	}
	c.Redirect(http.StatusFound, "/admin/donate?ok=costs#costs")
}

// DonatePointsPage (legacy) — redirects to the unified page.
func (h *Handlers) DonatePointsPage(c *gin.Context) {
	c.Redirect(http.StatusFound, "/admin/donate#points")
}

// SaveDonatePoints persists the four knobs. donate_locking_groups is a
// comma-separated list of goal-group names; only listed groups close
// donations when fully funded.
func (h *Handlers) SaveDonatePoints(c *gin.Context) {
	ctx := c.Request.Context()
	set := func(key, val string) {
		_ = h.deps.Settings.SetSetting(ctx, key, val)
	}
	set("donate_points_per_dollar", strings.TrimSpace(c.PostForm("points_per_dollar")))
	set("donate_multiplier_per_10", strings.TrimSpace(c.PostForm("multiplier_per_10")))
	set("donate_donator_threshold_usd", strings.TrimSpace(c.PostForm("donator_threshold_usd")))
	// Normalise locking_groups: trim each entry, drop empties.
	parts := strings.Split(c.PostForm("locking_groups"), ",")
	clean := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			clean = append(clean, p)
		}
	}
	set("donate_locking_groups", strings.Join(clean, ","))

	// Master enable/disable. Drives both the public route's 404 gate
	// and the navbar's hide-for-non-admins logic. Goes through the
	// host's Deps.SetDonateEnabled so the in-process atomic + DB row
	// update happen in one call — toggle takes effect immediately, no
	// restart.
	enabled := c.PostForm("donate_enabled") == "1"
	if err := h.deps.SetDonateEnabled(ctx, enabled); err != nil {
		h.errs.HandlerError(c, "admin/donate-points-enabled", err)
		return
	}

	c.Redirect(http.StatusFound, "/admin/donate?ok=points#points")
}

// DonateLogPage (legacy) — redirects to the unified page.
func (h *Handlers) DonateLogPage(c *gin.Context) {
	c.Redirect(http.StatusFound, "/admin/donate#log")
}

// SaveDonateManual records a fiat / off-chain donation. Asset is
// always "fiat" for these (the on-chain path goes through the BTCPay
// webhook). Donor user-id is optional — left blank for truly
// anonymous receipts that the admin doesn't want to attribute.
//
// Reuses the storage CreateDonation entry point, so the points-credit
// + Donator-flag-flip transaction runs identically to the webhook
// path. The only thing this handler does extra is read the donator
// threshold setting before calling storage.
func (h *Handlers) SaveDonateManual(c *gin.Context) {
	ctx := c.Request.Context()

	amountUSD, err := strconv.ParseFloat(c.PostForm("amount_usd"), 64)
	if err != nil || amountUSD <= 0 {
		c.Redirect(http.StatusFound, "/admin/donate?err=bad_amount#log")
		return
	}

	asset := strings.TrimSpace(c.PostForm("asset"))
	if asset == "" {
		asset = "fiat"
	}

	d := &Donation{
		Asset:      asset,
		AmountUSD:  amountUSD,
		DonorLabel: clampDonorLabel(c.PostForm("donor_label")),
		Note:       strings.TrimSpace(c.PostForm("note")),
	}
	// AmountNative — for fiat we store the USD value here so reports
	// don't read 0. For other assets the admin should have entered
	// the native-unit amount in the form.
	if v := c.PostForm("amount_native"); v != "" {
		if f, perr := strconv.ParseFloat(v, 64); perr == nil {
			d.AmountNative = f
		}
	} else {
		d.AmountNative = amountUSD
	}
	// Optional donor user_id by username lookup. Empty = unattributed.
	if username := strings.TrimSpace(c.PostForm("donor_username")); username != "" {
		if id, ok := h.deps.LookupUserID(ctx, username); ok {
			d.DonorUserID = &id
		}
		// If the username is bogus we silently drop the attribution
		// rather than error — the donation still gets recorded for
		// thermometer purposes; the admin can edit later if needed.
	}

	// Read the lifetime-Donator threshold from settings so storage
	// can flip users.donator inside the same transaction as the
	// donation insert.
	donatorThreshold := 5.0
	if v, _ := h.deps.Settings.GetSetting(ctx, "donate_donator_threshold_usd"); v != "" {
		if f, perr := strconv.ParseFloat(v, 64); perr == nil {
			donatorThreshold = f
		}
	}

	if err := h.store.CreateDonation(ctx, d, donatorThreshold); err != nil {
		h.errs.HandlerError(c, "admin/donate-manual", err)
		return
	}
	c.Redirect(http.StatusFound, "/admin/donate?ok=log#log")
}
