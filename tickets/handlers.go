// Package tickets is the support-ticket system — user submission,
// replies, opt-in public visibility, and admin triage. Twelfth
// pkg/core plugin, carved from the handlers.go / admin_handler.go
// god-files. The staff notification fan-out (OnNewTicket /
// OnTicketReply, which also email admins) stays on the host
// NotificationService and arrives via Deps.
package tickets

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// Deps carries the shared app-domain dependencies. Set from
// cmd/main.go before core.Boot. The ticket tables are
// plugin-private — the plugin builds its own Store at Provision.
// Handlers serves the /support* + /admin/tickets* surfaces.
type Handlers struct {
	deps  Deps
	store Store
	errs  core.ErrorReporter
	// core is held only to Emit; nil in tests, so emits go through h.emit.
	core *core.Core
}

func (h *Handlers) SupportPage(c *gin.Context) {
	user := deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	tickets, _ := h.store.GetTicketsByUser(c.Request.Context(), user.ID)
	render(c, http.StatusOK, "Support", "support.html", gin.H{
		"EditorHTML": editor(newTicketEditor),
		"Tickets":    tickets,
		"Submitted":  c.Query("submitted"),
	})
}

func (h *Handlers) SubmitTicket(c *gin.Context) {
	user := deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()

	subject := strings.TrimSpace(c.PostForm("subject"))
	body := strings.TrimSpace(c.PostForm("body"))
	priority := normalizePriority(c.PostForm("priority"))

	if subject == "" || body == "" {
		tickets, _ := h.store.GetTicketsByUser(ctx, user.ID)
		render(c, http.StatusBadRequest, "Support", "support.html", gin.H{
			"EditorHTML": editor(newTicketEditor),
			"Error":      "Subject and description are required.",
			"Tickets":    tickets,
		})
		return
	}
	subject = clampSubject(subject)

	ticket, err := h.store.CreateTicket(ctx, user.ID, user.Username, subject, body, priority)
	if err != nil {
		render(c, http.StatusInternalServerError, "Support", "support.html", gin.H{
			"EditorHTML": editor(newTicketEditor),
			"Error":      "Failed to submit ticket. Please try again.",
		})
		return
	}
	if ticket != nil {
		h.emit(ctx, EventTicketCreated, user.ID,
			TicketCreated{TicketID: ticket.ID, Subject: subject, Priority: priority})
	}
	if h.deps.NotifyNewTicket != nil && ticket != nil {
		h.deps.NotifyNewTicket(ctx, int(ticket.ID), user.Username, subject, body, user.ID)
	}
	c.Redirect(http.StatusFound, "/support?submitted=1")
}

func (h *Handlers) TicketDetail(c *gin.Context) {
	user := deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.String(http.StatusBadRequest, "invalid id")
		return
	}
	ctx := c.Request.Context()
	ticket, err := h.store.GetTicketByID(ctx, id)
	// Visibility — public tickets are readable by anyone signed in;
	// private tickets remain owner-and-admin only. Migration 207.
	visible := err == nil && ticketVisibleTo(ticket, user.ID, user.Admin)
	if !visible {
		c.String(http.StatusNotFound, "ticket not found")
		return
	}
	replies, _ := h.store.GetTicketReplies(ctx, id)

	// Look up role colors for the ticket owner and current user.
	ownerRole := h.roleData(ctx, defaultRoleName)
	if h.deps.OwnerRole != nil {
		if role, err := h.deps.OwnerRole(ctx, ticket.UserID); err == nil && role != "" {
			ownerRole = h.roleData(ctx, role)
		}
	}
	viewerRole := h.roleData(ctx, user.Role)

	render(c, http.StatusOK, "Ticket", "support_ticket.html", gin.H{
		"EditorHTML": editor(replyEditor),
		// The reply page hides "delete" from anyone but the ticket owner; the
		// comparison used to read $.User off the host page data, which stops
		// existing once the markup lives here.
		"ViewerID":   viewerID(c),
		"Ticket":     ticket,
		"Replies":    replies,
		"OwnerRole":  ownerRole,
		"ViewerRole": viewerRole,
	})
}

