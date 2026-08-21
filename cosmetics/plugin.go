// Package cosmetics sells what a member looks like.
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
// FOUR SLOTS: the username, the title under it, a frame around the picture, and
// the ground behind the profile card. A slot is a PLACE rather than a kind of
// effect, which is why the text effects fit two of them — an aura is an aura
// whether it is on a name or on a title, and somebody who owns one should be
// able to put it on either without buying it twice.
//
// AND ONE THING THAT IS NOT A COSMETIC AT ALL. A custom title is text somebody
// TYPED, published beside their name on every page they appear on, and it is
// the only part of this plugin with a moderation surface. So the shop sells the
// RIGHT to have one and staff pass the words: buying publishes nothing. See
// titles.go, which is also where the difference between checking characters and
// moderating is spelled out — no filter substitutes for a person reading it,
// and what the filter is for is the tricks that are about the rendering rather
// than the words.
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
const (
	itemKind  = "cosmetic"
	titleKind = "custom_title"
)

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
		Description: "Name effects, avatar frames, profile grounds and reviewed custom titles. The only rewards on the site anybody else can see.",
		Migrations:  migrations,
		Processes:   []string{"web"},
		Flavours:    []string{core.FlavourAny},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	db := c.Storage.SchemaDB(p.Metadata().Name)
	if db == nil {
		return fmt.Errorf("cosmetics: Core.Storage.SchemaDB is nil")
	}
	p.st = NewPGStore(db)

	if err := declareEvents(c); err != nil {
		return fmt.Errorf("cosmetics: declaring events: %w", err)
	}

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
	// The RIGHT to have a title, which is a different purchase from an effect:
	// it publishes nothing on its own, it unlocks a form whose output staff
	// review. Its own kind rather than a magic reward_ref on the one above,
	// because the two grant genuinely different things and the admin picking
	// from a dropdown should see both.
	c.Register(pluginapi.StoreItemTypePrefix+titleKind, titleItemType{p: p})

	if err := c.RegisterView(core.View{
		Slug:        "cosmetics",
		MinRole:     core.RoleUser,
		Title:       "Appearance",
		Description: "What you look like on the site — your name, your title, your picture, your profile.",
		Slot:        core.SlotSitePage,
		Nav:         core.NavHint{Group: "Account"},
		Render:      p.page,
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"equip": p.equip,
			"title": p.submitTitle,
		},
	}); err != nil {
		return fmt.Errorf("cosmetics: register view: %w", err)
	}
	// The queue. Titles are the only thing here that publishes words somebody
	// typed, so they are the only thing here with a staff surface.
	if err := c.RegisterView(core.View{
		Slug:        "titles",
		Title:       "Custom titles",
		Description: "Words members want under their name, waiting to be read.",
		Slot:        core.SlotAdminPage,
		MinRole:     core.RoleMod,
		Nav:         core.NavHint{Group: "Moderation"},
		Render:      p.queuePage,
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"review": p.review,
		},
	}); err != nil {
		return fmt.Errorf("cosmetics: register titles view: %w", err)
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

// Equipped returns username -> slot -> slug for everybody wearing anything.
//
// Two queries and no join across a schema boundary: the equipped map comes from
// this plugin's tables, the names from the host's user directory. A view that
// joined them would be this plugin reaching into the host's users table, which
// is precisely what core.UsersService exists to stop.
func (r resolver) Equipped(ctx context.Context) (map[string]pluginapi.Worn, error) {
	p := r.p
	if p.st == nil || p.users == nil {
		return nil, nil
	}
	byID, err := p.st.LiveEquipped(ctx)
	if err != nil {
		return nil, err
	}
	names, err := r.names(ctx, keysOf(byID))
	if err != nil {
		return nil, err
	}
	out := make(map[string]pluginapi.Worn, len(byID))
	for id, worn := range byID {
		if name := names[id]; name != "" {
			out[name] = pluginapi.Worn(worn)
		}
	}
	return out, nil
}

// ApprovedTitles returns username -> the words staff have passed.
func (r resolver) ApprovedTitles(ctx context.Context) (map[string]string, error) {
	p := r.p
	if p.st == nil || p.users == nil {
		return nil, nil
	}
	byID, err := p.st.ApprovedTitles(ctx)
	if err != nil {
		return nil, err
	}
	names, err := r.names(ctx, keysOf(byID))
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(byID))
	for id, text := range byID {
		if name := names[id]; name != "" {
			out[name] = text
		}
	}
	return out, nil
}

// names resolves ids through the host's directory.
//
// A member the directory did not return is skipped by every caller rather than
// keyed under an empty string, which would attach one person's cosmetics to
// every name that failed to resolve.
func (r resolver) names(ctx context.Context, ids []int64) (map[int64]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	names, err := r.p.users.BulkDisplayNames(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("cosmetics: resolving names: %w", err)
	}
	return names, nil
}

// keysOf is the ids of any map keyed by user id.
func keysOf[V any](m map[int64]V) []int64 {
	out := make([]int64, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	return out
}
