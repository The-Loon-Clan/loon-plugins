package donations

// BTCPay Server webhook ingress. Settled invoices land here as JSON;
// we verify the HMAC signature against a shared secret, look up the
// site user_id from the invoice metadata, and record the donation
// via the same store.CreateDonation entry point the admin manual-entry
// form uses. That keeps the points-credit + Donator-flag-flip transaction
// identical across both ingress paths.
//
// BTCPay setup (operator's side, one-time):
//   1. In BTCPay Server, Stores → <store> → Settings → Webhooks → Add
//      Webhook URL: https://amenzb.moe/api/btcpay/webhook
//      Events: pick "An invoice has been settled"
//      Secret: random 32-byte hex (paste into site setting
//              `btcpay_webhook_secret` via /admin/donate/btcpay)
//   2. Create invoices via the API with metadata={"site_user_id": "<n>"}
//      so this handler can credit points to the right site account.
//      Anonymous donations omit metadata; the donation row still gets
//      recorded but credits no user.
//
// Verification: BTCPay signs every webhook with HMAC-SHA256 over the
// raw body, hex-encoded, prefixed with `sha256=`, and sent in the
// `BTCPay-Sig` header. We recompute and compare with constant-time
// equality. Mismatch → 401, no DB write.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/httpclient"
)

// btcpayWebhookPayload is the subset of BTCPay's webhook body we read.
// BTCPay sends a much larger object; the rest is ignored. The
// json.Decoder is lenient about extra fields.
type btcpayWebhookPayload struct {
	DeliveryID         string `json:"deliveryId"`
	WebhookID          string `json:"webhookId"`
	OriginalDeliveryID string `json:"originalDeliveryId"`
	IsRedelivery       bool   `json:"isRedelivery"`
	Type               string `json:"type"`      // e.g. "InvoiceSettled"
	Timestamp          int64  `json:"timestamp"` // unix seconds
	StoreID            string `json:"storeId"`
	InvoiceID          string `json:"invoiceId"`
	// Metadata — operator-controlled when creating the invoice. We
	// honour `site_user_id`, `donor_label`, and `package_id` keys.
	// `package_id` links a settled donation back to the
	// donation_packages row the donor claimed (migration 261).
	//
	// json.RawMessage, not map[string]string: the site's own
	// click-to-claim flow (claim.go) writes site_user_id/package_id
	// as JSON NUMBERS, and BTCPay echoes metadata verbatim — a
	// map[string]string decode fails outright on those, 400-ing every
	// claim-flow settlement. metaString below coerces either shape.
	Metadata map[string]json.RawMessage `json:"metadata"`
}

// metaString extracts a metadata value as a string whether BTCPay
// echoed it as a JSON string ("42") or a JSON number (42). Absent key
// or unparseable value → "".
func metaString(m map[string]json.RawMessage, key string) string {
	raw, ok := m[key]
	if !ok || len(raw) == 0 {
		return ""
	}
	// Quoted string → unquote via json. Bare number/other → use the
	// raw token as-is (strconv on the caller side handles numerics).
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	}
	return string(raw)
}

