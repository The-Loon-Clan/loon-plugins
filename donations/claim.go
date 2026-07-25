package donations

// Click-to-claim flow for donation packages (migration 261).
//
// Public flow:
//   1. User clicks a package card on /help/donate.
//   2. Browser POSTs /donate/claim-package/:id (CSRF-protected like any
//      other form submit).
//   3. Server validates the package is still active + has stock left,
//      then POSTs to BTCPay Server's `/api/v1/stores/{store}/invoices`
//      with the package's amount in USD and metadata carrying:
//
//        site_user_id  — donor's id, if logged in (else absent)
//        package_id    — the package being claimed
//        donor_label   — optional public name
//
//   4. Server redirects the browser to the BTCPay-hosted checkout URL
//      from the invoice response.
//   5. Donor pays. BTCPay fires the InvoiceSettled webhook.
//   6. btcpay_webhook_handler.go reads metadata["package_id"], sets
//      d.PackageID on the donation row, and CreateDonation persists it.
//   7. On next /help/donate render, the package's StockUsed
//      increments by one; if it now equals StockTotal, the card moves
//      to the "Funded ✓" subsection.
//
// Failure modes:
//   - BTCPay not configured → 503 with a friendly message; nothing
//     attempted server-side.
//   - Package missing / inactive / sold out → 409, redirect back to
//     /help/donate#packages so the stale card refreshes.
//   - BTCPay returns non-2xx → 502; LogServiceError under
//     "donate/claim-invoice" so /admin/errors surfaces it.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// btcpayInvoiceRequest is the subset of BTCPay's invoice-create body
// we populate. BTCPay accepts a lot more; the rest defaults.
type btcpayInvoiceRequest struct {
	Amount   string                 `json:"amount"`
	Currency string                 `json:"currency"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	Checkout struct {
		RedirectURL string `json:"redirectURL,omitempty"`
	} `json:"checkout,omitempty"`
}

// btcpayInvoiceResponse picks up the fields we route the user to.
type btcpayInvoiceResponse struct {
	ID           string `json:"id"`
	CheckoutLink string `json:"checkoutLink"`
	Status       string `json:"status"`
}

// ClaimPackage validates the request and asks BTCPay for an invoice,
// then redirects the browser to the hosted checkout. See file header
// for the full lifecycle.
func (h *Handlers) ClaimPackage(c *gin.Context) {
	ctx := c.Request.Context()
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		c.Redirect(http.StatusFound, "/help/donate?err=claim-bad-id#packages")
		return
	}

	// Master donate-enabled gate — same check as /help/donate's page
	// guard. Non-admins get 404 when donations are off.
	if !h.deps.IsDonateEnabled() {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	pkg, err := h.store.GetDonationPackage(ctx, id)
	if err != nil || pkg == nil || !pkg.Active {
		c.Redirect(http.StatusFound, "/help/donate?err=claim-not-found#packages")
		return
	}

	// Recompute stock at request-time so a sold-out card can't be
	// claimed via a stale browser. yearStart matches the public page's
	// boundary so the two views can't disagree.
	now := time.Now().UTC()
	yearStart := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	usage, _ := h.store.CountDonationsPerPackageSince(ctx, yearStart)
	if usage[pkg.ID] >= pkg.StockTotal {
		c.Redirect(http.StatusFound, "/help/donate?err=claim-sold-out#packages")
		return
	}

	// Pull BTCPay credentials. Empty = "BTCPay not configured" — fail
	// with a clean 503 + the admin sees the gap in their settings.
	base, _ := h.deps.Settings.GetSetting(ctx, "btcpay_url")
	storeID, _ := h.deps.Settings.GetSetting(ctx, "btcpay_store_id")
	apiKey, _ := h.deps.Settings.GetSetting(ctx, "btcpay_api_key")
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	storeID = strings.TrimSpace(storeID)
	apiKey = strings.TrimSpace(apiKey)
	if base == "" || storeID == "" || apiKey == "" {
		c.HTML(http.StatusServiceUnavailable, "error.html", h.deps.BaseData(c, gin.H{
			"Code":    503,
			"Title":   "Click-to-claim isn't set up yet",
			"Message": "BTCPay Server isn't configured on this site. Send a direct crypto donation from the donate page — the admin will match it to your slot manually.",
		}))
		return
	}

	// Build the invoice. metadata is opaque to BTCPay; the webhook
	// reads what we put here. Values are STRINGS on purpose: BTCPay
	// echoes metadata verbatim in the settlement webhook, and the
	// webhook's tolerant parse handles both, but strings keep the two
	// ends aligned regardless of which parses a given invoice.
	meta := map[string]interface{}{
		"package_id": strconv.FormatInt(pkg.ID, 10),
	}
	if u, ok := h.auth.CurrentUser(c); ok {
		meta["site_user_id"] = strconv.FormatInt(u.ID, 10)
	}
	if label := clampDonorLabel(c.PostForm("donor_label")); label != "" {
		meta["donor_label"] = label
	}

	reqBody := btcpayInvoiceRequest{
		Amount:   fmt.Sprintf("%.2f", pkg.AmountUSD),
		Currency: "USD",
		Metadata: meta,
	}
	// Send the donor back to the donate page after they finish. The
	// page will show the still-pending slot until the webhook lands;
	// the Recent Donors carousel updates as soon as it does.
	reqBody.Checkout.RedirectURL = absSiteURL(c, "/help/donate?ok=claim#packages")

	resp, err := postBTCPayInvoice(ctx, base, storeID, apiKey, reqBody)
	if err != nil {
		log.Printf("donate/claim: BTCPay invoice create failed for package=%d: %v", pkg.ID, err)
		h.errs.Report(ctx, "donate/claim-invoice", err)
		c.HTML(http.StatusBadGateway, "error.html", h.deps.BaseData(c, gin.H{
			"Code":    502,
			"Title":   "Couldn't reach BTCPay right now",
			"Message": "Please try again in a moment, or send a direct crypto donation from the donate page.",
		}))
		return
	}
	if resp.CheckoutLink == "" {
		log.Printf("donate/claim: BTCPay returned empty checkoutLink for package=%d (invoice=%s)", pkg.ID, resp.ID)
		c.Redirect(http.StatusFound, "/help/donate?err=claim-no-checkout#packages")
		return
	}
	c.Redirect(http.StatusFound, resp.CheckoutLink)
}

// postBTCPayInvoice POSTs to BTCPay's invoice-create endpoint and
// returns the parsed response. Same auth + URL shape as
// fetchInvoiceTotals; kept here to avoid cross-file coupling.
func postBTCPayInvoice(ctx context.Context, base, storeID, apiKey string, body btcpayInvoiceRequest) (*btcpayInvoiceResponse, error) {
	url := base + "/api/v1/stores/" + storeID + "/invoices"
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal invoice: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "token "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := httpClientFor("btcpay")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("btcpay request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("btcpay status %d: %s", resp.StatusCode, string(respBody))
	}
	var out btcpayInvoiceResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode invoice: %w", err)
	}
	return &out, nil
}

// absSiteURL returns an absolute URL pointing at this site for the
// given path. Used as BTCPay's redirectURL so the donor lands back
// on /help/donate after checkout — relative URLs aren't valid there.
func absSiteURL(c *gin.Context, path string) string {
	scheme := "https"
	if c.Request.TLS == nil && !strings.HasPrefix(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "http"
	}
	host := c.Request.Host
	if h := c.GetHeader("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host + path
}