// SetTicketVisibility — owner-only toggle for the public flag.
// POST /support/:id/visibility?public=1 makes the ticket world-readable;
// public=0 takes it back private. Migration 207.
//
// Replies stay owner-and-admin only even on public tickets — public
// means "others can read" not "others can chime in." A future iteration
// could add a follow / me-too signal, but this keeps the social
// surface narrow while we learn whether opt-in publicity helps at all.
func (h *Handlers) SetTicketVisibility(c *gin.Context) {
	user := deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.String(http.StatusBadRequest, "invalid id")
		return
	}
	public := c.PostForm("public") == "1"
	if err := h.store.SetTicketPublic(c.Request.Context(), id, user.ID, public); err != nil {
		h.errs.HandlerError(c, "ticket/visibility", err)
		return
	}
	c.Redirect(http.StatusFound, fmt.Sprintf("/support/%d", id))
}

// PublicTickets renders /support/public — the opt-in public ticket
// feed. Anyone signed in can read; ticket body + replies appear on
// click-through via the existing TicketDetail handler. Replies are
// still restricted to owner + admin.
func (h *Handlers) PublicTickets(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	const pageSize = 30 // mirrors host
	offset := (page - 1) * pageSize
	tickets, total, err := h.store.ListPublicTickets(c.Request.Context(), pageSize, offset)
	if err != nil {
		h.errs.HandlerError(c, "ticket/public-list", err)
		return
	}
	pagination := deps.RenderPagination(page, pageSize, total, "/support/public?")
	render(c, http.StatusOK, "Public tickets", "support_public.html", gin.H{
		"Tickets":    tickets,
		"Total":      total,
		"Pagination": pagination,
	})
}

func (h *Handlers) ReplyTicket(c *gin.Context) {
	user := deps.Viewer(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.String(http.StatusBadRequest, "invalid id")
		return
	}
	ctx := c.Request.Context()
	ticket, err := h.store.GetTicketByID(ctx, id)
	if err != nil || (ticket.UserID != user.ID && !user.Admin) {
		c.String(http.StatusNotFound, "ticket not found")
		return
	}
	body := strings.TrimSpace(c.PostForm("body"))
	if body == "" {
		c.Redirect(http.StatusFound, "/support/"+c.Param("id"))
		return
	}
	isAdmin := user.Staff
	// A MEMBER replying to a closed ticket reopens it. Staff replying does not:
	// the common staff reply is the one that closes a thread out, and having
	// that immediately reopen it would make closing impossible.
	//
	// This is what makes closing safe to do liberally. Support tickets get
	// closed on the assumption the member can come back if it is not actually
	// resolved — but ReplyTicket wrote the reply and left status alone, so
	// "closed" was a one-way door and a member's follow-up landed in a thread
	// that no staff view lists. Answering into silence is worse than never
	// having replied.
	if !isAdmin {
		if reopened, rerr := h.store.ReopenTicketOnMemberReply(ctx, id); rerr != nil {
			// Best-effort: the reply itself is written below regardless. A
			// failure here costs visibility, not the member's words.
			log.Printf("tickets: reopen on member reply (ticket %d): %v", id, rerr)
		} else if reopened {
			ticket.Status = "open"
		}
	}
	// The error was discarded. Capturing it matters now: emitting after a
	// swallowed failure would announce a reply that does not exist, and a
	// subscriber counting staff replies would credit somebody for it.
	if _, err := h.store.CreateTicketReply(ctx, id, user.ID, user.Username, body, isAdmin); err == nil && isAdmin {
		// STAFF only. The achievement subscriber does not filter -- it adds
		// one for every countable event -- so firing on every reply would
		// badge whoever answered their own ticket most.
		h.emit(ctx, EventStaffReplied, user.ID,
			StaffReplied{TicketID: id, OwnerID: ticket.UserID})
	}
	if h.deps.NotifyReply != nil {
		h.deps.NotifyReply(ctx, int(id), ticket.UserID, ticket.UserID, user.ID, user.Username, ticket.Subject, isAdmin)
	}
	c.Redirect(http.StatusFound, "/support/"+c.Param("id"))
}

func (h *Handlers) Tickets(c *gin.Context) {
	ctx := c.Request.Context()
	statusFilter := c.DefaultQuery("status", "")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	offset := deps.PageOffset(page, ticketsPageSize)

	tickets, total, err := h.store.GetTickets(ctx, statusFilter, ticketsPageSize, offset)
	if err != nil {
		c.String(http.StatusInternalServerError, "failed to get tickets")
		return
	}
	ticketBaseURL := "/admin/tickets?"
	if statusFilter != "" {
		ticketBaseURL = "/admin/tickets?status=" + statusFilter + "&"
	}
	pag := deps.RenderPagination(page, ticketsPageSize, total, ticketBaseURL)
	render(c, http.StatusOK, "Tickets — admin", "admin_tickets.html", gin.H{
		"Tickets": tickets,
		"Total":   total,
		// Computed here rather than read back off the opaque pagination
		// value: the plugin already knows the page and the total, and
		// reaching into the host's type is what would tie this to its shape.
		"Page":         page,
		"TotalPages":   totalPages(total, ticketsPageSize),
		"StatusFilter": statusFilter,
		"Pagination":   pag,
	})
}

