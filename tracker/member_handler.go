package tracker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The member-facing pages, lifted from the host's TrackerHandler.
//
// One thing is deliberately different, and it is a fix rather than a port. The
// host read `u.Passkey` straight off the session user on every one of these, so a
// member with tracker access but no passkey — which was every member, since
// registration never minted one — got a page rendering an empty announce URL and a
// .torrent that could not announce. These read the passkey from the store and MINT
// one on first use, so the page is correct the first time it is opened rather than
// after somebody notices and clicks rotate.

// PageData is what the host's template set receives. A struct rather than a
// gin.H so a missing field is a compile error: html/template streams, and a name
// the model does not have truncates the page mid-row instead of failing.
type PageData struct {
	Torrents []*Torrent
	Total    int
	Rows     []*UserStat
	Totals   Totals
	Passkey  string
	// AnnounceURL is rendered so a member can paste it into a client that wants
	// the tracker URL directly rather than a .torrent.
	AnnounceURL string
	// CSRFToken for the passkey-rotate form, supplied by the host's session
	// layer via Deps rather than derived here.
	CSRFToken string

	// ── the torrent page ────────────────────────────────────────────────────
	// Torrent is the one this page is about; nil on every other page.
	Torrent *Torrent
	// Promotions is what magic has been cast on it, newest first — active and
	// lapsed both, because "the history of this file" is the question the
	// panel answers. Nil on a host without the magic plugin, which renders no
	// panel at all rather than an empty one.
	Promotions []pluginapi.TorrentPromotion
	// HasMagic is whether a cast can be OFFERED: the plugin answered, so the
	// page can link at its form. Separate from len(Promotions) — a torrent
	// nobody has promoted yet is exactly the one a member wants to promote.
	HasMagic bool
}

// IndexPage lists the swarm.
func (h *Handlers) IndexPage(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok := h.viewer(c)
	if !ok {
		return
	}
	rows, total, err := h.store.ListTorrents(ctx, 100, 0)
	if err != nil {
		c.String(http.StatusInternalServerError, "list torrents: %v", err)
		return
	}
	totals, _ := h.store.Totals(ctx, userID)
	pk, err := h.passkeyFor(ctx, userID)
	if err != nil {
		c.String(http.StatusInternalServerError, "passkey: %v", err)
		return
	}
	h.render(c, "tracker_list.html", "Private Tracker", PageData{
		Torrents: rows, Total: total, Totals: totals,
		Passkey: pk, AnnounceURL: h.announceURL(pk), CSRFToken: h.csrf(c),
	})
}

// MyStatsPage shows the member's per-torrent counters.
func (h *Handlers) MyStatsPage(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok := h.viewer(c)
	if !ok {
		return
	}
	totals, err := h.store.Totals(ctx, userID)
	if err != nil {
		c.String(http.StatusInternalServerError, "totals: %v", err)
		return
	}
	rows, err := h.store.ListUserStats(ctx, userID, 200)
	if err != nil {
		c.String(http.StatusInternalServerError, "stats: %v", err)
		return
	}
	pk, err := h.passkeyFor(ctx, userID)
	if err != nil {
		c.String(http.StatusInternalServerError, "passkey: %v", err)
		return
	}
	h.render(c, "tracker_stats.html", "My Tracker Stats", PageData{
		Rows: rows, Totals: totals, Passkey: pk,
		AnnounceURL: h.announceURL(pk), CSRFToken: h.csrf(c),
	})
}

// csrf reads the host's double-submit token. Empty when the seam is unwired,
// which the render path has already refused on, so this never silently produces
// a form that would be rejected.
func (h *Handlers) csrf(c *gin.Context) string {
	if deps == nil || deps.CSRFToken == nil {
		return ""
	}
	return deps.CSRFToken(c)
}

