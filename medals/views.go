package medals

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// registerViews publishes the member page and the admin page.
func (p *Plugin) registerViews(c *core.Core) error {
	if err := c.RegisterView(core.View{
		Slug: "medals", Title: "Medals", Slot: core.SlotSitePage,
		MinRole: core.RoleUser,
		Nav:     core.NavHint{Group: "Community"},
		Render:  p.renderMedals,
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"buy":  p.actionBuy,
			"wear": p.actionWear,
		},
	}); err != nil {
		return err
	}
	return c.RegisterView(core.View{
		Slug: "medals", Title: "Medals", Slot: core.SlotAdminPage,
		Description: "The medal catalogue: what exists, what it costs, what it looks like.",
		MinRole:     core.RoleAdmin,
		Nav:         core.NavHint{Group: "Community"},
		Render:      p.renderAdmin,
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"create": p.actionCreate,
			"update": p.actionUpdate,
			"toggle": p.actionToggle,
			"delete": p.actionDelete,
		},
	})
}

// ── the member page ─────────────────────────────────────────────────────

// medalVM is one medal as the page shows it.
type medalVM struct {
	Medal
	Description string // localized
	Owned       bool
	Shown       bool
}

func (p *Plugin) renderMedals(gc *gin.Context) (template.HTML, error) {
	ctx := gc.Request.Context()
	list, err := p.st.List(ctx, true)
	if err != nil {
		return "", err
	}
	owned := map[int64]bool{}
	if u, ok := p.core.Auth.CurrentUser(gc); ok && u != nil {
		if o, err := p.st.Owned(ctx, u.ID); err == nil {
			owned = o
		}
	}
	var vms []medalVM
	for _, m := range list {
		vm := medalVM{Medal: m, Description: p.localizedDescription(gc, m)}
		if shown, ok := owned[m.ID]; ok {
			vm.Owned, vm.Shown = true, shown
		}
		vms = append(vms, vm)
	}
	var buf bytes.Buffer
	if err := p.tmpl.ExecuteTemplate(&buf, "medals.html", map[string]any{
		"Medals": vms,
		"Msg":    gc.Query("msg"),
		"Err":    gc.Query("err"),
		// The name a message interpolates, carried separately so the SENTENCE
		// stays in the template. See the note at the top of the template on
		// why a concatenated sentence cannot be translated.
		"MsgName":   gc.Query("name"),
		"CSRFToken": p.csrfToken(gc),
	}); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

func (p *Plugin) actionBuy(gc *gin.Context) (template.HTML, error) {
	u, ok := p.core.Auth.CurrentUser(gc)
	if !ok || u == nil {
		gc.Redirect(http.StatusSeeOther, "/login")
		return "", nil
	}
	ctx := gc.Request.Context()
	id, _ := strconv.ParseInt(gc.PostForm("id"), 10, 64)
	m, err := p.st.Get(ctx, id)
	if err != nil || !m.Enabled || m.Price <= 0 {
		gc.Redirect(http.StatusSeeOther, "/p/medals?err="+"notforsale")
		return "", nil
	}
	owned, err := p.st.Owned(ctx, u.ID)
	if err == nil {
		if _, has := owned[m.ID]; has {
			gc.Redirect(http.StatusSeeOther, "/p/medals?err="+"alreadyheld")
			return "", nil
		}
	}
	// Deduct, then grant; a failed grant refunds — the store's unwind.
	if _, err := p.core.Points.Deduct(ctx, u.ID, int(m.Price), "spend_medal",
		fmt.Sprintf("Bought the %q medal", m.Name), m.ID); err != nil {
		gc.Redirect(http.StatusSeeOther, "/p/medals?err="+"toopoor")
		return "", nil
	}
	if err := p.st.Grant(ctx, u.ID, m.ID); err != nil {
		if _, rerr := p.core.Points.Refund(ctx, u.ID, int(m.Price), "spend_medal",
			"Medal purchase failed — refunded", m.ID); rerr != nil {
			return "", rerr
		}
		gc.Redirect(http.StatusSeeOther, "/p/medals?err="+"failed")
		return "", nil
	}
	gc.Redirect(http.StatusSeeOther, "/p/medals?msg="+"bought&name="+url.QueryEscape(m.Name))
	return "", nil
}

// actionWear saves the whole cabinet's checkboxes in one submit — the
// i18n-grid shape: wearing is arranged, not clicked one at a time.
func (p *Plugin) actionWear(gc *gin.Context) (template.HTML, error) {
	u, ok := p.core.Auth.CurrentUser(gc)
	if !ok || u == nil {
		gc.Redirect(http.StatusSeeOther, "/login")
		return "", nil
	}
	if err := gc.Request.ParseForm(); err == nil {
		ctx := gc.Request.Context()
		owned, err := p.st.Owned(ctx, u.ID)
		if err == nil {
			for id := range owned {
				shown := gc.Request.PostForm.Get("wear/"+strconv.FormatInt(id, 10)) != ""
				if err := p.st.SetShown(ctx, u.ID, id, shown); err != nil {
					gc.Redirect(http.StatusSeeOther, "/p/medals?err="+"savefailed")
					return "", nil
				}
			}
		}
	}
	gc.Redirect(http.StatusSeeOther, "/p/medals?msg="+"saved")
	return "", nil
}

// ── the admin page ──────────────────────────────────────────────────────

func (p *Plugin) renderAdmin(gc *gin.Context) (template.HTML, error) {
	ctx := gc.Request.Context()
	list, err := p.st.List(ctx, false)
	if err != nil {
		return "", err
	}
	var slugs []string
	if p.l10nSlugs != nil {
		slugs, _ = p.l10nSlugs(ctx)
	}
	var buf bytes.Buffer
	if err := p.tmpl.ExecuteTemplate(&buf, "medals_admin.html", map[string]any{
		"Medals":    list,
		"L10nSlugs": slugs,
		"Icons":     p.iconChoices(),
		"Msg":       gc.Query("msg"),
		"Err":       gc.Query("err"),
		// The name a message interpolates, carried separately so the SENTENCE
		// stays in the template. See the note at the top of the template on
		// why a concatenated sentence cannot be translated.
		"MsgName":   gc.Query("name"),
		"CSRFToken": p.csrfToken(gc),
	}); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

func (p *Plugin) actionCreate(gc *gin.Context) (template.HTML, error) {
	m := Medal{
		Slug:            strings.TrimSpace(gc.PostForm("slug")),
		Name:            strings.TrimSpace(gc.PostForm("name")),
		Icon:            strings.TrimSpace(gc.PostForm("icon")),
		Description:     strings.TrimSpace(gc.PostForm("description")),
		DescriptionSlug: strings.TrimSpace(gc.PostForm("description_slug")),
		Enabled:         true,
	}
	if !slugPattern.MatchString(m.Slug) {
		gc.Redirect(http.StatusSeeOther, "/admin/p/medals?err="+"badslug")
		return "", nil
	}
	if m.Name == "" {
		m.Name = m.Slug
	}
	if n, err := strconv.Atoi(strings.TrimSpace(gc.PostForm("bonus_pct"))); err == nil && n >= 0 {
		m.BonusPct = n
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(gc.PostForm("price")), 10, 64); err == nil && n >= 0 {
		m.Price = n
	}
	if n, err := strconv.Atoi(strings.TrimSpace(gc.PostForm("ordinal"))); err == nil {
		m.Ordinal = n
	}
	if err := p.st.Create(gc.Request.Context(), m); err != nil {
		gc.Redirect(http.StatusSeeOther, "/admin/p/medals?err="+"createfailed")
		return "", nil
	}
	gc.Redirect(http.StatusSeeOther, "/admin/p/medals?msg="+"created&name="+url.QueryEscape(m.Slug))
	return "", nil
}

// actionUpdate edits an existing medal. It exists because the icon picker
// below is useless without it: every medal a site already has was created
// before the picker, and "delete and recreate" throws away every holder's row.
func (p *Plugin) actionUpdate(gc *gin.Context) (template.HTML, error) {
	id, _ := strconv.ParseInt(gc.PostForm("id"), 10, 64)
	if id <= 0 {
		gc.Redirect(http.StatusSeeOther, "/admin/p/medals?err="+"nosuch")
		return "", nil
	}
	cur, err := p.st.Get(gc.Request.Context(), id)
	if err != nil {
		gc.Redirect(http.StatusSeeOther, "/admin/p/medals?err="+"nosuch")
		return "", nil
	}
	// Read over the CURRENT row rather than building a fresh one: the edit
	// form carries the fields it edits, and a missing field must leave the
	// medal alone rather than blanking it.
	if v, ok := gc.GetPostForm("name"); ok && strings.TrimSpace(v) != "" {
		cur.Name = strings.TrimSpace(v)
	}
	if v, ok := gc.GetPostForm("icon"); ok {
		cur.Icon = strings.TrimSpace(v)
	}
	if v, ok := gc.GetPostForm("description"); ok {
		cur.Description = strings.TrimSpace(v)
	}
	if v, ok := gc.GetPostForm("description_slug"); ok {
		cur.DescriptionSlug = strings.TrimSpace(v)
	}
	if n, err := strconv.Atoi(strings.TrimSpace(gc.PostForm("bonus_pct"))); err == nil && n >= 0 {
		cur.BonusPct = n
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(gc.PostForm("price")), 10, 64); err == nil && n >= 0 {
		cur.Price = n
	}
	if n, err := strconv.Atoi(strings.TrimSpace(gc.PostForm("ordinal"))); err == nil {
		cur.Ordinal = n
	}
	if err := p.st.Update(gc.Request.Context(), cur); err != nil {
		gc.Redirect(http.StatusSeeOther, "/admin/p/medals?err="+"savefailed")
		return "", nil
	}
	gc.Redirect(http.StatusSeeOther, "/admin/p/medals?msg="+"savedone&name="+url.QueryEscape(cur.Slug))
	return "", nil
}

func (p *Plugin) actionToggle(gc *gin.Context) (template.HTML, error) {
	id, _ := strconv.ParseInt(gc.PostForm("id"), 10, 64)
	on := gc.PostForm("on") == "1"
	if id > 0 {
		if err := p.st.SetEnabled(gc.Request.Context(), id, on); err != nil {
			gc.Redirect(http.StatusSeeOther, "/admin/p/medals?err="+url.QueryEscape("toggle failed"))
			return "", nil
		}
	}
	gc.Redirect(http.StatusSeeOther, "/admin/p/medals?msg="+url.QueryEscape("Saved"))
	return "", nil
}

func (p *Plugin) actionDelete(gc *gin.Context) (template.HTML, error) {
	id, _ := strconv.ParseInt(gc.PostForm("id"), 10, 64)
	if id > 0 {
		if err := p.st.Delete(gc.Request.Context(), id); err != nil {
			gc.Redirect(http.StatusSeeOther, "/admin/p/medals?err="+url.QueryEscape("delete failed"))
			return "", nil
		}
	}
	gc.Redirect(http.StatusSeeOther, "/admin/p/medals?msg="+url.QueryEscape("Deleted — holders' copies went with it"))
	return "", nil
}
