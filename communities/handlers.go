package communities

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Notification/ledger kinds — MUST match the models.LedgerEntryType
// catalog so the admin Points Log and the reputation queries see
// community joins like any other economy event.
const (
	ledgerSpendJoin  = "spend_community_join"
	ledgerRefundJoin = "refund_community_join"
)

// Handlers serves the /c/* surface. Ported from
// web/handlers.CommunitiesHandler during the plugin extraction;
// host seams are the exported handlers helpers (BaseData,
// pagination, RenderForumMarkdown) and the Core services (Auth
// for the session user, Points for the join-cost escrow, Errors
// for 500s).
type Handlers struct {
	store  Store
	auth   core.AuthService
	points core.PointsService
	errs   core.ErrorReporter
}

func NewHandlers(store Store, auth core.AuthService, points core.PointsService, errs core.ErrorReporter) *Handlers {
	return &Handlers{store: store, auth: auth, points: points, errs: errs}
}

// communityPageSize bounds the per-page thread + community-list
// pagination. Conservative because each card carries a banner; we
// can crank it up once the surface is exercised.
const communityPageSize = 25

// communitySlugRE is the closed allowlist for /c/<slug> values:
// 3–32 chars of [a-z0-9_-], starting with a letter. Matches the
// Reddit pattern; rejects punctuation, spaces, leading numbers, and
// reserved /c subpaths via the Gin radix tree.
var communitySlugRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,31}$`)

// communitySlugReserved blocks slugs that collide with static
// /c subpaths (handler routes) so a community named "new" can't be
// created at /c/new and shadow the create form.
var communitySlugReserved = map[string]bool{
	"new":      true,
	"create":   true,
	"admin":    true,
	"mod":      true,
	"settings": true,
	"about":    true,
	"static":   true,
	"random":   true,
	"popular":  true,
	"all":      true,
}

// viewer returns the request's user (nil for anonymous) and the
// site-admin flag (mod-or-above). The session middleware in the
// route chain has already loaded the user into the context.
func (h *Handlers) viewer(c *gin.Context) (*core.User, bool) {
	u, ok := h.auth.CurrentUser(c)
	if !ok {
		return nil, false
	}
	return u, u.AtLeast(core.RoleMod)
}

func (h *Handlers) viewerID(c *gin.Context) int {
	u, _ := h.viewer(c)
	if u == nil {
		return 0
	}
	return int(u.ID)
}

// ── List + create ─────────────────────────────────────────────────

// Index renders the public /c landing page — a card grid of
// non-hidden communities ordered by subscriber count.
func (h *Handlers) Index(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	offset := deps.PageOffset(page, communityPageSize)
	rows, total, err := h.store.ListCommunities(c.Request.Context(), communityPageSize, offset)
	if err != nil {
		h.errs.HandlerError(c, "community/index", err)
		return
	}
	h.render(c, http.StatusOK, "Communities", "communities_index.html",
		&communitiesIndexVM{
			Communities: rows,
			Total:       total,
			Pagination:  pager(page, total, "/c?"),
		},
		gin.H{
			"Communities": rows,
			"Total":       total,
			"Pagination":  legacyPager(page, total, "/c?"),
		})
}

// NewCommunityForm — GET /c/new. Authenticated users only; the
// session check happens in the route group so an anonymous visit
// redirects to /login.
func (h *Handlers) NewCommunityForm(c *gin.Context) {
	h.render(c, http.StatusOK, "Create community", "community_new.html",
		&communityNewVM{}, gin.H{})
}

// newCommunityFormError re-renders the create form with the entered values
// and one validation message. 400, not 200: the form is being rejected and
// the status line should say so.
func (h *Handlers) newCommunityFormError(c *gin.Context, msg, slug, name, description string) {
	h.render(c, http.StatusBadRequest, "Create community", "community_new.html",
		&communityNewVM{Error: msg, Slug: slug, Name: name, Description: description},
		gin.H{"Error": msg, "Slug": slug, "Name": name, "Description": description})
}

// CreateCommunity — POST /c. Validates slug + name, inserts the
// row with the session user as the owner.
func (h *Handlers) CreateCommunity(c *gin.Context) {
	userID := h.viewerID(c)
	if userID == 0 {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	slug := strings.ToLower(strings.TrimSpace(c.PostForm("slug")))
	name := strings.TrimSpace(c.PostForm("name"))
	description := strings.TrimSpace(c.PostForm("description"))
	if !communitySlugRE.MatchString(slug) {
		h.newCommunityFormError(c, "Slug must be 3–32 chars, start with a letter, and use only lowercase letters / digits / underscore / dash.", slug, name, description)
		return
	}
	if communitySlugReserved[slug] {
		h.newCommunityFormError(c, "That slug is reserved. Please pick another.", slug, name, description)
		return
	}
	if name == "" || len(name) > 80 {
		h.newCommunityFormError(c, "Name is required (1–80 chars).", slug, name, description)
		return
	}
	if len(description) > 500 {
		description = description[:500]
	}

	comm := &Community{
		Slug:        slug,
		Name:        name,
		Description: description,
		OwnerUserID: userID,
	}
	if err := h.store.CreateCommunity(c.Request.Context(), comm); err != nil {
		// Slug uniqueness violation surfaces as a pq error; map to
		// the same form re-render so the user can correct the slug.
		if strings.Contains(err.Error(), "communities_slug_key") {
			h.newCommunityFormError(c, "That slug is already taken. Please pick another.", slug, name, description)
			return
		}
		h.errs.HandlerError(c, "community/create", err)
		return
	}
	// Auto-subscribe the creator so the join counter starts at 1 and
	// their own community appears in their subscribed feed.
	_ = h.store.SubscribeCommunity(c.Request.Context(), comm.ID, userID)
	c.Redirect(http.StatusFound, "/c/"+comm.Slug)
}

// ── View ──────────────────────────────────────────────────────────

// View — GET /c/:slug. Loads the community + its thread page + the
// sidebar essentials (rules, mods, viewer role) in parallel-ish
// fetches (cheap; one DB hop each).
func (h *Handlers) View(c *gin.Context) {
	slug := strings.ToLower(c.Param("slug"))
	if !communitySlugRE.MatchString(slug) {
		c.Redirect(http.StatusFound, "/c")
		return
	}
	ctx := c.Request.Context()
	_, isAdmin := h.viewer(c)
	userID := h.viewerID(c)

	comm, err := h.store.GetCommunityBySlug(ctx, slug, userID)
	if err != nil {
		h.fail(c, http.StatusNotFound, "nocommunity")
		return
	}
	// Hidden communities 404 for everyone except the owner + admins.
	// OwnedBy, because userID is 0 for an anonymous viewer.
	if comm.HiddenAt != nil && !pluginapi.VisibleTo(comm.OwnerUserID, userID, isAdmin) {
		h.fail(c, http.StatusNotFound, "nocommunity")
		return
	}

	role, _ := h.store.GetCommunityViewerRole(ctx, comm.ID, userID)
	if isAdmin {
		role.IsMod = true
	}
	// Surface an outstanding join request so the template can show
	// "Requested" / the last decision instead of a Join button.
	var myRequest *CommunityJoinRequest
	if userID != 0 && !role.IsSubscriber {
		myRequest, _ = h.store.GetUserJoinRequest(ctx, comm.ID, userID)
		if myRequest != nil && myRequest.Status == JoinRequestPending {
			role.HasPendingRequest = true
		}
	}
	// Pending-request count drives the mod queue badge.
	pendingCount := 0
	if role.CanModerate() {
		if pending, err := h.store.ListPendingJoinRequests(ctx, comm.ID); err == nil {
			pendingCount = len(pending)
		}
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	offset := deps.PageOffset(page, communityPageSize)
	threads, total, err := h.store.ListCommunityThreads(ctx, comm.ID, communityPageSize, offset, false)
	if err != nil {
		h.errs.HandlerError(c, "community/view", err)
		return
	}
	rules, _ := h.store.ListCommunityRules(ctx, comm.ID)
	mods, _ := h.store.ListCommunityMods(ctx, comm.ID)

	// Hoisted rather than repeated per branch: popFlash consumes the
	// session message, so a second call would read nothing.
	flash := h.popFlash(c)
	sidebarHTML := deps.Markdown(comm.SidebarMD)
	descriptionHTML := deps.Markdown(comm.Description)

	h.render(c, http.StatusOK, fmt.Sprintf("/c/%s - %s", comm.Slug, comm.Name), "community_view.html",
		&communityViewVM{
			Community:       comm,
			Threads:         threads,
			Total:           total,
			Pagination:      pager(page, total, fmt.Sprintf("/c/%s?", slug)),
			Rules:           rules,
			Mods:            mods,
			Role:            role,
			MyRequest:       myRequest,
			PendingCount:    pendingCount,
			Flash:           flash,
			SidebarHTML:     sidebarHTML,
			DescriptionHTML: descriptionHTML,
		},
		gin.H{
			"Community":       comm,
			"Threads":         threads,
			"Total":           total,
			"Pagination":      legacyPager(page, total, fmt.Sprintf("/c/%s?", slug)),
			"Rules":           rules,
			"Mods":            mods,
			"Role":            role,
			"MyRequest":       myRequest,
			"PendingCount":    pendingCount,
			"Flash":           flash,
			"SidebarHTML":     sidebarHTML,
			"DescriptionHTML": descriptionHTML,
		})
}

// popFlash reads + clears the one-shot community flash message set
// by redirectWithFlash. Returns "" when none is set.
func (h *Handlers) popFlash(c *gin.Context) Flash {
	session := sessions.Default(c)
	v := session.Get("community_flash")
	if v == nil {
		return Flash{}
	}
	session.Delete("community_flash")
	_ = session.Save()
	raw, _ := v.(string)
	if raw == "" {
		return Flash{}
	}
	parts := strings.Split(raw, flashSep)
	return Flash{Code: parts[0], Args: parts[1:]}
}

// ToggleSubscribe — POST /c/:slug/subscribe. Owners can't
// unsubscribe (they're permanent); the form just no-ops in that
// case.
func (h *Handlers) ToggleSubscribe(c *gin.Context) {
	user, _ := h.viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	userID := int(user.ID)
	ctx := c.Request.Context()
	slug := strings.ToLower(c.Param("slug"))
	comm, err := h.store.GetCommunityBySlug(ctx, slug, userID)
	if err != nil {
		h.fail(c, http.StatusNotFound, "nocommunity")
		return
	}

	// Leaving is always allowed (no refund of any join cost — that's
	// spent). Bail early so the gate logic below only handles joins.
	if comm.IsSubscribed {
		_ = h.store.UnsubscribeCommunity(ctx, comm.ID, userID)
		c.Redirect(http.StatusFound, "/c/"+slug)
		return
	}

	balance, _ := h.points.Balance(ctx, user.ID)

	switch comm.JoinType {
	case CommunityJoinTypeInviteOnly:
		// No public join path; an invite link is the only way in.
		h.redirectWithFlash(c, slug, "inviteonly")
		return

	case CommunityJoinTypeRequest:
		// Gate first; queue second. Points are escrowed at request
		// time (spent now, refunded on deny / withdraw).
		if f := joinRequirementError(user, balance, comm); f.Code != "" {
			h.redirectWithFlash(c, slug, f.Code, f.Args...)
			return
		}
		held := 0
		if comm.JoinPointsCost > 0 {
			if _, err := h.points.Deduct(ctx, user.ID, comm.JoinPointsCost, ledgerSpendJoin, "Applied to /c/"+slug, 0); err != nil {
				h.redirectWithFlash(c, slug, "applycost", strconv.Itoa(comm.JoinPointsCost))
				return
			}
			held = comm.JoinPointsCost
		}
		pitch := strings.TrimSpace(c.PostForm("message"))
		if err := h.store.CreateJoinRequest(ctx, comm.ID, userID, pitch, held); err != nil {
			// Pending-request unique violation → already applied.
			// Refund the escrow we just took since the row didn't
			// land.
			if held > 0 {
				if _, err := h.points.Refund(ctx, user.ID, held, ledgerRefundJoin, "Duplicate application to /c/"+slug, 0); err != nil {
					h.errs.Report(ctx, "communities/refund-dup", err)
				}
			}
			h.redirectWithFlash(c, slug, "alreadypending")
			return
		}
		h.redirectWithFlash(c, slug, "requested")
		return

	default: // open
		if f := joinRequirementError(user, balance, comm); f.Code != "" {
			h.redirectWithFlash(c, slug, f.Code, f.Args...)
			return
		}
		if comm.JoinPointsCost > 0 {
			if _, err := h.points.Deduct(ctx, user.ID, comm.JoinPointsCost, ledgerSpendJoin, "Joined /c/"+slug, 0); err != nil {
				h.redirectWithFlash(c, slug, "joincost", strconv.Itoa(comm.JoinPointsCost))
				return
			}
		}
		_ = h.store.SubscribeCommunity(ctx, comm.ID, userID)
		c.Redirect(http.StatusFound, "/c/"+slug)
	}
}

// joinRequirementError reports which join gate the user fails (account age,
// role, points balance), as a CODE and the numbers its sentence quotes. A zero
// Flash means every gate passes.
//
// Points affordability is double-checked here so the refusal can say WHICH
// gate stopped them; the actual debit's balance guard is the real enforcement.
func joinRequirementError(user *core.User, balance int, comm *Community) Flash {
	if comm.MinAccountAgeDays > 0 {
		ageDays := int(time.Since(user.CreatedAt).Hours() / 24)
		if ageDays < comm.MinAccountAgeDays {
			return Flash{"tooyoung", []string{
				strconv.Itoa(comm.MinAccountAgeDays), strconv.Itoa(ageDays)}}
		}
	}
	if comm.MinRoleLevel > 0 && int(user.Role) < comm.MinRoleLevel {
		return Flash{Code: "lowrank"}
	}
	if comm.JoinPointsCost > 0 && balance < comm.JoinPointsCost {
		return Flash{"toopoor", []string{
			strconv.Itoa(comm.JoinPointsCost), strconv.Itoa(balance)}}
	}
	return Flash{}
}

// redirectWithFlash stashes a one-shot message in the session and
// redirects back to the community page. The View handler reads +
// clears it. Avoids threading error state through query params.
// Flash is one message waiting for the next page render: a CODE, and the
// values its sentence quotes.
//
// A CODE AND NOT A SENTENCE (CHECKLIST §10). The words live in the templates,
// which is what makes them translatable -- and where a sentence quotes a
// number, the number is an ARGUMENT rather than something formatted into the
// text, because word order differs by language and a sentence assembled in Go
// has to be rewritten rather than translated.
//
// This is the only message channel in the tree that is not a query parameter,
// which is why it took longest to convert: the Go-sentence audit looks at
// redirect literals and could not see any of these.
type Flash struct {
	Code string
	Args []string
}

// Arg returns the i'th value, or "" — a template that asks for one the sender
// did not provide renders a gap rather than failing the whole page.
func (f Flash) Arg(i int) string {
	if i < 0 || i >= len(f.Args) {
		return ""
	}
	return f.Args[i]
}

// flashSep joins the code to its arguments inside the session's single string.
// The unit separator cannot occur in an identifier, a decimal number or an
// invite code, so the split needs no escaping and cannot be spoofed by a value.
const flashSep = "\x1f"

func (h *Handlers) redirectWithFlash(c *gin.Context, slug, code string, args ...string) {
	session := sessions.Default(c)
	session.Set("community_flash", strings.Join(append([]string{code}, args...), flashSep))
	_ = session.Save()
	c.Redirect(http.StatusFound, "/c/"+slug)
}

// ── Threads ───────────────────────────────────────────────────────

// NewThreadForm — GET /c/:slug/submit. Anonymous users get bounced
// to /login; the form's POST handler re-checks since the session
// can expire between GET and submit.
func (h *Handlers) NewThreadForm(c *gin.Context) {
	userID := h.viewerID(c)
	if userID == 0 {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	slug := strings.ToLower(c.Param("slug"))
	comm, err := h.store.GetCommunityBySlug(c.Request.Context(), slug, userID)
	if err != nil {
		h.fail(c, http.StatusNotFound, "nocommunity")
		return
	}
	h.render(c, http.StatusOK, fmt.Sprintf("New thread in /c/%s", comm.Slug), "community_new_thread_c.html",
		&communityNewThreadVM{Community: comm, Editor: editorHTML()},
		gin.H{"Community": comm})
}

// CreateThread — POST /c/:slug/submit.
func (h *Handlers) CreateThread(c *gin.Context) {
	userID := h.viewerID(c)
	if userID == 0 {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	slug := strings.ToLower(c.Param("slug"))
	comm, err := h.store.GetCommunityBySlug(c.Request.Context(), slug, userID)
	if err != nil {
		h.fail(c, http.StatusNotFound, "nocommunity")
		return
	}
	title := strings.TrimSpace(c.PostForm("title"))
	body := strings.TrimSpace(c.PostForm("body"))
	if title == "" || len(title) > 300 {
		c.Redirect(http.StatusFound, "/c/"+slug+"/submit")
		return
	}
	thread, err := h.store.CreateCommunityThread(c.Request.Context(), comm.ID, userID, title, body)
	if err != nil {
		h.errs.HandlerError(c, "community/create-thread", err)
		return
	}
	c.Redirect(http.StatusFound, fmt.Sprintf("/c/%s/thread/%d", slug, thread.ID))
}

// ViewThread — GET /c/:slug/thread/:id. Renders OP + paged replies.
func (h *Handlers) ViewThread(c *gin.Context) {
	slug := strings.ToLower(c.Param("slug"))
	threadID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.Redirect(http.StatusFound, "/c/"+slug)
		return
	}
	ctx := c.Request.Context()
	_, isAdmin := h.viewer(c)
	userID := h.viewerID(c)

	thread, err := h.store.GetCommunityThread(ctx, threadID)
	if err != nil {
		h.fail(c, http.StatusNotFound, "nothread")
		return
	}
	// Cross-check the slug param against the thread's actual
	// community so /c/<other>/thread/<id> doesn't render under the
	// wrong sidebar.
	if !strings.EqualFold(thread.CommunitySlug, slug) {
		c.Redirect(http.StatusFound, fmt.Sprintf("/c/%s/thread/%d", thread.CommunitySlug, thread.ID))
		return
	}
	comm, err := h.store.GetCommunityBySlug(ctx, thread.CommunitySlug, userID)
	if err != nil {
		h.fail(c, http.StatusNotFound, "nocommunity")
		return
	}
	role, _ := h.store.GetCommunityViewerRole(ctx, comm.ID, userID)
	if isAdmin {
		role.IsMod = true
	}
	// Mod-removed / admin-hidden threads stay visible for mods and
	// the OP (so they can see the removal reason) but 404 for
	// everyone else.
	if (thread.RemovedAt != nil || thread.HiddenAt != nil) &&
		!pluginapi.VisibleTo(thread.UserID, userID, role.CanModerate()) {
		h.fail(c, http.StatusNotFound, "nothread")
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	offset := deps.PageOffset(page, communityPageSize)
	posts, total, err := h.store.ListCommunityPosts(ctx, threadID, communityPageSize, offset)
	if err != nil {
		h.errs.HandlerError(c, "community/view-thread", err)
		return
	}
	rules, _ := h.store.ListCommunityRules(ctx, comm.ID)
	mods, _ := h.store.ListCommunityMods(ctx, comm.ID)

	// Pre-render markdown bodies once in Go so the template doesn't
	// re-invoke the renderer per row. Mirrors the forum plugin's
	// pattern.
	views := make([]threadPostVM, 0, len(posts))
	for _, p := range posts {
		views = append(views, threadPostVM{CommunityPost: p, BodyHTML: deps.Markdown(p.Body)})
	}
	bodyHTML := deps.Markdown(thread.Body)
	sidebarHTML := deps.Markdown(comm.SidebarMD)

	h.render(c, http.StatusOK, fmt.Sprintf("%s - /c/%s", thread.Title, comm.Slug), "community_thread_c.html",
		&communityThreadVM{
			Community:   comm,
			Thread:      thread,
			BodyHTML:    bodyHTML,
			Posts:       views,
			Total:       total,
			Pagination:  pager(page, total, fmt.Sprintf("/c/%s/thread/%d?", slug, threadID)),
			Rules:       rules,
			Mods:        mods,
			Role:        role,
			SidebarHTML: sidebarHTML,
		},
		gin.H{
			"Community":   comm,
			"Thread":      thread,
			"BodyHTML":    bodyHTML,
			"Posts":       views,
			"Total":       total,
			"Pagination":  legacyPager(page, total, fmt.Sprintf("/c/%s/thread/%d?", slug, threadID)),
			"Rules":       rules,
			"Mods":        mods,
			"Role":        role,
			"SidebarHTML": sidebarHTML,
		})
}

// Reply — POST /c/:slug/thread/:id/reply.
func (h *Handlers) Reply(c *gin.Context) {
	userID := h.viewerID(c)
	if userID == 0 {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	slug := strings.ToLower(c.Param("slug"))
	threadID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.Redirect(http.StatusFound, "/c/"+slug)
		return
	}
	thread, err := h.store.GetCommunityThread(c.Request.Context(), threadID)
	if err != nil {
		h.fail(c, http.StatusNotFound, "nothread")
		return
	}
	if thread.Locked {
		c.Redirect(http.StatusFound, fmt.Sprintf("/c/%s/thread/%d", slug, threadID))
		return
	}
	body := strings.TrimSpace(c.PostForm("body"))
	if body == "" {
		c.Redirect(http.StatusFound, fmt.Sprintf("/c/%s/thread/%d", slug, threadID))
		return
	}
	if _, err := h.store.CreateCommunityPost(c.Request.Context(), threadID, userID, body, nil); err != nil {
		h.errs.HandlerError(c, "community/reply", err)
		return
	}
	c.Redirect(http.StatusFound, fmt.Sprintf("/c/%s/thread/%d", slug, threadID))
}

// ── Mod actions ───────────────────────────────────────────────────

// modCheck is the gate every mod action runs first — resolves the
// community by slug, checks the viewer can moderate, and returns
// the community object for the action handler.
func (h *Handlers) modCheck(c *gin.Context, slug string) (*Community, error) {
	ctx := c.Request.Context()
	user, isAdmin := h.viewer(c)
	if user == nil {
		return nil, errors.New("not authenticated")
	}
	comm, err := h.store.GetCommunityBySlug(ctx, slug, int(user.ID))
	if err != nil {
		return nil, err
	}
	role, _ := h.store.GetCommunityViewerRole(ctx, comm.ID, int(user.ID))
	if isAdmin {
		role.IsMod = true
	}
	if !role.CanModerate() {
		return nil, errors.New("not a moderator")
	}
	return comm, nil
}

// PinThread — POST /c/:slug/thread/:id/pin (mod).
func (h *Handlers) PinThread(c *gin.Context) {
	slug := strings.ToLower(c.Param("slug"))
	comm, err := h.modCheck(c, slug)
	if err != nil {
		h.fail(c, http.StatusForbidden, "notmod")
		return
	}
	threadID, _ := strconv.Atoi(c.Param("id"))
	pinned := c.PostForm("pin") != "0"
	if err := h.store.SetCommunityThreadPinned(c.Request.Context(), threadID, comm.ID, pinned); err != nil {
		h.errs.HandlerError(c, "community/pin", err)
		return
	}
	c.Redirect(http.StatusFound, fmt.Sprintf("/c/%s/thread/%d", slug, threadID))
}

// LockThread — POST /c/:slug/thread/:id/lock (mod).
func (h *Handlers) LockThread(c *gin.Context) {
	slug := strings.ToLower(c.Param("slug"))
	comm, err := h.modCheck(c, slug)
	if err != nil {
		h.fail(c, http.StatusForbidden, "notmod")
		return
	}
	threadID, _ := strconv.Atoi(c.Param("id"))
	locked := c.PostForm("lock") != "0"
	if err := h.store.SetCommunityThreadLocked(c.Request.Context(), threadID, comm.ID, locked); err != nil {
		h.errs.HandlerError(c, "community/lock", err)
		return
	}
	c.Redirect(http.StatusFound, fmt.Sprintf("/c/%s/thread/%d", slug, threadID))
}

// RemoveThread — POST /c/:slug/thread/:id/remove (mod). Soft-delete
// via the removed_at column; the row stays in the DB so an admin
// can restore it.
func (h *Handlers) RemoveThread(c *gin.Context) {
	slug := strings.ToLower(c.Param("slug"))
	comm, err := h.modCheck(c, slug)
	if err != nil {
		h.fail(c, http.StatusForbidden, "notmod")
		return
	}
	threadID, _ := strconv.Atoi(c.Param("id"))
	reason := strings.TrimSpace(c.PostForm("reason"))
	if err := h.store.RemoveCommunityThread(c.Request.Context(), threadID, comm.ID, h.viewerID(c), reason); err != nil {
		h.errs.HandlerError(c, "community/remove-thread", err)
		return
	}
	c.Redirect(http.StatusFound, "/c/"+slug)
}

// RemovePost — POST /c/:slug/thread/:id/post/:pid/remove (mod).
func (h *Handlers) RemovePost(c *gin.Context) {
	slug := strings.ToLower(c.Param("slug"))
	comm, err := h.modCheck(c, slug)
	if err != nil {
		h.fail(c, http.StatusForbidden, "notmod")
		return
	}
	threadID, _ := strconv.Atoi(c.Param("id"))
	postID, err := strconv.ParseInt(c.Param("pid"), 10, 64)
	if err != nil || postID <= 0 {
		c.Redirect(http.StatusFound, fmt.Sprintf("/c/%s/thread/%d", slug, threadID))
		return
	}
	reason := strings.TrimSpace(c.PostForm("reason"))
	if err := h.store.RemoveCommunityPost(c.Request.Context(), postID, comm.ID, h.viewerID(c), reason); err != nil {
		h.errs.HandlerError(c, "community/remove-post", err)
		return
	}
	c.Redirect(http.StatusFound, fmt.Sprintf("/c/%s/thread/%d", slug, threadID))
}

// ── Join-request queue (mod) ──────────────────────────────────────

// RequestQueue — GET /c/:slug/requests. Owner/mods see pending
// join requests with each applicant's pitch + requirement-relevant
// stats (account age, points, role).
func (h *Handlers) RequestQueue(c *gin.Context) {
	slug := strings.ToLower(c.Param("slug"))
	comm, err := h.modCheck(c, slug)
	if err != nil {
		h.fail(c, http.StatusForbidden, "notmod")
		return
	}
	pending, _ := h.store.ListPendingJoinRequests(c.Request.Context(), comm.ID)
	invites, _ := h.store.ListCommunityInvites(c.Request.Context(), comm.ID)
	flash := h.popFlash(c)
	h.render(c, http.StatusOK, fmt.Sprintf("Join requests - /c/%s", comm.Slug), "community_join_requests.html",
		&communityJoinRequestsVM{Community: comm, Requests: pending, Invites: invites, Flash: flash},
		gin.H{"Community": comm, "Requests": pending, "Invites": invites, "Flash": flash})
}

// ApproveRequest — POST /c/:slug/requests/:rid/approve (mod).
func (h *Handlers) ApproveRequest(c *gin.Context) {
	slug := strings.ToLower(c.Param("slug"))
	comm, err := h.modCheck(c, slug)
	if err != nil {
		h.fail(c, http.StatusForbidden, "notmod")
		return
	}
	rid, _ := strconv.Atoi(c.Param("rid"))
	jr, err := h.store.GetJoinRequest(c.Request.Context(), rid)
	if err != nil || jr == nil || jr.CommunityID != comm.ID {
		c.Redirect(http.StatusFound, "/c/"+slug+"/requests")
		return
	}
	resp := strings.TrimSpace(c.PostForm("response"))
	if err := h.store.ApproveJoinRequest(c.Request.Context(), rid, h.viewerID(c), resp); err != nil {
		h.errs.HandlerError(c, "community/approve-request", err)
		return
	}
	c.Redirect(http.StatusFound, "/c/"+slug+"/requests")
}

// DenyRequest — POST /c/:slug/requests/:rid/deny (mod). Refunds any
// escrowed points to the applicant.
func (h *Handlers) DenyRequest(c *gin.Context) {
	slug := strings.ToLower(c.Param("slug"))
	comm, err := h.modCheck(c, slug)
	if err != nil {
		h.fail(c, http.StatusForbidden, "notmod")
		return
	}
	rid, _ := strconv.Atoi(c.Param("rid"))
	jr, err := h.store.GetJoinRequest(c.Request.Context(), rid)
	if err != nil || jr == nil || jr.CommunityID != comm.ID {
		c.Redirect(http.StatusFound, "/c/"+slug+"/requests")
		return
	}
	resp := strings.TrimSpace(c.PostForm("response"))
	userID, pointsHeld, err := h.store.DenyJoinRequest(c.Request.Context(), rid, h.viewerID(c), resp)
	if err != nil {
		h.errs.HandlerError(c, "community/deny-request", err)
		return
	}
	// Refund the escrowed join cost — the applicant didn't get in.
	if userID != 0 && pointsHeld > 0 {
		if _, err := h.points.Refund(c.Request.Context(), int64(userID), pointsHeld, ledgerRefundJoin, "Join request denied for /c/"+slug, 0); err != nil {
			h.errs.Report(c.Request.Context(), "communities/refund-deny", err)
		}
	}
	c.Redirect(http.StatusFound, "/c/"+slug+"/requests")
}

// ── Invites (mod create / public redeem) ──────────────────────────

// CreateInvite — POST /c/:slug/invites (mod). Generates a random
// code; max_uses + expiry are optional form fields.
func (h *Handlers) CreateInvite(c *gin.Context) {
	slug := strings.ToLower(c.Param("slug"))
	comm, err := h.modCheck(c, slug)
	if err != nil {
		h.fail(c, http.StatusForbidden, "notmod")
		return
	}
	maxUses, _ := strconv.Atoi(c.PostForm("max_uses"))
	if maxUses < 0 {
		maxUses = 0
	}
	note := strings.TrimSpace(c.PostForm("note"))
	var expiresAt *time.Time
	if days, _ := strconv.Atoi(c.PostForm("expires_days")); days > 0 {
		t := time.Now().AddDate(0, 0, days)
		expiresAt = &t
	}
	code, err := randomInviteCode()
	if err != nil {
		h.errs.HandlerError(c, "community/invite-code", err)
		return
	}
	if err := h.store.CreateCommunityInvite(c.Request.Context(), comm.ID, h.viewerID(c), code, note, maxUses, expiresAt); err != nil {
		h.errs.HandlerError(c, "community/create-invite", err)
		return
	}
	h.redirectWithFlash(c, slug, "invited", code)
	c.Redirect(http.StatusFound, "/c/"+slug+"/requests")
}

// RedeemInvite — GET /c/join/:code. Public landing that subscribes
// the (logged-in) user, bypassing the requirement gates.
func (h *Handlers) RedeemInvite(c *gin.Context) {
	userID := h.viewerID(c)
	if userID == 0 {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	code := strings.TrimSpace(c.Param("code"))
	communityID, err := h.store.RedeemCommunityInvite(c.Request.Context(), code, userID)
	if err != nil {
		h.fail(c, http.StatusForbidden, "badinvite")
		return
	}
	comm, err := h.store.GetCommunityByID(c.Request.Context(), communityID)
	if err != nil {
		c.Redirect(http.StatusFound, "/c")
		return
	}
	c.Redirect(http.StatusFound, "/c/"+comm.Slug)
}

// ── Settings (owner) ──────────────────────────────────────────────

// Settings — GET /c/:slug/settings. Owner-only (the form lets them
// switch join mode + set the requirement gates). Mods can moderate
// but only the owner reconfigures access.
func (h *Handlers) Settings(c *gin.Context) {
	slug := strings.ToLower(c.Param("slug"))
	_, isAdmin := h.viewer(c)
	userID := h.viewerID(c)
	comm, err := h.store.GetCommunityBySlug(c.Request.Context(), slug, userID)
	if err != nil {
		h.fail(c, http.StatusNotFound, "nocommunity")
		return
	}
	if !pluginapi.VisibleTo(comm.OwnerUserID, userID, isAdmin) {
		c.String(http.StatusForbidden, "owner only")
		return
	}
	flash := h.popFlash(c)
	h.render(c, http.StatusOK, fmt.Sprintf("Settings - /c/%s", comm.Slug), "community_settings.html",
		&communitySettingsVM{Community: comm, Flash: flash},
		gin.H{"Community": comm, "Flash": flash})
}

// SaveSettings — POST /c/:slug/settings. Persists join config +
// description/sidebar customisation.
func (h *Handlers) SaveSettings(c *gin.Context) {
	slug := strings.ToLower(c.Param("slug"))
	_, isAdmin := h.viewer(c)
	userID := h.viewerID(c)
	comm, err := h.store.GetCommunityBySlug(c.Request.Context(), slug, userID)
	if err != nil {
		h.fail(c, http.StatusNotFound, "nocommunity")
		return
	}
	if !pluginapi.VisibleTo(comm.OwnerUserID, userID, isAdmin) {
		c.String(http.StatusForbidden, "owner only")
		return
	}

	// Join settings — closed allowlist on join_type; clamp the
	// numeric gates to sane non-negative ranges.
	joinType := c.PostForm("join_type")
	if joinType != CommunityJoinTypeRequest && joinType != CommunityJoinTypeInviteOnly {
		joinType = CommunityJoinTypeOpen
	}
	minAge := clampInt(atoiDefault(c.PostForm("min_account_age_days"), 0), 0, 3650)
	minRole := clampInt(atoiDefault(c.PostForm("min_role_level"), 0), 0, int(core.RoleMod))
	pointsCost := clampInt(atoiDefault(c.PostForm("join_points_cost"), 0), 0, 1000000)
	if err := h.store.UpdateCommunityJoinSettings(c.Request.Context(), comm.ID, joinType, minAge, minRole, pointsCost); err != nil {
		h.errs.HandlerError(c, "community/save-join-settings", err)
		return
	}

	// Customisation — description + sidebar markdown. Banner/icon
	// upload fields accept URLs; uploaded files win when present.
	comm.Name = strings.TrimSpace(c.PostForm("name"))
	if comm.Name == "" {
		comm.Name = comm.Slug
	}
	if len(comm.Name) > 80 {
		comm.Name = comm.Name[:80]
	}
	comm.Description = strings.TrimSpace(c.PostForm("description"))
	if len(comm.Description) > 500 {
		comm.Description = comm.Description[:500]
	}
	comm.SidebarMD = strings.TrimSpace(c.PostForm("sidebar_md"))
	comm.BannerURL = strings.TrimSpace(c.PostForm("banner_url"))
	comm.IconURL = strings.TrimSpace(c.PostForm("icon_url"))
	// A failed upload (bad type / too large) flashes the error and
	// aborts the save so the user can correct it rather than
	// silently dropping the image.
	if url, err := saveCommunityImage(c, bannerKind, comm.Slug); err != nil {
		h.redirectWithFlash(c, slug, "bannerfailed")
		c.Redirect(http.StatusFound, "/c/"+slug+"/settings")
		return
	} else if url != "" {
		comm.BannerURL = url
	}
	if url, err := saveCommunityImage(c, iconKind, comm.Slug); err != nil {
		h.redirectWithFlash(c, slug, "iconfailed")
		c.Redirect(http.StatusFound, "/c/"+slug+"/settings")
		return
	} else if url != "" {
		comm.IconURL = url
	}
	comm.AccentColor = strings.TrimSpace(c.PostForm("accent_color"))
	// Banner vertical focal point (0–100). Clamp defensively.
	comm.BannerPosition = clampInt(atoiDefault(c.PostForm("banner_position"), 50), 0, 100)
	comm.NSFW = c.PostForm("nsfw") == "1"
	if _, err := h.store.UpdateCommunityCustomization(c.Request.Context(), comm.ID, comm); err != nil {
		h.errs.HandlerError(c, "community/save-customization", err)
		return
	}
	h.redirectWithFlash(c, slug, "saved")
	c.Redirect(http.StatusFound, "/c/"+slug+"/settings")
}

// ── helpers ───────────────────────────────────────────────────────

// baseURL builds the scheme+host prefix for absolute invite links.
func baseURL(c *gin.Context) string {
	scheme := "https"
	if c.Request.TLS == nil && !strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "http"
	}
	return scheme + "://" + c.Request.Host
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
