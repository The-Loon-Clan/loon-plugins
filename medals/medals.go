// Package medals is a loon plugin: the display cabinet. Collectible badges
// a member buys with points or is paid by a reward (rewards.payout.medal —
// the payout kind that was declared with no implementation anywhere), wears
// or quietly does not on their profile, and which MAY carry a bonus.
//
// The bonus is deliberately inert here. A medal row can name a percentage,
// the plugin publishes each member's summed WORN bonus under
// pluginapi.MedalBonusName — and nothing in this plugin applies it to
// anything. Some sites want a 5%-bonus medal, some want medals to be
// nothing but medals; that is a host decision, so the plugin only answers.
//
// What the plugin publishes:
//
//	medals.granter  pluginapi.MedalGranter — rewards' medal payouts land here
//	medals.worn     pluginapi.WornMedalsFunc — the profile's icon row
//	medals.bonus    pluginapi.MedalBonusFunc — the optional mechanics
//
// What it consumes (all host-registered before Boot, so Provision-safe):
//
//	medals.l10n.slugs / medals.l10n.resolve — the message catalogue, for
//	    localized descriptions (the achievements story)
//	medals.csrf — the host token every form embeds
package medals

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

//go:embed migrations/*.sql
var migrations embed.FS

//go:embed templates/*.html
var tmplFS embed.FS

// The host-registered seams this plugin consumes.
const (
	CSRFExtension      = "medals.csrf"
	L10nSlugsExtension = "medals.l10n.slugs"
	L10nExtension      = "medals.l10n.resolve"
	// IconsExtension is what icons THIS site has (func() []string), so the
	// admin form can offer them instead of asking an operator to type a name
	// they have no way to look up. Optional: without it the form falls back to
	// this plugin's own palette, which is the set the defaults come from
	// anyway — so the picker is never empty, just shorter.
	IconsExtension = "icons.catalogue"
)

func init() {
	core.RegisterPlugin("medals", func() core.Plugin { return &Plugin{} })
}

type Plugin struct {
	core      *core.Core
	st        *PGStore
	tmpl      *template.Template
	l10nSlugs func(context.Context) ([]string, error)
	l10n      func(*gin.Context, string) (string, bool)
	icons     func() []string
}

