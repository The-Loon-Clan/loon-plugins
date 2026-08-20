package cosmetics

import (
	"bytes"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// effectVM is one row on the member's page.
type effectVM struct {
	Slug        string
	Label       string
	Description string
	Class       string
	Tinted      bool
	Animated    bool

	// Owned and Worn drive the controls. Separate because the page shows the
	// WHOLE catalogue, not just what you have: a shop that only lists what you
	// already own sells nothing, and seeing the eight of them side by side —
	// each drawn as it will actually look — is the only honest way to choose.
	Owned   bool
	Worn    bool
	Expires *time.Time
}

func (p *Plugin) page(c *gin.Context) (template.HTML, error) {
	u, ok := p.core.Auth.CurrentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return "", nil
	}
	ctx := c.Request.Context()

	owned, err := p.st.OwnedBy(ctx, u.ID)
	if err != nil {
		return "", err
	}
	worn, err := p.st.EquippedBy(ctx, u.ID, SlotName)
	if err != nil {
		return "", err
	}
	have := make(map[string]Owned, len(owned))
	for _, o := range owned {
		have[o.Slug] = o
	}

	// The catalogue's order, not the order things were bought. It groups the
	// still effects before the moving ones, which is the axis somebody
	// choosing actually cares about.
	rows := make([]effectVM, 0, len(pluginapi.Effects))
	for _, e := range pluginapi.Effects {
		o, mine := have[e.Slug]
		rows = append(rows, effectVM{
			Slug: e.Slug, Label: e.Label, Description: e.Description,
			Class: pluginapi.EffectClass(e.Slug),
			Tinted: e.Tinted, Animated: e.Animated,
			Owned: mine, Worn: e.Slug == worn, Expires: o.ExpiresAt,
		})
	}
	return p.render("cosmetics_page.html", map[string]any{
		"CSRF": pluginapi.CSRFToken(p.core, c),
		// The viewer's OWN name in every preview, because a swatch of the word
		// "Sparkle" tells you nothing about what your name will look like —
		// and the length of a name is most of how an effect reads.
		"Name":    u.Username,
		"Effects": rows,
		"Wearing": worn != "",
		"Err":     c.Query("err"),
	})
}

// equip puts one on or takes it off.
func (p *Plugin) equip(c *gin.Context) (template.HTML, error) {
	u, ok := p.core.Auth.CurrentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return "", nil
	}
	slug := strings.TrimSpace(c.PostForm("slug"))
	// Empty is "take it off" and is always allowed. Anything else is checked
	// against the catalogue here and against OWNERSHIP in the statement, so a
	// forged slug can neither be stored nor rendered.
	if slug != "" {
		if _, ok := pluginapi.EffectBySlug(slug); !ok {
			c.Redirect(http.StatusSeeOther, pagePath+"?err=unknown")
			return "", nil
		}
	}
	done, err := p.st.Equip(c.Request.Context(), u.ID, SlotName, slug)
	switch {
	case err != nil:
		c.Redirect(http.StatusSeeOther, pagePath+"?err=failed")
	case !done:
		// The statement wrote nothing, which means they do not own it (or it
		// lapsed between the page rendering and the click). Said plainly:
		// this is the one refusal a member can act on.
		c.Redirect(http.StatusSeeOther, pagePath+"?err=notowned")
	default:
		c.Redirect(http.StatusSeeOther, pagePath)
	}
	return "", nil
}

func (p *Plugin) render(name string, data any) (template.HTML, error) {
	var buf bytes.Buffer
	if err := p.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

func tmplFuncs() template.FuncMap {
	return template.FuncMap{
		"date": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.Format("2 Jan 2006")
		},
	}
}