// BTCPayWebhook receives + verifies + records BTCPay invoice events.
//
// Wired by main.go as a public POST endpoint (no auth middleware) —
// the HMAC verification IS the auth.
func (h *Handlers) BTCPayWebhook(c *gin.Context) {
	ctx := c.Request.Context()

	// Read the entire body before anything else — HMAC verification
	// has to run against the EXACT bytes the sender signed; using
	// gin's binding helpers would re-serialise and break the digest.
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "read body"})
		return
	}

	// Verify signature against the shared secret.
	secret, _ := h.deps.Settings.GetSetting(ctx, "btcpay_webhook_secret")
	if secret == "" {
		// Unconfigured — refuse so a misconfigured webhook can't
		// silently inject donations.
		log.Printf("btcpay-webhook: rejected — secret not configured")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "btcpay webhook unconfigured"})
		return
	}
	header := c.GetHeader("BTCPay-Sig")
	if !verifyBTCPaySig(secret, body, header) {
		log.Printf("btcpay-webhook: signature mismatch (header=%q)", header)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "bad signature"})
		return
	}

	var p btcpayWebhookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad json"})
		return
	}

	// We only care about settlements. BTCPay also sends Created /
	// PaymentSettled / Confirmed / etc — those are fine to ignore.
	if p.Type != "InvoiceSettled" {
		c.JSON(http.StatusOK, gin.H{"ok": true, "ignored": p.Type})
		return
	}

	// Idempotency by the stable txid (btcpay-<invoiceID>), BEFORE the
	// API fetch that can vary the asset label. BTCPay redelivers until
	// it gets a 2xx; a redelivery whose API-fetch outcome differs from
	// the first would otherwise land a SECOND row under a different
	// (asset, txid) and double-credit. txid is derived from the
	// HMAC-verified body, so this pre-check is the true dedup.
	txid := "btcpay-" + p.InvoiceID
	if existing, _ := h.store.GetDonationByTxid(ctx, txid); existing != nil {
		c.JSON(http.StatusOK, gin.H{"ok": true, "deduped": true})
		return
	}

	// Pull the actual amount from the BTCPay API — the webhook body
	// carries no amount field, so this call is the only amount source.
	// On failure it returns zeros; we record the settlement at $0
	// rather than lose it, and the admin corrects via /admin/donate/log.
	// (Follow-up: distinguish a transient API failure — worth a 5xx so
	// BTCPay retries when the API is back — from absent API creds,
	// where retrying can't help.)
	amountUSD, asset, amountNative := fetchInvoiceTotals(ctx, h, p.InvoiceID)
	if amountUSD <= 0 {
		log.Printf("btcpay-webhook: invoice %s settled with $0 — recording anyway", p.InvoiceID)
	}

	donorLabel := clampDonorLabel(metaString(p.Metadata, "donor_label"))
	d := &Donation{
		Asset:        asset,
		Txid:         txid,
		AmountNative: amountNative,
		AmountUSD:    amountUSD,
		DonorLabel:   donorLabel,
		Note:         "btcpay invoice " + p.InvoiceID,
		ReceivedAt:   time.Unix(p.Timestamp, 0).UTC(),
	}
	if uidStr := strings.TrimSpace(metaString(p.Metadata, "site_user_id")); uidStr != "" {
		if uid, err := strconv.Atoi(uidStr); err == nil && uid > 0 {
			d.DonorUserID = &uid
		}
	}
	// Package claim link (migration 261). Empty / unparseable / non-
	// positive → leave PackageID nil; the donation lands as if it
	// came from the tip jar or a direct address. The donations-page
	// stock counter only counts rows where package_id matches an
	// active package, so a stale id can't poison the public view.
	if pidStr := strings.TrimSpace(metaString(p.Metadata, "package_id")); pidStr != "" {
		if pid, err := strconv.ParseInt(pidStr, 10, 64); err == nil && pid > 0 {
			d.PackageID = &pid
		}
	}

	donatorThreshold := 5.0
	if v, _ := h.deps.Settings.GetSetting(ctx, "donate_donator_threshold_usd"); v != "" {
		if f, perr := strconv.ParseFloat(v, 64); perr == nil {
			donatorThreshold = f
		}
	}

	if err := h.store.CreateDonation(ctx, d, donatorThreshold); err != nil {
		// Two failure classes, and they must be told apart — the old
		// code acked 200 for BOTH, which silently DROPPED a settled
		// donation on any transient error (BTCPay stops retrying on
		// 2xx). Re-check by txid: if a row now exists, this was a
		// concurrent-redelivery race that lost the (asset,txid) UNIQUE
		// — the donation IS recorded, so ack. Otherwise it's a real
		// failure (transient DB, FK on a since-deleted donor) — 5xx so
		// BTCPay retries and the donation isn't lost.
		if existing, _ := h.store.GetDonationByTxid(ctx, txid); existing != nil {
			c.JSON(http.StatusOK, gin.H{"ok": true, "deduped": true})
			return
		}
		log.Printf("btcpay-webhook: CreateDonation invoice=%s: %v", p.InvoiceID, err)
		h.errs.Report(ctx, "btcpay-webhook", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "record failed"})
		return
	}

	log.Printf("btcpay-webhook: recorded donation $%.2f (%s) invoice=%s user=%v",
		d.AmountUSD, d.Asset, p.InvoiceID, d.DonorUserID)
	// Did this one meet the site's monthly goal? Checked after the row commits
	// so the sum it reads includes this donation — before it, the payment that
	// crosses the line is the one that does not count. See goalreward.go.
	h.maybeOpenGoalReward(ctx)
	// After the row commits, and NOT in the dedup branch above: a redelivery
	// that finds the donation already recorded must not announce it a second
	// time.
	h.emit(ctx, EventDonationReceived, DonationReceived{
		DonationID: d.ID, DonorUserID: d.DonorUserID, AmountUSD: d.AmountUSD,
		Asset: d.Asset, PackageID: d.PackageID,
	})
	c.JSON(http.StatusOK, gin.H{"ok": true, "donation_id": d.ID})
}

