package ranks

import (
	"context"
	"embed"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

//go:embed templates/*.html
var viewTemplates embed.FS

// The admin catalog is a plugin-owned View now (loon SlotAdminPage →
// /admin/p/groups) rather than a host template. That is what lets the plugin
// expose what the groups model actually has — kind, parent, visibility — which
// the host's admin_ranks.html could not express, and it drops the last
// web/handlers import standing between this plugin and the lift.

type groupRowVM struct {
	ID            int
	Name          string
	Slug          string
	Kind          string
	Visible       bool
	Depth         int
	Color         string
	TitleColor    string
	CostPoints    int
	DurationDays  int
	SortOrder     int
	DownloadDaily int64
	APIDaily      int64
	Members       int
	// ParentIDValue is 0 for "no parent", so the template can compare without
	// dereferencing a pointer.
	ParentIDValue int
}

type groupsPageVM struct {
	Groups  []groupRowVM
	Kinds   []string
	CanHide bool
	Error   string
	Ok      bool
	// CSRFToken for the three POST forms on this page. They shipped with no
	// token, against a host that gates every POST — so creating, updating and
	// deleting a group each answered 403 for every operator who tried.
	CSRFToken string
}

func (p *Plugin) renderGroups(c *gin.Context) (template.HTML, error) {
	ctx := c.Request.Context()
	groups, err := p.store.Groups(ctx)
	if err != nil {
		p.errs.Report(ctx, "ranks/catalog", err)
		return "", err
	}
	counts, err := p.store.MemberCounts(ctx)
	if err != nil {
		// A missing count is cosmetic; the page is still usable without it.
		p.errs.Report(ctx, "ranks/member-counts", err)
	}

	vm := groupsPageVM{
		CSRFToken: pluginapi.CSRFToken(p.core, c),
		Kinds:     []string{"paid", "earned", "assigned"},
		CanHide:   p.viewerIsAdmin(c),
		Error:     c.Query("error"),
		Ok:        c.Query("ok") == "1",
	}
	for i := range groups {
		g := &groups[i]
		row := groupRowVM{
			ID: g.ID, Name: g.Name, Slug: g.Slug, Kind: g.Kind, Visible: g.Visible,
			Depth: g.Depth, Color: g.Color, TitleColor: g.TitleColor,
			CostPoints: g.CostPoints, DurationDays: g.DurationDays, SortOrder: g.SortOrder,
			DownloadDaily: g.limit(entDownloadDaily, defaultDownloadDaily),
			APIDaily:      g.limit(entAPIDaily, defaultAPIDaily),
			Members:       counts[g.ID],
		}
		if g.ParentID != nil {
			row.ParentIDValue = *g.ParentID
		}
		vm.Groups = append(vm.Groups, row)
	}

	tmpl, err := template.ParseFS(viewTemplates, "templates/groups.html")
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, vm); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

// viewerIsAdmin gates the visibility switch. A hidden group confers real
// entitlements with no badge, which is the shape of a privilege-escalation
// mistake — so mods keep full catalog CRUD but only admins may flip that
// particular lever (operator decision, ENTITLEMENTS.md Stage 2 decisions).
func (p *Plugin) viewerIsAdmin(c *gin.Context) bool {
	u, ok := p.auth.CurrentUser(c)
	return ok && u != nil && u.AtLeast(core.RoleAdmin)
}

// redirect sends the browser back to the page. Actions that redirect return
// ("", nil) per the View contract.
func redirectGroups(c *gin.Context, q string) (template.HTML, error) {
	c.Redirect(http.StatusFound, "/admin/p/groups"+q)
	return "", nil
}

func (p *Plugin) actionCreate(c *gin.Context) (template.HTML, error) {
	g := formGroup(c)
	if g.Name == "" {
		return redirectGroups(c, "?error=name+required")
	}
	// Only an admin may create a group that is invisible from the start.
	if !g.Visible && !p.viewerIsAdmin(c) {
		return redirectGroups(c, "?error=only+an+admin+may+create+a+hidden+group")
	}
	if err := p.store.CreateGroup(c.Request.Context(), g); err != nil {
		p.errs.Report(c.Request.Context(), "ranks/create", err)
		return redirectGroups(c, "?error=create+failed")
	}
	return redirectGroups(c, "?ok=1")
}

func (p *Plugin) actionUpdate(c *gin.Context) (template.HTML, error) {
	ctx := c.Request.Context()
	// Actions are flat (POST /admin/p/groups/<action>), so the row id travels
	// as a form field rather than a path segment.
	id, _ := strconv.Atoi(c.PostForm("id"))
	cur, err := p.store.Group(ctx, id)
	if err != nil {
		if err == ErrGroupNotFound {
			return redirectGroups(c, "?error=unknown+group")
		}
		p.errs.Report(ctx, "ranks/update-read", err)
		return redirectGroups(c, "?error=update+failed")
	}

	g := formGroup(c)
	g.ID = id
	g.Slug = cur.Slug // renaming must not silently re-key Discord role sync
	g.Icon = cur.Icon // no form field for it yet; absent must not mean cleared
	// Only take kind when the form actually posted it. A form that omits the
	// field (an older cached page, a partial POST) must not silently convert an
	// assigned staff group into a purchasable paid tier.
	if _, ok := c.GetPostForm("kind"); !ok {
		g.Kind = cur.Kind
	}
	// Visibility is admin-only: a mod's form posts the current value back, but
	// trust the STORED value rather than the field, so a hand-crafted POST
	// cannot publish a hidden group.
	if p.viewerIsAdmin(c) {
		g.Visible = c.PostForm("visible") == "1"
	} else {
		g.Visible = cur.Visible
	}
	// Overlay the keys this form owns; a group may carry others.
	merged := make(map[string]int64, len(cur.Grants)+len(g.Grants))
	for k, v := range cur.Grants {
		merged[k] = v
	}
	for k, v := range g.Grants {
		merged[k] = v
	}
	// A non-paid group has no purchase semantics, so it should not confer the
	// paid-tier DM ability just because the form always posts limits.
	if g.Kind != "paid" {
		delete(merged, entDMInitiate)
	}
	g.Grants = merged
	g.ParentID = cur.ParentID // re-parenting goes through SetParent below

	if err := p.store.UpdateGroup(ctx, g); err != nil {
		p.errs.Report(ctx, "ranks/update", err)
		return redirectGroups(c, "?error=update+failed")
	}
	// A key the group no longer confers has to be revoked explicitly: core
	// offers no way to enumerate a user's grants, so the only moment the
	// removed set is knowable is here, holding both the old and new group.
	var removed []string
	for k := range cur.Grants {
		if _, still := g.Grants[k]; !still {
			removed = append(removed, k)
		}
	}
	p.resyncEntitlements(ctx, id, removed)

	// Parent last: it has its own validation, and a rejected move should not
	// discard the rest of the edit. Only when the field was actually POSTed —
	// an absent parent_id means "not edited", not "detach from the parent".
	want, provided := parentFromForm(c)
	if provided && !sameParent(cur.ParentID, want) {
		switch err := p.store.SetParent(ctx, id, want); err {
		case nil:
		case ErrParentCycle:
			return redirectGroups(c, "?error=that+parent+would+create+a+loop")
		case ErrParentTooDeep:
			return redirectGroups(c, "?error=nesting+is+limited+to+4+levels")
		default:
			p.errs.Report(ctx, "ranks/set-parent", err)
			return redirectGroups(c, "?error=parent+change+failed")
		}
		// Moving a group changes what it inherits, and therefore what every
		// member of it and of its descendants holds.
		p.resyncEntitlements(ctx, id, nil)
	}
	return redirectGroups(c, "?ok=1")
}

func (p *Plugin) actionDelete(c *gin.Context) (template.HTML, error) {
	id, _ := strconv.Atoi(c.PostForm("id"))
	// Revoke BEFORE the delete: the membership rows cascade away with the
	// group, and without them there is nothing left to say which grants to
	// remove — they would linger until their expiry.
	if p.ents != nil && p.ents.ents != nil {
		if members, err := p.store.MembersOfGroups(c.Request.Context(), []int{id}); err == nil {
			for _, m := range members {
				if err := p.ents.revokeMembership(c.Request.Context(), m.UserID, id, nil); err != nil {
					p.errs.Report(c.Request.Context(), "ranks/revoke-on-delete", err)
				}
			}
		}
	}
	if err := p.store.DeleteGroup(c.Request.Context(), id); err != nil {
		p.errs.Report(c.Request.Context(), "ranks/delete", err)
		return redirectGroups(c, "?error=delete+failed")
	}
	return redirectGroups(c, "?ok=1")
}

// parentFromForm reports the requested parent and whether the field was posted
// at all. The distinction matters: the select's empty option means "no parent",
// but an absent field means the form never offered the control.
// resyncEntitlements fans a catalog change out to the affected members.
// Best-effort and reported: the catalog edit itself has already committed, and
// the boot rebuild repairs a miss.
func (p *Plugin) resyncEntitlements(ctx context.Context, groupID int, removed []string) {
	if p.ents == nil || p.ents.ents == nil {
		return
	}
	if err := p.ents.resyncGroup(ctx, groupID, removed); err != nil {
		p.errs.Report(ctx, "ranks/entitlement-resync", err)
	}
}

func parentFromForm(c *gin.Context) (*int, bool) {
	raw, ok := c.GetPostForm("parent_id")
	if !ok {
		return nil, false
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true // explicit "— none —"
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return nil, true
	}
	return &n, true
}

func sameParent(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
