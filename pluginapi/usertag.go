// usertag.go is the one place a plugin asks the host to draw a username.
//
// WHY IT EXISTS. A member's cosmetics -- name effect, role colour, the link to
// their profile -- reach every listing the HOST draws, because the host has a
// `user-tag` partial and uses it. A plugin fragment does not use host
// templates: each plugin parses its own set, deliberately, so what a username
// LOOKS like stays the host's decision rather than eighteen plugins' decision.
//
// The consequence was that every plugin either reinvented the chip or did
// without. Counted on 23 Aug 2026: eighteen plugins render a username and four
// applied the effects, by four DIFFERENT mechanisms -- forum with four template
// funcs over a package-level Core, comments by resolving a class into its view
// model, playlists through a callback in its own Deps that only it declares,
// cosmetics on its own page. The other fourteen showed a plain name, so a
// member who bought a name effect saw it in some places and not others with no
// pattern they could learn.
//
// This is the seam playlists already had, moved somewhere every plugin can
// reach. It needs NO per-plugin wiring by the host: like the cosmetics helpers
// beside it, the call takes the Core and finds its own way.
package pluginapi

import (
	"html/template"
	"strings"

	"github.com/the-loon-clan/loon/core"
)

// UserTagName is the Core extension-registry key under which a host publishes
// its username renderer.
const UserTagName = "usertag.render"

// UserTag renders one member's name as this host draws it: role colour, any
// equipped name effect, and a link to their profile.
//
// A FUNC and not a string, for the reason IconCatalogue is: the answer changes
// when the member equips something, and a value read once at Provision would
// draw yesterday's cosmetics for the life of the process.
type UserTag func(name string) template.HTML

// UserTagRenderer resolves the host's renderer, if it published one.
func UserTagRenderer(c *core.Core) (UserTag, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.Lookup(UserTagName)
	if !ok {
		return nil, false
	}
	switch fn := v.(type) {
	case UserTag:
		return fn, true
	// The bare signature too, the way Icons tolerates func() []string: a
	// Lookup asserts on the EXACT type, so a named type and its underlying
	// type are two different registrations.
	case func(string) template.HTML:
		return UserTag(fn), true
	}
	return nil, false
}

// RenderUserTag is the call a plugin makes. Never fails, never panics, and
// always returns something a reader can click.
//
// THE FALLBACK IS THE POINT, not an afterthought. A host that publishes no
// renderer -- or one whose template errors -- gets a plain link to the profile:
// the information without the decoration. The alternative, returning "", makes
// a plugin that trusted this seam silently drop the author's name off its own
// page, which is a worse failure than a missing colour and one nobody would
// attribute to a cosmetics contract.
func RenderUserTag(c *core.Core, name string) template.HTML {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if fn, ok := UserTagRenderer(c); ok && fn != nil {
		if out := fn(name); out != "" {
			return out
		}
	}
	return plainUserTag(name)
}

// plainUserTag is the no-host, no-cosmetics form. Escaped by hand because it is
// assembled as a string: a username is member-controlled input, and the whole
// value of this function is that it is safe to drop into any plugin's markup.
func plainUserTag(name string) template.HTML {
	safeText := template.HTMLEscapeString(name)
	safeHref := template.HTMLEscapeString("/u/" + name)
	return template.HTML(`<a class="user-tag__link" href="` + safeHref + `">` + safeText + `</a>`)
}