// Download returns the .torrent with this member's passkey in the announce URL.
//
// The stored info_bytes are spliced in UNCHANGED. That is the whole reason they are
// stored as bytes: the info_hash is their hash, so re-encoding the dict — even to
// something semantically identical — would change the hash and produce a torrent
// the swarm is not tracking.
func (h *Handlers) Download(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok := h.viewer(c)
	if !ok {
		return
	}
	infoHash := strings.ToLower(c.Param("info_hash"))
	// 40 hex characters or it is not an info_hash. Checked before the lookup so a
	// malformed request cannot become a query.
	if len(infoHash) != 40 {
		c.String(http.StatusBadRequest, "bad info hash")
		return
	}
	t, err := h.store.Torrent(ctx, infoHash)
	if err != nil || t == nil {
		c.Status(http.StatusNotFound)
		return
	}
	pk, err := h.passkeyFor(ctx, userID)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	blob := BuildForUser(t.InfoBytes, h.announceURL(pk))
	c.Header("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s.torrent"`, sanitizeFilename(t.Name)))
	c.Data(http.StatusOK, "application/x-bittorrent", blob)
}

// RotatePasskey mints a new passkey, invalidating every .torrent the member has
// already downloaded.
func (h *Handlers) RotatePasskey(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok := h.viewer(c)
	if !ok {
		return
	}
	pk, err := generatePasskey()
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	if err := h.store.SetPasskey(ctx, userID, pk); err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	// The warning is part of the response, not decoration: rotating silently
	// breaks every .torrent the member is currently seeding, and they will
	// otherwise discover it as clients reporting the tracker as dead.
	c.JSON(http.StatusOK, gin.H{
		"passkey":      pk,
		"announce_url": h.announceURL(pk),
		"warning":      "Every .torrent you downloaded before now must be re-downloaded — their announce URLs carry the old passkey.",
	})
}

// viewer resolves the signed-in member, or writes the failure and returns false.
//
// These routes already sit behind RequireUser and the entitlement gate, so a
// missing user here means the middleware chain was wired wrongly rather than that
// a stranger arrived — hence 500 rather than a redirect. A redirect would send a
// logged-in member to /login and loop.
func (h *Handlers) viewer(c *gin.Context) (int64, bool) {
	u, ok := h.currentUser(c)
	if !ok || u == nil {
		c.String(http.StatusInternalServerError, "tracker: no user on a gated route")
		return 0, false
	}
	return u.ID, true
}

// passkeyFor returns the member's passkey, minting one on first use.
//
// THE FIX over the host version. The host read u.Passkey off the user row, which
// was empty for every member because registration never minted one — so the page
// rendered an announce URL ending in nothing and the .torrent it produced could
// not announce at all. Minting on demand means the first visit is correct.
func (h *Handlers) passkeyFor(ctx context.Context, userID int64) (string, error) {
	pk, ok, err := h.store.Passkey(ctx, userID)
	if err != nil {
		return "", err
	}
	if ok && pk != "" {
		return pk, nil
	}
	pk, err = generatePasskey()
	if err != nil {
		return "", err
	}
	if err := h.store.SetPasskey(ctx, userID, pk); err != nil {
		return "", err
	}
	return pk, nil
}

func (h *Handlers) announceURL(passkey string) string {
	return h.siteURL + "/api/tracker/announce/" + passkey
}

// generatePasskey mints 32 hex characters from crypto/rand.
//
// crypto/rand and not math/rand: a guessable passkey is someone else's ratio, and
// on a private tracker it is also an invitation. An error is returned rather than
// falling back to a weaker source, because a passkey nobody can predict is the
// only kind worth having.
func generatePasskey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// sanitizeFilename strips characters that break Content-Disposition or confuse
// filesystems. Not a security control — the member sees this in their download
// dialog, and the bytes it names are already theirs.
func sanitizeFilename(s string) string {
	const bad = `/\"<>|:*?`
	out := strings.Map(func(r rune) rune {
		if strings.ContainsRune(bad, r) || r < 0x20 {
			return '_'
		}
		return r
	}, s)
	if len(out) > 120 {
		out = out[:120]
	}
	if out == "" {
		out = "torrent"
	}
	return out
}

// TorrentPage is one torrent's own page.
//
// It exists because there was no such thing: the tracker was a flat list, so
// every other surface that wanted to act on ONE torrent had to be reached by
// pasting its info-hash into a form. Casting magic was the case that made it
// obvious — a member had to copy a 40-character hex string out of a link.
func (h *Handlers) TorrentPage(c *gin.Context) {
	ctx := c.Request.Context()
	userID, ok := h.viewer(c)
	if !ok {
		return
	}
	// Lowercased and length-checked before it reaches the store: the hash is a
	// path segment, and a page that 500s on a typo is a page a crawler can
	// fill the error log with.
	//
	// A hash that is not one, or names nothing, sends the reader to the LIST
	// rather than answering with a page. It is where they were going next
	// anyway — a stale bookmark or a torrent since removed is not an error
	// they can act on — and a bare-text 404 in the middle of a themed site
	// reads as the site being broken rather than as the torrent being gone.
	hash := strings.ToLower(strings.TrimSpace(c.Param("info_hash")))
	if len(hash) != 40 {
		c.Redirect(http.StatusFound, "/tracker")
		return
	}
	t, err := h.store.Torrent(ctx, hash)
	if err != nil || t == nil {
		c.Redirect(http.StatusFound, "/tracker")
		return
	}
	pk, err := h.passkeyFor(ctx, userID)
	if err != nil {
		c.String(http.StatusInternalServerError, "passkey: %v", err)
		return
	}
	data := PageData{
		Torrent: t, Passkey: pk,
		AnnounceURL: h.announceURL(pk), CSRFToken: h.csrf(c),
	}
	if h.promotions != nil {
		data.HasMagic = true
		// Best effort: a promotions read that fails costs its panel, never the
		// page. The torrent's own facts are what the reader came for.
		if list, perr := h.promotions(ctx, hash); perr == nil {
			data.Promotions = list
		}
	}
	h.render(c, "tracker_torrent.html", t.Name, data)
}
