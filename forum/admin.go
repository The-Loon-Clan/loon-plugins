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
		"Flash":      c.Query("msg"),
		"Err":        c.Query("err"),
	}))
}

func (h *Handlers) AdminCreateCategory(c *gin.Context) {
	name := strings.TrimSpace(c.PostForm("name"))
	desc := strings.TrimSpace(c.PostForm("description"))
	ordinal, _ := strconv.Atoi(c.PostForm("ordinal"))
	if name == "" {
		redirectCategories(c, "", "name is required")
		return
	}
	if err := h.store.CreateForumCategory(c.Request.Context(), name, desc, ordinal); err != nil {
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
	if err := h.store.UpdateForumCategory(c.Request.Context(), id, name, desc, ordinal); err != nil {
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
