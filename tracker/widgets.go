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
			// Icons rather than words. This is the shape every tracker's top
			// bar has, and it is the only shape that fits one: four labelled
			// pairs is a paragraph, four icons is a glance.
			//
			// Each figure still carries its label as visually-hidden text and
			// a title. An icon-only readout is unreadable to a screen reader
			// and ambiguous to anyone who has not seen a tracker before, and
			// neither costs a pixel to fix.
			return figures(
				figure("up", "chevron-up", "Uploaded", humanBytes(t.Uploaded)),
				figure("down", "chevron-down", "Downloaded", humanBytes(t.Downloaded)),
				figure("ratio", "shield", "Ratio", ratioLabel(t)),
				figure("seed", "users", "Seeding", fmt.Sprintf("%d", t.Seeding)),
			), nil
		},
	})

	// ── the member's announce URL ───────────────────────────────────────────
	//
	// The most-copied string on a private tracker, and until now it lived on
	// one page a member had to go and find. As a widget they can keep it where
	// they actually work.
	//
	// It READS a passkey and never mints one. passkeyFor on the member page
	// creates one on first use, which is right for a page somebody deliberately
	// opened, and wrong for a widget that could be rendered into the footer of
	// every page on the site: a member who has never touched the tracker should
	// not be issued a credential by walking past it.
	_ = c.RegisterWidget(core.Widget{
		Slug:        "tracker-announce",
		Title:       "Your announce URL",
		Description: "The passkey URL to paste into a torrent client.",
		Weight:      12,
		Render: func(gc *gin.Context) (template.HTML, error) {
			if c.Auth == nil {
				return "", nil
			}
			u, ok := c.Auth.CurrentUser(gc)
			if !ok || u == nil {
				return "", nil
			}
			pk, has, err := p.store.Passkey(gc.Request.Context(), u.ID)
			if err != nil || !has || pk == "" {
				// No passkey yet — say where to get one rather than minting it
				// here. The tracker page mints on arrival.
				return template.HTML(
					`<p class="text-muted">No passkey yet. ` +
						`<a href="/tracker">Open the tracker</a> to get one.</p>`), nil
			}
			url := p.cfg.SiteURL + "/api/tracker/announce/" + pk
			// <code> and nothing clever. A member selects and copies this, and
			// a copy button would need JavaScript on a site that has none.
			return template.HTML(fmt.Sprintf(
				`<p class="text-muted small">Paste this into your client.</p>`+
					`<code class="announce-url">%s</code>`,
				template.HTMLEscapeString(url))), nil
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
			return figures(
				figure("torrents", "database", "Torrents", fmt.Sprintf("%d", total)),
				figure("up", "chevron-up", "Seeders", fmt.Sprintf("%d", seed)),
				figure("down", "chevron-down", "Leechers", fmt.Sprintf("%d", leech)),
				figure("seed", "download", "Snatches", fmt.Sprintf("%d", snatch)),
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
			return figures(
				figure("up", "chevron-up", "Seeders", fmt.Sprintf("%d", t.Seeders)),
				figure("down", "chevron-down", "Leechers", fmt.Sprintf("%d", t.Leechers)),
				figure("seed", "download", "Snatches", fmt.Sprintf("%d", t.Snatches)),
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

func figures(items ...string) template.HTML {
	out := `<div class="stat-figures">`
	for _, it := range items {
		out += it
	}
	return template.HTML(out + `</div>`)
}

// figure is one icon + value.
//
// kind names WHAT it is ("up", "down", "ratio"), not what colour to be. The
// host's stylesheet decides how each kind looks, so the figures follow the
// theme and a plugin never ships a literal colour — the same split the rest of
// this widget file keeps by emitting the host's class names rather than styles.
//
// The icon id must exist in the host's sprite. A missing symbol renders as
// nothing at all, silently, which is why these are limited to ids the host has
// carried since before this file existed.
func figure(kind, icon, label, value string) string {
	return fmt.Sprintf(
		`<span class="stat-figure stat-figure--%s" title="%s">`+
			`<svg class="icon" aria-hidden="true"><use href="#%s"></use></svg>`+
			`<span class="stat-figure__value">%s</span>`+
			`<span class="visually-hidden">%s</span>`+
			`</span>`,
		kind, template.HTMLEscapeString(label), icon,
		template.HTMLEscapeString(value), template.HTMLEscapeString(label))
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
