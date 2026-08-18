package achievements

import (
	"embed"
	"html/template"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

//go:embed templates/*.html
var viewFS embed.FS

// parseTemplates is split out so a test can render the pages without a Core.
func (p *Plugin) parseTemplates() error {
	t, err := template.New("").Funcs(template.FuncMap{
		"ts": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			return t.UTC().Format("2006-01-02 15:04")
		},
	}).ParseFS(viewFS, "templates/*.html")
	if err != nil {
		return err
	}
	p.tmpl = t
	return nil
}

// registerViews mounts the admin page.
//
// The view MOVED here from rewards with its slug, title and nav group intact,
// so the hub link and every bookmark keep working: achievements got their own
// page there because a definition page is a destination, and it stays one.
func (p *Plugin) registerViews(c *core.Core) error {
	return c.RegisterView(core.View{
		Slug: "achievements", Title: "Achievements", Slot: core.SlotAdminPage,
		Description: "Define what can be earned: criterion, value, and what the badge looks like.",
		Nav:         core.NavHint{Group: "Operations"},
		Render: func(gc *gin.Context) (template.HTML, error) {
			// The token rides in the ctx: renderAdminPage takes a
			// context.Context, and its two POST forms had none at all — so
			// creating and toggling an achievement each answered 403.
			return p.renderAdminPage(
				pluginapi.WithCSRF(gc.Request.Context(), pluginapi.CSRFToken(p.core, gc)),
				gc.Query("msg"), gc.Query("err"))
		},
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			// Create and enable are SEPARATE actions on purpose: enabling
			// backfills the achievement to everyone already past the
			// threshold, so one click must not both define and award it.
			"achievement-create": p.actionCreateAchievement,
			"achievement-update": p.actionUpdateAchievement,
			"achievement-toggle": p.actionToggleAchievement,
		},
	})
}
