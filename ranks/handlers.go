// Package ranks owns the groups model behind the paid-rank system — the
// catalog, memberships, the grant capability, and the expiry job
// (ENTITLEMENTS.md Stage 2). Its tables live in the plugin's own Postgres
// schema, and as of Stage 3.4 it is the only copy — the legacy
// public.user_ranks mirror is no longer written.
//
// The admin UI is a plugin-owned View (views.go) rather than a host template,
// so this file is now just the form parsing the plugin's actions share.
//
// There is no Deps/SetDeps any more. The web leg's last shared-domain
// dependency was the host's user-limits cache, which the granter invalidated
// after a purchase; Stage 3.2 moved the limits themselves onto core
// entitlements, so granting through core invalidates it and the plugin no
// longer reaches into host services for app data at all.
package ranks

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify mirrors the migration's slug expression so a group created here and
// one imported from the legacy table get the same shape of key.
func slugify(name string) string {
	s := slugUnsafe.ReplaceAllString(strings.ToLower(name), "-")
	return strings.Trim(s, "-")
}

// limitField reads a numeric limit, returning 0 when the field is absent,
// blank or unusable — i.e. "no opinion".
func limitField(c *gin.Context, name string) int64 {
	raw, ok := c.GetPostForm(name)
	if !ok || strings.TrimSpace(raw) == "" {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 {
		return 0
	}
	return int64(n)
}

// formGroup reads the admin form into a Group.
//
// Parent is deliberately NOT read here: re-parenting goes through SetParent,
// which is the only path carrying the cycle and depth checks. Visible is read,
// but the update action overrides it from the stored row unless the viewer is
// an admin — see actionUpdate.
func formGroup(c *gin.Context) *Group {
	mc, _ := strconv.Atoi(c.PostForm("monthly_cost"))
	dd, _ := strconv.Atoi(c.PostForm("duration_days"))
	so, _ := strconv.Atoi(c.PostForm("sort_order"))
	if dd < 1 {
		dd = 30
	}
	kind := c.PostForm("kind")
	switch kind {
	case "paid", "earned", "assigned":
	default:
		kind = "paid"
	}
	name := c.PostForm("name")
	// A paid tier confers the DM ability — the half of canSendDM the RoleMod
	// baseline does not cover. A zero-cost or non-paid group does not,
	// mirroring the migration's monthly_cost > 0 rule for the seeded grants.
	// A blank limit means the group has NO OPINION about it, which is not the
	// same as wanting the code default. Clamping a blank field to 100/10000 and
	// storing that as a grant would push it out to the legacy mirror — which is
	// how saving any group would have rewritten Free's operator-set
	// api_limit=1000 to 10000. Absent stays absent; the reader supplies the
	// default.
	grants := map[string]int64{}
	if v := limitField(c, "download_limit"); v > 0 {
		grants[entDownloadDaily] = v
	}
	if v := limitField(c, "api_limit"); v > 0 {
		grants[entAPIDaily] = v
	}
	if kind == "paid" && mc > 0 {
		grants[entDMInitiate] = 1
	}
	// A create form with no visibility control (a mod's) means "visible": the
	// hidden case is admin-only and the action re-checks that.
	visible := true
	if v, ok := c.GetPostForm("visible"); ok {
		visible = v == "1"
	}
	return &Group{
		Name:         name,
		Slug:         slugify(name),
		Kind:         kind,
		Visible:      visible,
		Color:        c.PostForm("color"),
		TitleColor:   c.PostForm("title_color"),
		Grants:       grants,
		CostPoints:   mc,
		DurationDays: dd,
		SortOrder:    so,
	}
}