// donorLabelMaxLen caps the public-leaderboard label. The claim POST
// takes donor_label from the donor and it round-trips to the public
// page via invoice metadata; without a server-side cap a hand-crafted
// POST could store multi-KB junk (the label renders auto-escaped, so
// this is defacement/bloat control, not XSS). Matches the admin
// form's client-side maxlength.
const donorLabelMaxLen = 80

// clampDonorLabel trims and length-limits a donor-supplied label.
func clampDonorLabel(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > donorLabelMaxLen {
		s = s[:donorLabelMaxLen]
	}
	return s
}

// verifyBTCPaySig compares the `sha256=<hex>` header against an
// HMAC-SHA256 of the raw body keyed on the shared secret. Constant-
// time compare prevents timing-side-channel leakage of the secret.
func verifyBTCPaySig(secret string, body []byte, header string) bool {
	if !strings.HasPrefix(header, "sha256=") {
		return false
	}
	want, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	got := mac.Sum(nil)
	return hmac.Equal(want, got)
}

// fetchInvoiceTotals queries the BTCPay API for the canonical
// invoice state. Returns (usd_amount, asset_label, native_amount).
// On any error returns zeroes — the caller logs the gap and records
// what it has.
//
// BTCPay API: GET /api/v1/stores/{storeId}/invoices/{invoiceId}
// Auth: API key with View Invoices permission, header
//
//	Authorization: token <api-key>
//
// Settings consumed:
//
//	btcpay_url       e.g. https://btcpay.example.com
//	btcpay_store_id
//	btcpay_api_key
func fetchInvoiceTotals(ctx context.Context, h *Handlers, invoiceID string) (usd float64, asset string, native float64) {
	base, _ := h.deps.Settings.GetSetting(ctx, "btcpay_url")
	storeID, _ := h.deps.Settings.GetSetting(ctx, "btcpay_store_id")
	apiKey, _ := h.deps.Settings.GetSetting(ctx, "btcpay_api_key")
	if base == "" || storeID == "" || apiKey == "" || invoiceID == "" {
		return 0, "BTC", 0
	}
	url := strings.TrimRight(base, "/") + "/api/v1/stores/" + storeID + "/invoices/" + invoiceID
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, "BTC", 0
	}
	req.Header.Set("Authorization", "token "+apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClientFor("btcpay").Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return 0, "BTC", 0
	}
	defer resp.Body.Close()
	var inv struct {
		Currency string  `json:"currency"`
		Amount   float64 `json:"amount,string"`
		Status   string  `json:"status"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err := json.Unmarshal(body, &inv); err != nil {
		return 0, "BTC", 0
	}
	// Currency is the invoice's settlement currency (USD / BTC / EUR).
	// We treat USD-priced invoices as fiat-tracked; crypto-priced
	// invoices need a quick FX hop, deferred — for now treat the
	// invoice currency as the asset label and the amount as both
	// usd-equivalent and native (fine for a USD-denominated invoice).
	return inv.Amount, inv.Currency, inv.Amount
}

// httpClientFor centralises outbound HTTP for the BTCPay calls so
// they share the standard pooled client, avoiding per-call dialer
// allocation. Reuses indexer-site's httpclient pattern.
func httpClientFor(_ string) *http.Client {
	// Five-second timeout is plenty for a single GET against BTCPay's
	// own API; retries aren't useful here because the webhook handler
	// returns 200 either way. NewSafeFetch applies the SSRF block so the
	// admin-configured BTCPay base URL can't be pointed at internal /
	// cloud-metadata addresses.
	return httpclient.NewSafeFetch(5 * time.Second)
}
