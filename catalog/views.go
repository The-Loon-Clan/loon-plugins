package catalog

import (
	"bytes"
	"context"
	"html/template"
	"net/http"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// The catalog plugin contributes a "Categories" section to the host's
// /admin/settings page (loon's admin.settings view slot): every Newznab
// category listed, each with an enable/disable toggle — "list everything, pick
// what to index".

func (p *Plugin) registerViews(c *core.Core) error {
	t, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		return err
	}
	p.tmpl = t
	return c.RegisterView(core.View{
		Slug: "catalog", Title: "Categories", Slot: core.SlotAdminSettings,
		Render: func(gc *gin.Context) (template.HTML, error) {
			return p.renderSettings(gc.Request.Context(), gc.Query("msg"), csrfToken(gc))
		},
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"toggle": p.actionToggle,
		},
	})
}

type categoryVM struct {
	ID      int
	Name    string
	Subcats string
	Enabled bool
}

func (p *Plugin) renderSettings(ctx context.Context, msg, csrf string) (template.HTML, error) {
	disabled, err := p.st.DisabledSet(ctx)
	if err != nil {
		return "", err
	}
	rows := make([]categoryVM, len(taxonomy))
	for i, c := range taxonomy {
		subs := ""
		for j, s := range c.Subcats {
			if j > 0 {
				subs += ", "
			}
			subs += s.Name
		}
		rows[i] = categoryVM{ID: c.ID, Name: c.Name, Subcats: subs, Enabled: !disabled[c.ID]}
	}
	var buf bytes.Buffer
	if err := p.tmpl.ExecuteTemplate(&buf, "settings.html", map[string]any{"Categories": rows, "Msg": msg, "CSRFToken": csrf}); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

func (p *Plugin) actionToggle(gc *gin.Context) (template.HTML, error) {
	id, _ := strconv.Atoi(gc.PostForm("id"))
	if id > 0 {
		if err := p.st.SetEnabled(gc.Request.Context(), id, gc.PostForm("enabled") == "true"); err != nil {
			return "", err
		}
	}
	gc.Redirect(http.StatusSeeOther, "/admin/settings?msg="+url.QueryEscape("categories updated")+"#s-catalog")
	return "", nil
}