// iconChoices is what the admin picker offers: the host's whole icon set when
// it publishes one, this plugin's palette otherwise. Never empty — a picker
// with nothing in it is worse than the text box it replaced.
func (p *Plugin) iconChoices() []string {
	if p.icons != nil {
		if got := p.icons(); len(got) > 0 {
			return got
		}
	}
	return spritePalette
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "medals",
		Version:     "0.1.0",
		Description: "Collectible medals: buy or be awarded them, wear them on your profile; a medal may carry a bonus the host chooses to honour.",
		Migrations:  migrations,
		Processes:   []string{"web"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	db := c.Storage.SchemaDB(p.Metadata().Name)
	if db == nil || db.DB() == nil {
		return fmt.Errorf("medals: Core.Storage.SchemaDB is nil")
	}
	p.st = NewPGStore(db.DB())

	if err := p.parseTemplates(); err != nil {
		return err
	}

	// The localization seams, host-registered before Boot (Provision-safe).
	// Optional: a host without a catalogue simply has plain descriptions.
	if v, ok := c.Lookup(L10nSlugsExtension); ok {
		if fn, ok := v.(func(context.Context) ([]string, error)); ok {
			p.l10nSlugs = fn
		}
	}
	if v, ok := c.Lookup(L10nExtension); ok {
		if fn, ok := v.(func(*gin.Context, string) (string, bool)); ok {
			p.l10n = fn
		}
	}
	if v, ok := c.Lookup(IconsExtension); ok {
		if fn, ok := v.(func() []string); ok {
			p.icons = fn
		}
	}

	// The published answers. All three are ours to answer regardless of who
	// listens; the granter especially — the host's rewards.payout.medal
	// handler resolves it lazily at settle time, so order never matters.
	if err := c.Register(pluginapi.MedalGranterName, pluginapi.MedalGranter(granter{p.st})); err != nil {
		return fmt.Errorf("medals: register granter: %w", err)
	}
	if err := c.Register(pluginapi.WornMedalsName,
		pluginapi.WornMedalsFunc(func(ctx context.Context, userID int64) ([]pluginapi.WornMedal, error) {
			return p.st.Worn(ctx, userID)
		})); err != nil {
		return fmt.Errorf("medals: register worn: %w", err)
	}
	if err := c.Register(pluginapi.MedalBonusName,
		pluginapi.MedalBonusFunc(func(ctx context.Context, userID int64) (int, error) {
			return p.st.WornBonusPct(ctx, userID)
		})); err != nil {
		return fmt.Errorf("medals: register bonus: %w", err)
	}
	// And the same answer in the USER MULTIPLIER vocabulary: worn medals are
	// a source of the points dimension, combined additively with every other
	// bonus by pluginapi.ResolveMultiplier. A host that wants medals to be
	// nothing but medals sets every bonus_pct to 0 — the optionality moved
	// from "no consumer" to "no value", which an operator can see and set.
	if err := c.Register(pluginapi.MultiplierSourcePrefix+"medals",
		pluginapi.MultiplierSource(bonusSource{p.st})); err != nil {
		return fmt.Errorf("medals: register multiplier source: %w", err)
	}

	return p.registerViews(c)
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }

// bonusSource speaks the points dimension: 1 + the summed worn bonus.
type bonusSource struct{ st *PGStore }

func (b bonusSource) Factor(ctx context.Context, dim string, mc pluginapi.MultiplierContext) (float64, bool, error) {
	if dim != pluginapi.MultPoints {
		return 0, false, nil
	}
	pct, err := b.st.WornBonusPct(ctx, mc.UserID)
	if err != nil || pct == 0 {
		return 0, false, err
	}
	return 1 + float64(pct)/100, true, nil
}

// granter adapts the store to pluginapi.MedalGranter. Unknown slugs are the
// quiet no-op the payout tolerance expects; a repeat grant is the same
// nothing (ON CONFLICT DO NOTHING).
type granter struct{ st *PGStore }

func (g granter) GrantMedal(ctx context.Context, userID int64, slug string) error {
	granted, err := g.st.GrantBySlug(ctx, userID, slug)
	if err != nil {
		return err
	}
	if !granted {
		log.Printf("medals: payout targeted unknown or disabled medal %q — settled as a no-op", slug)
	}
	return nil
}

func (p *Plugin) csrfToken(gc *gin.Context) string {
	if v, ok := p.core.Lookup(CSRFExtension); ok {
		if fn, ok := v.(func(*gin.Context) string); ok {
			return fn(gc)
		}
	}
	return ""
}

// localizedDescription is the viewer's text: the catalogue when the medal
// names a slug the catalogue can answer, the plain description otherwise.
func (p *Plugin) localizedDescription(gc *gin.Context, m Medal) string {
	if m.DescriptionSlug != "" && p.l10n != nil {
		if t, ok := p.l10n(gc, m.DescriptionSlug); ok && t != "" {
			return t
		}
	}
	return m.Description
}

// parseTemplates builds the plugin's own set, with the two helpers the icon
// picker needs. A method rather than inline in Provision so a template test can
// render the same set the site does — the picker is one partial called from
// three places, which is exactly the shape that fails only at runtime.
func (p *Plugin) parseTemplates() error {
	t, err := template.New("medals").Funcs(template.FuncMap{
		// dict passes several values to a partial. The host's own templates
		// have the same helper under the same name; a partial taking a struct
		// instead would need a type per call site.
		"dict": func(kv ...any) (map[string]any, error) {
			if len(kv)%2 != 0 {
				return nil, fmt.Errorf("dict: odd argument count")
			}
			m := make(map[string]any, len(kv)/2)
			for i := 0; i < len(kv); i += 2 {
				k, ok := kv[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict: key %d is %T, not a string", i, kv[i])
				}
				m[k] = kv[i+1]
			}
			return m, nil
		},
		// hasIcon reports whether the picker already offers a value — the
		// guard that keeps an image URL set before the picker existed from
		// being silently replaced with a default the first time somebody opens
		// the form and saves it.
		"hasIcon": func(icons []string, want string) bool {
			for _, s := range icons {
				if s == want {
					return true
				}
			}
			return false
		},
	}).ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		return fmt.Errorf("medals: templates: %w", err)
	}
	p.tmpl = t
	return nil
}
