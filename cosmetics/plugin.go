// Package cosmetics sells what a username looks like.
//
// Every reward system on this site pays in NUMBERS — points, ratio, a rank you
// climb — and numbers are invisible to everybody except their owner. A
// cosmetic is the other kind of reward: it costs the site nothing, it grants no
// advantage, and it is the only one anybody else can see. That is the whole
// appeal, and it is why every tracker with a shop sells one.
//
// The host's user-tag template already carried the note that made this worth
// building: it lists donor sparkle backgrounds and group gradient effects as
// "deliberately NOT ported — none has a data source here, and inventing one
// would be fabrication". This plugin is that data source.
//
// WHERE THE HALVES LIVE. The catalogue of effects is in pluginapi, not here,
// because an effect is two halves in two repositories — this plugin sells and
// records it, the HOST draws it, and drawing it means CSS in the host's
// stylesheet. A slug they disagree about fails silently: the sale succeeds and
// the name renders plain. So the slug list is the contract, and a host test
// asserts its stylesheet covers every entry.
//
// WHAT IT DOES NOT DO. It does not let anybody type text. A custom title is
// user-supplied words rendered beside a name on every page they appear on,
// which is a moderation surface and a very different feature; this sells eight
// effects from a fixed list and cannot be made to say anything.
package cosmetics

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

//go:embed migrations/*.sql
var migrations embed.FS

//go:embed templates/*.html
var tmplFS embed.FS

func init() {
	core.RegisterPlugin("cosmetics", func() core.Plugin { return &Plugin{} })
}

// itemKind is the store's reward_type for a cosmetic, and the registry suffix
// under pluginapi.StoreItemTypePrefix.
const itemKind = "cosmetic"

// pagePath is the member's own page: what they own, and what they are wearing.
const pagePath = "/p/cosmetics"

type Plugin struct {
	core  *core.Core
	st    Store
	tmpl  *template.Template
	users core.UsersService
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "cosmetics",
		Version:     "0.1.0",
		Description: "Name effects, bought with points or granted with a standing. The only reward on the site anybody else can see.",
		Migrations:  migrations,
		Processes:   []string{"web"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	db := c.Storage.SchemaDB(p.Metadata().Name)
	if db == nil {
		return fmt.Errorf("cosmetics: Core.Storage.SchemaDB is nil")
	}
	p.st = NewPGStore(db)

	tmpl, err := template.New("cosmetics").Funcs(tmplFuncs()).ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		return fmt.Errorf("cosmetics: parsing templates: %w", err)
	}
	p.tmpl = tmpl

	// The read side, for the host's renderer. Registered in Provision because
	// the host resolves it once at boot and asks it per request.
	c.Register(pluginapi.CosmeticsName, resolver{p: p})

	// The buy side. One kind; the item's reward_ref decides whether a def sells
	// ONE effect or lets the member choose — see Describe.
	c.Register(pluginapi.StoreItemTypePrefix+itemKind, itemType{p: p})

	if err := c.RegisterView(core.View{
		Slug:        "cosmetics",
		Title:       "Name effects",
		Description: "What your name looks like. Wear one at a time.",
		Slot:        core.SlotSitePage,
		Nav:         core.NavHint{Group: "Account"},
		Render:      p.page,
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"equip": p.equip,
		},
	}); err != nil {
		return fmt.Errorf("cosmetics: register view: %w", err)
	}
	return nil
}

// Start resolves the user directory.
//
// In Start rather than Provision because it is a sibling capability and every
// Provision runs before any Start — asking earlier is how a lookup comes back
// absent for a plugin that is perfectly present.
func (p *Plugin) Start(ctx context.Context) error {
	if p.core == nil {
		return nil
	}
	p.users = p.core.Users
	if p.users == nil {
		// Fatal to the feature rather than to the site: the renderer keys on
		// USERNAME (the rendering site has a name and nothing else), so
		// without a directory to turn ids into names nothing can be drawn.
		log.Printf("cosmetics: no core.Users service — effects are recorded and " +
			"nothing will render. The host should wire one.")
	}
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }

// resolver is the read side the host renders through.
type resolver struct{ p *Plugin }

var _ pluginapi.CosmeticResolver = resolver{}

// EquippedNames returns username -> effect slug for everybody wearing one.
//
// Two queries and no join across a schema boundary: the equipped map comes from
// this plugin's tables, the names from the host's user directory. A view that
// joined them would be this plugin reaching into the host's users table, which
// is precisely what core.UsersService exists to stop.
func (r resolver) EquippedNames(ctx context.Context) (map[string]string, error) {
	p := r.p
	if p.st == nil || p.users == nil {
		return nil, nil
	}
	byID, err := p.st.LiveEquipped(ctx, SlotName)
	if err != nil {
		return nil, err
	}
	if len(byID) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	names, err := p.users.BulkDisplayNames(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("cosmetics: resolving names: %w", err)
	}
	out := make(map[string]string, len(byID))
	for id, slug := range byID {
		// A member the directory did not return is skipped rather than keyed
		// under an empty string, which would put an effect on every name that
		// failed to resolve.
		if name := names[id]; name != "" {
			out[name] = slug
		}
	}
	return out, nil
}
