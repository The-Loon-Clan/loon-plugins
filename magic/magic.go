// Package magic is a loon plugin: torrent promotions, the NexusPHP "magic"
// tradition. A member spends points to cast a BUFF on a torrent — free
// leech, double upload, or a custom ratio pair — for themselves (private),
// for everyone (public), or for one named member, lasting a bounded number
// of hours. Every cast is history: a promotion cannot be edited once cast,
// only terminated by an admin, and the row stays either way.
//
// RESOLUTION is the genre's rule, spoken through the USER MULTIPLIER
// system (pluginapi.ResolveMultiplier): on a torrent, a member gets the HIGHEST
// upload factor and the LOWEST download factor across every active magic
// visible to them — a global promotion never overrides a private one with
// better numbers, and there is no limit to how many promotions a torrent
// carries. The tracker folds that answer into its announce crediting
// best-of with its other economies (perks' freeleech tokens), so the same
// rule holds across systems.
//
// LEVELS: casting is practice. Every point spent on magic is experience;
// levels make casting cheaper (a percentage discount) and the reach longer
// (higher duration caps), and unlock custom ratio pairs beyond the preset
// buffs. The curve lives in level.go where the tests can hold it still.
//
// The COST formula is a simplified cousin of the tradition's: a scope base
// (global casts cost most, private least), scaled by torrent size against
// the site's typical size, by the strength of the ratios asked for, and by
// the square root of the duration — so twice the hours costs √2, not 2.
package magic

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"math"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

//go:embed migrations/*.sql
var migrations embed.FS

//go:embed templates/*.html
var tmplFS embed.FS

// CSRFExtension is the host token seam, one key per plugin.
const CSRFExtension = "magic.csrf"

func init() {
	core.RegisterPlugin("magic", func() core.Plugin { return &Plugin{} })
}

type Plugin struct {
	core        *core.Core
	st          *PGStore
	tmpl        *template.Template
	torrentInfo pluginapi.TorrentInfoFunc // nil = names unshown, size factor 1
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "magic",
		Version:     "0.1.0",
		Description: "Torrent promotions: cast free leech / double upload / custom ratio buffs on a torrent, private or public, for points.",
		Migrations:  migrations,
		Processes:   []string{"web"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	db := c.Storage.SchemaDB(p.Metadata().Name)
	if db == nil || db.DB() == nil {
		return fmt.Errorf("magic: Core.Storage.SchemaDB is nil")
	}
	p.st = NewPGStore(db.DB())

	t, err := template.ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		return fmt.Errorf("magic: templates: %w", err)
	}
	p.tmpl = t

	// The one published answer, in the USER MULTIPLIER vocabulary: magic is
	// a source of upload/download factors, discovered by prefix and combined
	// with every other source (medals, whatever comes next) by
	// pluginapi.ResolveMultiplier — where the stacking rules live, once.
	if err := c.Register(pluginapi.MultiplierSourcePrefix+"magic",
		pluginapi.MultiplierSource(resolver{p.st})); err != nil {
		return fmt.Errorf("magic: register multiplier source: %w", err)
	}
	return p.registerViews(c)
}

// Start picks up the tracker's torrent-info seam — sibling capability, so
// Start, not Provision (the lesson this tree learned twice in one day).
func (p *Plugin) Start(ctx context.Context) error {
	if v, ok := p.core.Lookup(pluginapi.TorrentInfoName); ok {
		if fn, ok := v.(pluginapi.TorrentInfoFunc); ok {
			p.torrentInfo = fn
		}
	}
	if err := p.st.EnsureBuffDefs(ctx, classicBuffs); err != nil {
		return fmt.Errorf("magic: ensure buffs: %w", err)
	}
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }

// classicBuffs are the six promotions the genre ships everywhere, ensured
// by slug so an operator's edits survive and a new default reaches old
// databases.
var classicBuffs = []BuffDef{
	{Slug: "free", Name: "Free", UpRatio: 1, DownRatio: 0, Ordinal: 10},
	{Slug: "2x", Name: "2×", UpRatio: 2, DownRatio: 1, Ordinal: 20},
	{Slug: "2x-free", Name: "2× Free", UpRatio: 2, DownRatio: 0, Ordinal: 30},
	{Slug: "half", Name: "50%", UpRatio: 1, DownRatio: 0.5, Ordinal: 40},
	{Slug: "2x-half", Name: "2× 50%", UpRatio: 2, DownRatio: 0.5, Ordinal: 50},
	{Slug: "30", Name: "30%", UpRatio: 1, DownRatio: 0.3, Ordinal: 60},
}

// resolver answers the multiplier system: magic speaks the upload and
// download dimensions, from one indexed read. Errors and other dimensions
// are "no opinion" — earning never fails over a promotion.
type resolver struct{ st *PGStore }

func (r resolver) Factor(ctx context.Context, dim string, mc pluginapi.MultiplierContext) (float64, bool, error) {
	if (dim != pluginapi.MultUpload && dim != pluginapi.MultDownload) || mc.InfoHash == "" {
		return 0, false, nil
	}
	up, down, err := r.st.EffectiveRatios(ctx, mc.InfoHash, mc.UserID)
	if err != nil {
		return 0, false, err
	}
	if dim == pluginapi.MultUpload {
		return up, up != 1, nil
	}
	return down, down != 1, nil
}

// ── cost ────────────────────────────────────────────────────────────────

// castCost prices one cast, before the level discount.
//
// scopeBase × sizeFactor × ratioFactor × √hours, scaled down to demo-sized
// points. The ratio factor is the tradition's: strong upload promises cost
// super-linearly, deep download forgiveness costs quadratically — a 2×Free
// global for a week is a site event, and its price says so.
func castCost(cfg Config, scope string, sizeBytes int64, up, down float64, hours int) int64 {
	base := cfg.BaseSelf
	switch scope {
	case "all":
		base = cfg.BaseAll
	case "user":
		base = cfg.BaseUser
	}
	sizeF := 1.0
	if cfg.AvgSizeGB > 0 && sizeBytes > 0 {
		if r := float64(sizeBytes) / float64(cfg.AvgSizeGB<<30); r > 1 {
			sizeF = math.Sqrt(r)
		}
	}
	ratioF := 2*math.Pow(math.Max(up-1, 0), 1.5) + math.Pow(2*math.Abs(1-down), 2)
	cost := float64(base) * sizeF * ratioF * math.Sqrt(float64(hours)) / 40
	if cost < 1 {
		cost = 1
	}
	return int64(math.Ceil(cost))
}

// applyDiscount takes the level's percentage off, never below one point —
// magic is never free, or the history fills with noise casts.
func applyDiscount(cost int64, discountPct int) int64 {
	c := cost * int64(100-discountPct) / 100
	if c < 1 {
		return 1
	}
	return c
}

func (p *Plugin) csrfToken(gc *gin.Context) string {
	if v, ok := p.core.Lookup(CSRFExtension); ok {
		if fn, ok := v.(func(*gin.Context) string); ok {
			return fn(gc)
		}
	}
	return ""
}
