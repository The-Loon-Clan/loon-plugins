package forum

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// Admin category management — ported from the host's AdminHandler
// when the forum became a plugin. The category-list page doubles as
// the merge tool for the legacy duplicate categories (see migration
// 196: name gained UNIQUE after a schema_migrations gap-recovery
// re-ran the seed INSERT).

func (h *Handlers) AdminCategories(c *gin.Context) {
	cats, err := h.store.GetForumCategories(c.Request.Context())
	if err != nil {
		c.String(http.StatusInternalServerError, "list forum categories: %v", err)
		return
	}
	h.render(c, http.StatusOK, "Forum categories", "admin_forum_categories.html", gin.H{
		"Categories": cats,
		"Colors":     categoryColorList,
		"GateRoles":  gateRoleList,
		"Flash":      c.Query("msg"),
		"Err":        c.Query("err"),
	})
}

// categoryColorList is the closed palette the category color must come from
// — it becomes a CSS class suffix (forum-cat-icon-<color>), so a free-text
// value would be a junk class at best. Ordered for the admin form's select;
// the template receives it as "Colors" so form and validator cannot drift.
var categoryColorList = []string{"blue", "cyan", "green", "orange", "pink", "purple", "red", "yellow"}

var categoryColors = func() map[string]bool {
	m := make(map[string]bool, len(categoryColorList))
	for _, c := range categoryColorList {
		m[c] = true
	}
	return m
}()

// categoryStyle validates the icon + color form fields. icon is a Bootstrap
// Icons name (class suffix bi-<icon>) — restrict to the charset icon names
// use so a crafted value can't smuggle extra classes; empty falls back to
// the default chat icon.
func categoryStyle(c *gin.Context) (icon, color string) {
	icon = strings.TrimSpace(c.PostForm("icon"))
	for _, r := range icon {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			icon = ""
			break
		}
	}
	if icon == "" {
		icon = "chat-square-text"
	}
	if color = c.PostForm("color"); !categoryColors[color] {
		color = "blue"
	}
	return icon, color
}

// gateField validates one access gate's (role, tier) pair from the form.
// Role must come from the closed gateRoleList; tier is clamped to the
// contract's 0-3 ladder. Unknown role names fall back to the gate's default
// rather than erroring — the form only offers valid values, so anything
// else is a hand-crafted POST.
func gateField(c *gin.Context, prefix, defRole string) (role string, tier int) {
	role = c.PostForm(prefix + "_role")
	if role != "all" {
		if _, ok := gateRoles[role]; !ok {
			role = defRole
		}
	}
	tier, _ = strconv.Atoi(c.PostForm(prefix + "_tier"))
	if tier < 0 {
		tier = 0
	}
	if tier > 3 {
		tier = 3
	}
	return role, tier
}

// categoryParams assembles + validates the full writable surface from the
// admin form. Gate defaults reproduce the ungated behaviour (see access.go).
func categoryParams(c *gin.Context) CategoryParams {
	p := CategoryParams{
		Name:        strings.TrimSpace(c.PostForm("name")),
		Description: strings.TrimSpace(c.PostForm("description")),
	}
	p.Ordinal, _ = strconv.Atoi(c.PostForm("ordinal"))
	p.Icon, p.Color = categoryStyle(c)
	p.SeeRole, p.SeeTier = gateField(c, "see", "all")
	p.ReadRole, p.ReadTier = gateField(c, "read", "all")
	p.WriteRole, p.WriteTier = gateField(c, "write", "user")
	return p
}

func (h *Handlers) AdminCreateCategory(c *gin.Context) {
	p := categoryParams(c)
	if p.Name == "" {
		redirectCategories(c, "", "name is required")
		return
	}
	if err := h.store.CreateForumCategory(c.Request.Context(), p); err != nil {
		redirectCategories(c, "", err.Error())
		return
	}
	redirectCategories(c, "created "+p.Name, "")
}

func (h *Handlers) AdminUpdateCategory(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	p := categoryParams(c)
	if p.Name == "" {
		redirectCategories(c, "", "name is required")
		return
	}
	if err := h.store.UpdateForumCategory(c.Request.Context(), id, p); err != nil {
		redirectCategories(c, "", err.Error())
		return
	}
	redirectCategories(c, "updated "+p.Name, "")
}

func (h *Handlers) AdminDeleteCategory(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := h.store.DeleteForumCategory(c.Request.Context(), id); err != nil {
		redirectCategories(c, "", err.Error())
		return
	}
	redirectCategories(c, "deleted", "")
}

// AdminMergeCategory consolidates a duplicate category into a chosen
// destination. Pulls src from the path param, dst from a form field.
func (h *Handlers) AdminMergeCategory(c *gin.Context) {
	src, _ := strconv.Atoi(c.Param("id"))
	dst, _ := strconv.Atoi(c.PostForm("dst"))
	if dst <= 0 {
		redirectCategories(c, "", "destination category is required")
		return
	}
	if err := h.store.MergeForumCategory(c.Request.Context(), src, dst); err != nil {
		redirectCategories(c, "", err.Error())
		return
	}
	redirectCategories(c, "merged", "")
}

func redirectCategories(c *gin.Context, msg, errMsg string) {
	u := "/admin/forum-categories"
	if msg != "" {
		u += "?msg=" + url.QueryEscape(msg)
	} else if errMsg != "" {
		u += "?err=" + url.QueryEscape(errMsg)
	}
	c.Redirect(http.StatusSeeOther, u)
}