func (h *Handlers) UpdateTicket(c *gin.Context) {
	ctx := c.Request.Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/tickets")
		return
	}
	status := c.PostForm("status")
	adminNote := strings.TrimSpace(c.PostForm("admin_note"))
	_ = h.store.UpdateTicketStatus(ctx, id, status, adminNote)
	// Redirect back to ticket detail if that's where the request came from.
	ref := c.GetHeader("Referer")
	if strings.Contains(ref, "/support/") {
		c.Redirect(http.StatusFound, fmt.Sprintf("/support/%d", id))
		return
	}
	c.Redirect(http.StatusFound, "/admin/tickets")
}

func (h *Handlers) DeleteTicket(c *gin.Context) {
	ctx := c.Request.Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/tickets")
		return
	}
	_ = h.store.DeleteTicket(ctx, id)
	c.Redirect(http.StatusFound, "/admin/tickets")
}

// ── System info ───────────────────────────────────────────────────────────────

const ticketsPageSize = 50 // mirrors the host admin_handler const

// normalizePriority clamps a user-supplied priority to the CHECK-constrained
// set {low,normal,high}; anything else (empty, typo, hostile input) falls back
// to normal. Mirrors the support_tickets.priority CHECK from migration 41.
func normalizePriority(p string) string {
	switch p {
	case "low", "normal", "high":
		return p
	default:
		return "normal"
	}
}

// clampSubject truncates an already-trimmed subject to the 200-byte column
// budget. Byte-based to match the original slice; ASCII subjects are untouched.
func clampSubject(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// ticketVisibleTo is the read-access predicate for a single ticket: the owner
// and any admin always see it; everyone else only once the owner has opted it
// public (migration 207). Callers guard the fetch error separately.
func ticketVisibleTo(t *SupportTicket, viewerID int, isAdmin bool) bool {
	return t.Public || t.UserID == viewerID || isAdmin
}

// roleData resolves role styling for the ticket detail chrome, falling back to
// a neutral badge when the host offers none.
//
// The value is opaque here and rendered by the host's own template: role
// display — colour, label, precedence — is host policy, and a plugin that
// built its own badge would diverge from every other place a role appears on
// the site.
func (h *Handlers) roleData(ctx context.Context, roleName string) any {
	if h.deps.RoleBadge == nil {
		return roleFallback{Name: roleName, DisplayName: roleName, Color: "secondary"}
	}
	if badge := h.deps.RoleBadge(ctx, roleName); badge != nil {
		return badge
	}
	return roleFallback{Name: roleName, DisplayName: roleName, Color: "secondary"}
}

// roleFallback carries the same field names the host's role type does, so the
// template renders identically whether or not RoleBadge is wired.
type roleFallback struct {
	Name        string
	DisplayName string
	Color       string
}

// defaultRoleName is the role assumed for a ticket whose author can no longer
// be resolved — a deleted account must not blank the page.
const defaultRoleName = "user"

// totalPages mirrors the host helper's clamp: always at least one page, so an
// empty list still renders as "page 1 of 1" rather than "1 of 0".
func totalPages(totalItems, pageSize int) int {
	if pageSize < 1 {
		pageSize = 1
	}
	n := (totalItems + pageSize - 1) / pageSize
	if n < 1 {
		n = 1
	}
	return n
}

// viewerID is the signed-in member's id, or 0. Small helper because the reply
// page's owner check needs it and a nil Viewer must compare as "not the
// owner" rather than panicking.
func viewerID(c *gin.Context) int {
	if v := deps.Viewer(c); v != nil {
		return v.ID
	}
	return 0
}

// editor and paginate pick whichever contract the host wired. Both return
// empty on the legacy path, where the host's own template supplies these
// itself and the extra keys are simply unused.
func editor(opts map[string]any) template.HTML {
	if deps.RenderEditor == nil {
		return ""
	}
	return deps.RenderEditor(opts)
}

func paginate(page, pageSize, total int, baseURL string) template.HTML {
	if deps.RenderPagination == nil {
		return ""
	}
	return deps.RenderPagination(page, pageSize, total, baseURL)
}
