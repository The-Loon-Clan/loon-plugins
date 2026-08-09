package tracker

import (
	"context"
	"encoding/hex"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// The BitTorrent HTTP endpoints, lifted from the host's TrackerHandler.
//
// These two are the only surface on the site whose client is a program rather
// than a browser, and that shapes everything about them:
//
//   - Authentication is a passkey in the URL. A torrent client has no session,
//     cannot follow a login redirect, and would happily parse a login page as a
//     bencoded response. That is why the page policy declares these Public and why
//     failures answer 200 with a bencoded failure rather than 401 or a redirect.
//   - A failure is a MESSAGE, not a status. `announceFail` returns HTTP 200 with
//     `d14:failure reason…e`, because that is what clients display to the user.
//     A 403 shows up in a client as "tracker error" with no explanation.

// Gate is what the plugin needs to know about a member before letting them
// announce, supplied by the host.
//
// Two questions, deliberately separate. `Entitled` is the tracker's own access
// decision, which is an entitlement rather than a column — the host may grant it
// by role baseline, by paid rank, or by hand, and this plugin does not care which.
// `Active` is whether the account still exists and is not banned, which is the
// host's business entirely.
type Gate struct {
	Entitled func(ctx context.Context, userID int64) bool
	Active   func(ctx context.Context, userID int64) bool
}

// EntitlementKey is the entitlement an announce requires.
//
// Exported because the HOST grants it and needs the same string. A literal in two
// repositories is how a member ends up entitled to something nothing checks.
const EntitlementKey = "tracker.access"

// NewGate builds the standard gate from core's services.
//
// Both checks fail CLOSED when the service is absent. A host with no entitlements
// service has not decided that everyone may use the tracker; it has not wired the
// thing that decides, and answering "yes" on its behalf would open a private
// tracker to anyone holding a passkey.
func NewGate(c *core.Core) Gate {
	return Gate{
		Entitled: func(ctx context.Context, userID int64) bool {
			if c.Entitlements == nil {
				return false
			}
			return c.Entitlements.Has(ctx, userID, EntitlementKey)
		},
		Active: func(ctx context.Context, userID int64) bool {
			if c.Users == nil {
				return false
			}
			u, err := c.Users.GetByID(ctx, userID)
			return err == nil && u != nil && u.Role >= core.RoleUser
		},
	}
}

// Announce is the BitTorrent HTTP announce endpoint.
func (h *Handlers) Announce(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok, err := h.store.UserByPasskey(ctx, c.Param("passkey"))
	if err != nil || !ok {
		// One message for both cases on purpose: an unknown passkey and a failed
		// lookup are indistinguishable to the client, and saying which would let
		// someone probe whether a passkey is real.
		h.announceFail(c, "invalid passkey")
		return
	}
	// Two checks, both replacing what was `!u.TrackerAccess || u.RoleLevel() <=
	// RoleDisabled` on a host user row.
	if !h.gate.Active(ctx, userID) {
		h.announceFail(c, "account not authorized")
		return
	}
	if !h.gate.Entitled(ctx, userID) {
		h.announceFail(c, "account not authorized")
		return
	}

	// Clients URL-encode the 20-byte info_hash and peer_id exactly once, so the
	// raw query values are read straight off the URL rather than through any
	// binding that might re-decode them.
	q := c.Request.URL.Query()
	req, err := ParseAnnounce(map[string]string{
		"info_hash":  q.Get("info_hash"),
		"peer_id":    q.Get("peer_id"),
		"port":       q.Get("port"),
		"uploaded":   q.Get("uploaded"),
		"downloaded": q.Get("downloaded"),
		"left":       q.Get("left"),
		"event":      q.Get("event"),
		"numwant":    q.Get("numwant"),
		"compact":    q.Get("compact"),
		"no_peer_id": q.Get("no_peer_id"),
	}, c.ClientIP())
	if err != nil {
		h.announceFail(c, err.Error())
		return
	}

	tt, err := h.store.Torrent(ctx, req.InfoHashHex)
	if err != nil {
		h.announceFail(c, "lookup failed")
		return
	}
	if tt == nil {
		h.announceFail(c, "torrent not registered")
		return
	}

	prev, err := h.peers.Get(ctx, req.InfoHashHex, req.PeerID)
	if err != nil {
		h.announceFail(c, "peer store unavailable")
		return
	}
	// One peer_id belongs to one member. This is what catches a shared account and
	// the naive "copy a friend's .torrent" — the passkey identifies the member, but
	// without this a second person could announce the same peer_id and have their
	// traffic credited to the first.
	if prev != nil && prev.UserID != 0 && prev.UserID != userID {
		h.announceFail(c, "peer_id already in use")
		return
	}

	delta := ComputeDelta(prev, req)
	if delta.Stopped {
		_ = h.peers.Remove(ctx, req.InfoHashHex, req.PeerID)
	} else {
		_ = h.peers.Put(ctx, req.InfoHashHex, Peer{
			UserID: userID, PeerID: req.PeerID, IP: req.IP, Port: req.Port,
			Uploaded: req.Uploaded, Downloaded: req.Downloaded,
			Left: req.Left, Completed: req.Left == 0,
		})
	}

	// What the site CREDITS, which is not always what the client reported.
	// Freeleech, double upload and any other economy a host runs arrive here as
	// two factors — see multiplier.go. With nothing wired these are the deltas
	// unchanged, so a tracker with no economy plugin behaves exactly as before.
	//
	// The PEER snapshot above keeps the raw figures deliberately: it is the
	// baseline the next delta is diffed against, and scaling it would compound
	// the multiplier on every announce.
	creditUp, creditDown := Credit(ctx, userID, req.InfoHashHex, delta.Up, delta.Down)
	if err := h.store.ApplyAnnounceDelta(ctx, userID, req.InfoHashHex,
		creditUp, creditDown, delta.Left, delta.Done); err != nil {
		h.announceFail(c, "stats write failed")
		return
	}
	// Snatch on the TRANSITION only. `delta.Done && !prev.Completed` is what stops
	// a client that keeps announcing after finishing from incrementing the count
	// every few minutes for the rest of the week.
	if delta.Done && (prev == nil || !prev.Completed) {
		_ = h.store.IncrementSnatches(ctx, req.InfoHashHex)
	}

	peers, err := h.peers.List(ctx, req.InfoHashHex)
	if err != nil {
		// A peer list this announce cannot read is not a failure worth refusing:
		// the client's own stats were already recorded, and an empty peer list
		// means it retries at the interval rather than seeing an error.
		peers = nil
	}

	seeders, leechers := 0, 0
	for _, pp := range peers {
		if pp.Left == 0 {
			seeders++
		} else {
			leechers++
		}
	}
	// Recomputed from the swarm rather than accumulated, so the denormalised
	// counters on the torrent row cannot drift away from the peers actually there.
	_ = h.store.SetSwarmCounts(ctx, req.InfoHashHex, seeders, leechers)

	c.Data(http.StatusOK, "text/plain", EncodeAnnounceResponse(
		AnnounceInterval, seeders, leechers,
		CompactPeers4(peers, req.NumWant, req.PeerID),
		CompactPeers6(peers, req.NumWant, req.PeerID),
	))
}

// Scrape answers the multi-info-hash scrape. Clients repeat info_hash= per
// torrent; unknown hashes are OMITTED rather than reported, which is the standard
// behaviour and also avoids confirming which hashes this tracker carries.
func (h *Handlers) Scrape(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok, err := h.store.UserByPasskey(ctx, c.Param("passkey"))
	if err != nil || !ok || !h.gate.Active(ctx, userID) || !h.gate.Entitled(ctx, userID) {
		c.Data(http.StatusForbidden, "text/plain", EncodeAnnounceFailure("invalid passkey"))
		return
	}
	hashes := c.Request.URL.Query()["info_hash"]
	entries := make([]ScrapeEntry, 0, len(hashes))
	for _, h20 := range hashes {
		// Exactly 20 bytes or it is not an info_hash. Skipped rather than
		// rejected, so one malformed entry in a batch does not cost the client
		// the rest of its scrape.
		if len(h20) != 20 {
			continue
		}
		row, err := h.store.Torrent(ctx, hex.EncodeToString([]byte(h20)))
		if err != nil || row == nil {
			continue
		}
		entries = append(entries, ScrapeEntry{
			InfoHash: h20, Complete: row.Seeders,
			Downloaded: row.Snatches, Incomplete: row.Leechers,
		})
	}
	c.Data(http.StatusOK, "text/plain", EncodeScrapeResponse(entries))
}

// announceFail answers a failure the way a torrent client can read it: HTTP 200
// carrying a bencoded failure reason. A 4xx shows up in a client as an unexplained
// "tracker error", so the status code is the wrong channel for this.
func (h *Handlers) announceFail(c *gin.Context, reason string) {
	c.Data(http.StatusOK, "text/plain", EncodeAnnounceFailure(reason))
}
