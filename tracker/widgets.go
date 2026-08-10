package tracker

import (
	"fmt"
	"html/template"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// The tracker's placeable widgets.
//
// These read the tracker's OWN tables through its own store, which is the
// point. The host previously carried the same figures by querying
// tracker.user_stats and tracker.torrents directly: it worked, and it meant the
// host hardcoded this plugin's schema. A column rename here would have turned
// those surfaces silently blank — the query errors, the host reads that as "no
// data", and the figures just stop appearing.
//
// Registered from Provision, which returns EARLY when the tracker is disabled
// or has no Redis. So on a site without a tracker these are not merely empty,
// they are not offered in the placement editor at all — an operator is never
// shown a widget that could not work.
//
// Each renders an empty fragment when it has nothing to say, and the host drops
// it rather than drawing an empty box.

// registerWidgets publishes them. Errors are logged by the caller's convention
// rather than failing Provision: a widget that could not register is a missing
// card, not a reason to refuse to run a tracker.
func (p *Plugin) registerWidgets(c *core.Core) {
	// ── the viewer's own standing ───────────────────────────────────────────
	//
	// Not Public: a ratio is the viewer's own business, and the host applies
	// this per request. An operator placing it in the footer publishes it to
	// members, never to anonymous readers.
	_ = c.RegisterWidget(core.Widget{
		Slug:        "tracker-standing",
		Title:       "Your tracker standing",
		Description: "Upload, download and ratio for the signed-in member.",
		Weight:      10,
		Render: func(gc *gin.Context) (template.HTML, error) {
			if c.Auth == nil {
				return "", nil
			}
			u, ok := c.Auth.CurrentUser(gc)
			if !ok || u == nil {
				return "", nil
			}
			t, err := p.store.Totals(gc.Request.Context(), u.ID)
			if err != nil {
				return "", nil
			}
			// A member who has never announced has nothing to show. Zeroes
			// would read as a ratio of nought rather than as "not started".
			if t.Uploaded == 0 && t.Downloaded == 0 && t.Seeding == 0 && t.Leeching == 0 {
				return "", nil
			}
			return kv(
				row("Uploaded", humanBytes(t.Uploaded)),
				row("Downloaded", humanBytes(t.Downloaded)),
				row("Ratio", ratioLabel(t)),
				rowInt("Seeding", t.Seeding),
			), nil
		},
	})

	// ── the swarm, site-wide ────────────────────────────────────────────────
	//
	// Public: it describes the tracker rather than a member.
	_ = c.RegisterWidget(core.Widget{
		Slug:        "tracker-swarm",
		Title:       "Tracker",
		Description: "Site-wide torrent, peer and snatch totals.",
		Public:      true,
		Weight:      20,
		Render: func(gc *gin.Context) (template.HTML, error) {
			// limit 0 takes the store's default page; the TOTAL is what matters
			// and the rows carry the counters to sum.
			rows, total, err := p.store.ListTorrents(gc.Request.Context(), 500, 0)
			if err != nil || total == 0 {
				return "", nil
			}
			var seed, leech, snatch int
			for _, t := range rows {
				seed += t.Seeders
				leech += t.Leechers
				snatch += t.Snatches
			}
			return kv(
				rowInt("Torrents", total),
				rowInt("Seeders", seed),
				rowInt("Leechers", leech),
				rowInt("Snatches", snatch),
			), nil
		},
	})

	// ── the swarm for the release being viewed ──────────────────────────────
	//
	// Regions is stated, unusually, because this widget is meaningless anywhere
	// the host has not said what the page is about. Narrowing keeps the editor
	// from offering it for the footer, where it could only render nothing.
	_ = c.RegisterWidget(core.Widget{
		Slug:        "release-swarm",
		Title:       "Swarm",
		Description: "Seeders and leechers for the release being viewed.",
		Public:      true,
		Regions:     []string{"release", "sidebar-left", "sidebar-right"},
		Weight:      30,
		Render: func(gc *gin.Context) (template.HTML, error) {
			ref, ok := core.WidgetItem(gc)
			// The KIND is checked, not just the id: an id alone is how a
			// release widget renders against a forum thread id and shows a
			// confidently wrong swarm.
			if !ok || ref.Kind != "release" {
				return "", nil
			}
			t, err := p.store.TorrentByNzbID(gc.Request.Context(), ref.ID)
			if err != nil || t == nil {
				return "", nil
			}
			return kv(
				rowInt("Seeders", t.Seeders),
				rowInt("Leechers", t.Leechers),
				rowInt("Snatches", t.Snatches),
			), nil
		},
	})
}

// ── fragment helpers ────────────────────────────────────────────────────────
//
// A widget returns a FRAGMENT with no layout, so these build the host's
// key-value shape directly. Values are escaped at the point of interpolation:
// the counters are ints and cannot carry markup, but the byte figures go
// through the escaper anyway rather than relying on that staying true.

func kv(rows ...string) template.HTML {
	out := `<dl class="key-value">`
	for _, r := range rows {
		out += r
	}
	return template.HTML(out + `</dl>`)
}

func row(label, value string) string {
	return fmt.Sprintf(`<div class="key-value__group"><dt>%s</dt><dd>%s</dd></div>`,
		template.HTMLEscapeString(label), template.HTMLEscapeString(value))
}

func rowInt(label string, n int) string {
	return fmt.Sprintf(`<div class="key-value__group"><dt>%s</dt><dd>%d</dd></div>`,
		template.HTMLEscapeString(label), n)
}

// ratioLabel renders Totals.Ratio the way a tracker does: two decimals, and the
// infinity sign for a member who has uploaded without ever downloading, because
// rendering their byte count as a ratio reads as a bug.
func ratioLabel(t Totals) string {
	if t.Downloaded == 0 {
		if t.Uploaded == 0 {
			return "—"
		}
		return "∞"
	}
	return fmt.Sprintf("%.2f", t.Ratio())
}
