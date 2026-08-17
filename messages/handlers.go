package messages

import (
	"context"
	"html"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// InboxItem is one row in the unified inbox conversation list — a DM
// thread or a system announcement, normalised so the template can
// render both with one ranges loop. Item is the canonical "subject"
// of the row: a counterparty for DMs, a title for announcements.
type InboxItem struct {
	Kind        string    // "dm" | "announcement"
	ID          int64     // thread_id (dm) or message_id (announcement)
	DisplayName string    // counterparty username (dm) or "System" (announcement)
	AvatarPath  string    // counterparty avatar (dm); empty for system
	Subtitle    string    // last DM preview (dm) or message title (announcement)
	UpdatedAt   time.Time // last activity for sort
	UnreadCount int       // DM unread count; for announcement 0 (read) or 1 (unread)
}

// Inbox renders the unified two-pane inbox at /inbox. Left pane is
// one combined conversation list: DM threads alongside system
// announcements, sorted by most-recent activity. Right pane toggles
// based on the query string:
//
//	?thread=N → render the DM thread N (gated by participation)
//	?msg=N    → render the announcement message N
//	?compose=1 → render the compose form (gated by canSendDM)
//	(none)    → empty state with a Compose CTA
//
// The left pane is always the same; only the right pane swaps. No
// separate /inbox/dm route — that's kept as a redirect for backward
// compat with old notification links.
func (h *Handlers) Inbox(c *gin.Context) {
	user := h.currentUser(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()
	isAdmin := user.AtLeast(core.RoleMod)

	// Parallel: DM threads + system announcements. Independent reads —
	// run them concurrently so the page renders at max(t_dm, t_msg)
	// not t_dm + t_msg.
	var (
		threads []*DMThreadView
		msgs    []*Announcement
		wg      sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		threads, _ = h.store.ListDMThreadsForUser(ctx, user.ID)
	}()
	go func() {
		defer wg.Done()
		msgs, _ = h.store.GetMessagesForUser(ctx, user.ID, isAdmin)
	}()
	wg.Wait()

	// Merge into one list, sorted most-recent-first.
	items := make([]InboxItem, 0, len(threads)+len(msgs))
	for _, t := range threads {
		items = append(items, InboxItem{
			Kind:        "dm",
			ID:          t.ThreadID,
			DisplayName: t.CounterpartyUsername,
			AvatarPath:  t.CounterpartyAvatarPath,
			Subtitle:    t.LastMessageBody,
			UpdatedAt:   t.LastMessageAt,
			UnreadCount: t.UnreadCount,
		})
	}
	for _, m := range msgs {
		unread := 0
		if m.ReadAt == nil {
			unread = 1
		}
		items = append(items, InboxItem{
			Kind:        "announcement",
			ID:          m.ID,
			DisplayName: m.FromName,
			Subtitle:    m.Title,
			UpdatedAt:   m.CreatedAt,
			UnreadCount: unread,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})

	// Right-pane mode resolution. Mutually exclusive — first match
	// wins; the rest are ignored. compose=1 with a thread param is
	// treated as compose (explicit click overrides ambient state).
	composeMode := c.Query("compose") == "1"
	var (
		activeThreadID int64
		activeMessages []*DMMessageView
		activeCpName   string
		activeCpID     int
		activeBlocked  bool
		activeMsg      *Announcement
	)
	canSend := canSendDM(ctx, h.ents, user)

	if composeMode {
		// no thread/msg resolution needed
	} else if tidStr := c.Query("thread"); tidStr != "" {
		if tid, err := strconv.ParseInt(tidStr, 10, 64); err == nil && tid > 0 {
			t, _ := h.store.GetDMThreadForUser(ctx, tid, user.ID)
			if t != nil {
				activeThreadID = t.ID
				if t.UserLoID == user.ID {
					activeCpID = t.UserHiID
				} else {
					activeCpID = t.UserLoID
				}
				var twg sync.WaitGroup
				twg.Add(3)
				go func() {
					defer twg.Done()
					if cp, _ := h.usersByID(ctx, activeCpID); cp != nil {
						activeCpName = cp.Username
					}
				}()
				go func() {
					defer twg.Done()
					activeMessages, _ = h.store.ListDMMessagesForThread(ctx, activeThreadID)
				}()
				go func() {
					defer twg.Done()
					activeBlocked, _ = h.store.IsDMBlocked(ctx, user.ID, activeCpID)
				}()
				go func(tid int64, uid int) {
					bg := context.Background()
					_, _ = h.store.MarkDMThreadRead(bg, tid, uid)
				}(activeThreadID, user.ID)
				twg.Wait()
			}
		}
	} else if midStr := c.Query("msg"); midStr != "" {
		if mid, err := strconv.ParseInt(midStr, 10, 64); err == nil && mid > 0 {
			// GetMessagesForUser already filtered to the viewer's scope;
			// pick the matching one out of the already-loaded slice
			// rather than a second DB hit. Auto-mark as read on view.
			for _, m := range msgs {
				if m.ID == mid {
					activeMsg = m
					if m.ReadAt == nil {
						go func(id int64, uid int) {
							bg := context.Background()
							_ = h.store.MarkMessageRead(bg, id, uid)
						}(m.ID, user.ID)
					}
					break
				}
			}
		}
	}

	h.render(c, "Inbox", "inbox.html", gin.H{
		"PageTitle":        "Inbox",
		"ActiveNav":        "inbox",
		"Items":            items,
		"ActiveThreadID":   activeThreadID,
		"ActiveCpName":     activeCpName,
		"ActiveCpID":       activeCpID,
		"ActiveMessages":   activeMessages,
		"ActiveBlocked":    activeBlocked,
		"ActiveMessage":    activeMsg,
		"ComposeMode":      composeMode,
		"CanSendDM":        canSend,
		"ComposeOk":        c.Query("ok") == "1",
		"ComposeError":     c.Query("err"),
		"PrefillRecipient": c.Query("to"),
	})
}

func (h *Handlers) MarkRead(c *gin.Context) {
	user := h.currentUser(c)
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	_ = h.store.MarkMessageRead(c.Request.Context(), id, user.ID)
	isAdmin := user.AtLeast(core.RoleMod)
	count, _ := h.store.GetUnreadCount(c.Request.Context(), user.ID, isAdmin)
	c.JSON(http.StatusOK, gin.H{"unread": count})
}

func (h *Handlers) DismissMessage(c *gin.Context) {
	user := h.currentUser(c)
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	_ = h.store.DismissMessage(c.Request.Context(), id, user.ID)
	c.Redirect(http.StatusFound, "/inbox")
}

const msgPageSize = 30

func (h *Handlers) AdminMessages(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	offset := pageOffset(page, msgPageSize)

	msgs, total, err := h.store.GetAllMessages(c.Request.Context(), msgPageSize, offset)
	if err != nil {
		c.String(http.StatusInternalServerError, "failed to load messages")
		return
	}

	users, _ := h.allUsers(c.Request.Context())

	totalPages := (total + msgPageSize - 1) / msgPageSize
	if totalPages < 1 {
		totalPages = 1
	}

	h.render(c, "Messages — admin", "admin_messages.html", gin.H{
		"Messages":       msgs,
		"Users":          users,
		"Total":          total,
		"Page":           page,
		"TotalPages":     totalPages,
		"PaginationHTML": deps.RenderPagination(page, msgPageSize, total, "/admin/messages?"),
	})
}

func (h *Handlers) AdminSend(c *gin.Context) {
	sender := h.currentUser(c)
	fromName := sender.Username
	title := c.PostForm("title")
	body := c.PostForm("body")
	target := c.PostForm("target")

	if title == "" || body == "" || target == "" {
		c.Redirect(http.StatusFound, "/admin/messages")
		return
	}

	switch {
	case target == "all", target == "admin":
	default:
		if len(target) < 6 || target[:5] != "user:" {
			c.Redirect(http.StatusFound, "/admin/messages")
			return
		}
	}

	// The error was discarded here until the event went in, which meant an
	// announcement that failed to save still redirected as though it had sent.
	ctx := c.Request.Context()
	if _, err := h.store.SendMessage(ctx, fromName, title, body, target, nil); err != nil {
		h.errs.Report(ctx, "messages/admin-send", err)
		c.Redirect(http.StatusFound, "/admin/messages?err=Could+not+send")
		return
	}
	h.emit(ctx, EventBroadcastSent, sender.ID, BroadcastSent{Title: title, Target: target})
	c.Redirect(http.StatusFound, "/admin/messages")
}

func (h *Handlers) AdminDelete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/messages")
		return
	}
	_ = h.store.DeleteMessage(c.Request.Context(), id)
	c.Redirect(http.StatusFound, "/admin/messages")
}

// ─── Direct messages (migration 247) ─────────────────────────────────

// Eligibility rule: the user holds the dm.initiate entitlement. Plain
// RoleUser can RECEIVE DMs but not start a conversation. Banned/Disabled
// never reach here — the auth gate filters them out.
//
// This used to read user_rank_subscriptions directly, which is what made
// "may I DM?" a question only the ranks domain could answer, and the ranks
// domain therefore impossible to move (ENTITLEMENTS.md). Both halves of the
// old rule still hold, they are just no longer this plugin's business: mod+
// comes from the role baseline wired in cmd/entitlements_wiring.go, and a paid
// tier grants the key through its group. A future source — a follower
// relationship, a trust level — grants the same key and this code does not
// change.
//
// Fails CLOSED by construction: core returns false when it cannot resolve.
func canSendDM(ctx context.Context, ents core.EntitlementsService, user *viewer) bool {
	if user == nil || ents == nil {
		return false
	}
	return ents.Has(ctx, int64(user.ID), entDMInitiate)
}

// InboxDM is kept as a backward-compat alias for old links (deep-link
// notifications written before unification, external bookmarks). It
// just forwards to the unified /inbox, preserving the thread query.
func (h *Handlers) InboxDM(c *gin.Context) {
	target := "/inbox"
	if tid := strings.TrimSpace(c.Query("thread")); tid != "" {
		target += "?thread=" + tid
	} else if c.Query("compose") == "1" {
		target += "?compose=1"
	}
	c.Redirect(http.StatusFound, target)
}

// SendDM handles both "compose new" (resolves recipient_username →
// id, ensures a thread, posts the first message) and "reply to
// existing" (thread_id supplied, recipient inferred from the row).
// Eligibility gate enforced server-side regardless of UI state.
func (h *Handlers) SendDM(c *gin.Context) {
	user := h.currentUser(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()

	if !canSendDM(ctx, h.ents, user) {
		c.Redirect(http.StatusFound, "/inbox?err="+
			"You need a mod role or an active paid rank to send DMs.")
		return
	}

	body := strings.TrimSpace(c.PostForm("body"))
	if body == "" {
		c.Redirect(http.StatusFound, "/inbox?err=Message+body+required")
		return
	}

	var (
		threadID    int64
		recipientID int
	)
	if tidStr := c.PostForm("thread_id"); tidStr != "" {
		tid, _ := strconv.ParseInt(tidStr, 10, 64)
		t, _ := h.store.GetDMThreadForUser(ctx, tid, user.ID)
		if t == nil {
			c.Redirect(http.StatusFound, "/inbox?err=Thread+not+found")
			return
		}
		threadID = t.ID
		if t.UserLoID == user.ID {
			recipientID = t.UserHiID
		} else {
			recipientID = t.UserLoID
		}
	} else {
		recipName := strings.TrimSpace(c.PostForm("recipient"))
		if recipName == "" {
			c.Redirect(http.StatusFound, "/inbox?err=Recipient+required")
			return
		}
		recip, err := h.usersByName(ctx, recipName)
		if err != nil || recip == nil {
			c.Redirect(http.StatusFound, "/inbox?err=User+not+found")
			return
		}
		if recip.ID == user.ID {
			c.Redirect(http.StatusFound, "/inbox?err=Cannot+message+yourself")
			return
		}
		recipientID = recip.ID
		blocked, _ := h.store.IsDMBlocked(ctx, user.ID, recipientID)
		if blocked {
			c.Redirect(http.StatusFound, "/inbox?err=Cannot+send+(blocked)")
			return
		}
		tid, _, err := h.store.EnsureDMThread(ctx, user.ID, recipientID)
		if err != nil {
			h.errs.Report(ctx, "dm/ensure-thread", err)
			c.Redirect(http.StatusFound, "/inbox?err=Internal+error")
			return
		}
		threadID = tid
	}

	if _, err := h.store.CreateDMMessage(ctx, threadID, user.ID, body); err != nil {
		h.errs.Report(ctx, "dm/create-message", err)
		c.Redirect(http.StatusFound, "/inbox?err=Internal+error")
		return
	}
	h.emit(ctx, EventDMSent, user.ID, DMSent{ThreadID: threadID, RecipientID: recipientID})

	// Notification fan-out — recipient gets an inbox NotifDM entry.
	// Best-effort; failure doesn't block the message send.
	if recipientID > 0 {
		go func(senderName string, recipID int, tID int64) {
			bg := context.Background()
			_ = h.notify.Notify(bg, int64(recipID), core.Notification{
				Kind:      notifDM,
				ActorID:   int64(user.ID),
				ActorName: senderName,
				Title:     "New DM from " + senderName,
				Body:      previewBody(body, 140),
				Link:      "/inbox?thread=" + strconv.FormatInt(tID, 10),
			})
		}(user.Username, recipientID, threadID)
	}

	c.Redirect(http.StatusFound, "/inbox?thread="+strconv.FormatInt(threadID, 10)+"&ok=1")
}

// MarkDMRead — POST /inbox/dm/:id/read. Stamps every unread message
// in the thread (where the viewer is the recipient). Used by the
// JS auto-mark-on-open path; the GET handler also calls this on
// view, so the POST is the explicit "I read it" affordance.
func (h *Handlers) MarkDMRead(c *gin.Context) {
	user := h.currentUser(c)
	if user == nil {
		jsonError(c, http.StatusUnauthorized, "login required")
		return
	}
	tid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || tid <= 0 {
		jsonError(c, http.StatusBadRequest, "bad id")
		return
	}
	n, err := h.store.MarkDMThreadRead(c.Request.Context(), tid, user.ID)
	if err != nil {
		jsonInternalError(c, h.errs, "dm/mark-read", err)
		return
	}
	jsonOK(c, gin.H{"marked": n})
}

// DeleteDMThread — POST /inbox/dm/:id/delete. Per-user soft delete:
// hides the thread from this viewer's inbox without affecting the
// other side. A subsequent incoming message restores visibility.
func (h *Handlers) DeleteDMThread(c *gin.Context) {
	user := h.currentUser(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	tid, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if tid > 0 {
		_ = h.store.SoftDeleteDMThreadForUser(c.Request.Context(), tid, user.ID)
	}
	c.Redirect(http.StatusFound, "/inbox")
}

// BlockDMUser — POST /inbox/dm/block. Symmetric refusal model:
// after A blocks B, neither side can DM the other. A unblock from
// either side requires explicit removal.
func (h *Handlers) BlockDMUser(c *gin.Context) {
	user := h.currentUser(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	blockedID, _ := strconv.Atoi(c.PostForm("user_id"))
	if blockedID > 0 && blockedID != user.ID {
		_ = h.store.CreateDMBlock(c.Request.Context(), user.ID, blockedID)
	}
	c.Redirect(http.StatusFound, "/inbox")
}

// UnblockDMUser — POST /inbox/dm/unblock.
func (h *Handlers) UnblockDMUser(c *gin.Context) {
	user := h.currentUser(c)
	if user == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	blockedID, _ := strconv.Atoi(c.PostForm("user_id"))
	if blockedID > 0 {
		_ = h.store.RemoveDMBlock(c.Request.Context(), user.ID, blockedID)
	}
	c.Redirect(http.StatusFound, "/inbox")
}

// previewBody reduces a message body to the text a one-line preview can
// hold: through the same sanitising markdown pipeline the message pane
// renders with, then flattened — tags dropped, entities unescaped,
// whitespace collapsed — so "**hi**" previews as "hi" and a typed "<b>"
// reads as the same literal text the pane shows, not as double-escaped
// markup. Rune-safe cap (n > 0) with an ellipsis only when truncating.
func previewBody(body string, n int) string {
	if deps != nil && deps.Markdown != nil {
		body = stripTags(string(deps.Markdown(body)))
		// The pipeline's output entity-escapes what it refused to treat as
		// markup; a PREVIEW is plain text, so the escapes come back off.
		// Safe because html/template re-escapes on render.
		body = html.UnescapeString(body)
	}
	body = strings.Join(strings.Fields(body), " ")
	if n <= 0 {
		return body
	}
	runes := []rune(body)
	if len(runes) <= n {
		return body
	}
	return string(runes[:n]) + "…"
}

// stripTags drops every tag from already-SANITISED html. It can afford to be
// this simple because the input went through deps.Markdown, which removes
// script/style with their contents — there is no dangerous interior left to
// mis-skip.
func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}
