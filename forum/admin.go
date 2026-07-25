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
	c.HTML(http.StatusOK, "admin_forum_categories.html", deps.BaseData(c, gin.H{
		"Categories": cats,
		"Colors":     categoryColorList,
		"Flash":      c.Query("msg"),
		"Err":        c.Query("err"),
	}))
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

func (h *Handlers) AdminCreateCategory(c *gin.Context) {
	name := strings.TrimSpace(c.PostForm("name"))
	desc := strings.TrimSpace(c.PostForm("description"))
	ordinal, _ := strconv.Atoi(c.PostForm("ordinal"))
	if name == "" {
		redirectCategories(c, "", "name is required")
		return
	}
	icon, color := categoryStyle(c)
	if err := h.store.CreateForumCategory(c.Request.Context(), name, desc, ordinal, icon, color); err != nil {
		redirectCategories(c, "", err.Error())
		return
	}
	redirectCategories(c, "created "+name, "")
}

func (h *Handlers) AdminUpdateCategory(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	name := strings.TrimSpace(c.PostForm("name"))
	desc := strings.TrimSpace(c.PostForm("description"))
	ordinal, _ := strconv.Atoi(c.PostForm("ordinal"))
	if name == "" {
		redirectCategories(c, "", "name is required")
		return
	}
	icon, color := categoryStyle(c)
	if err := h.store.UpdateForumCategory(c.Request.Context(), id, name, desc, ordinal, icon, color); err != nil {
		redirectCategories(c, "", err.Error())
		return
	}
	redirectCategories(c, "updated "+name, "")
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
