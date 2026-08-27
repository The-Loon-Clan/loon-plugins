package messages

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// Replying to an existing thread is NOT gated on dm.initiate, and consts.go
// has always said so: that entitlement answers "may this user START a
// conversation?". The code did not match — canSendDM ran before the handler
// looked at thread_id, and the template gated the reply box the same way — so
// an ordinary member could read a DM, block the sender and delete the thread,
// but not answer it. Being messaged and being unable to reply is a worse state
// than not being messaged at all.
//
// Nothing opened by moving the gate. The reply branch is scoped by
// GetDMThreadForUser(tid, user.ID) and re-checks IsDMBlocked; both were
// already there. These pin the distinction so it cannot quietly close again.

func init() { gin.SetMode(gin.TestMode) }

// sendCtx drives SendDM as a real form POST.
func sendCtx(form url.Values) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest("POST", "/inbox/dm/send", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Request = req
	return c, w
}

// replyHandlers wires the minimum SendDM's reply path touches: a store, an
// auth service that returns `me`, and the real entitlements composition.
func replyHandlers(t *testing.T, me int, roles map[int64]core.Role) *Handlers {
	t.Helper()
	svc, _ := gate(t, roles)
	return &Handlers{
		store: NewMemStore(),
		ents:  svc,
		// Both are REQUIRED by core.New (it refuses to build without them),
		// so a real host never has them nil — but the send path fans a
		// notification out on a GOROUTINE, and a nil there panics the whole
		// process rather than failing one request. No-op adapters, so the
		// test exercises the real path instead of a shorter one.
		notify: core.NewNotifications(core.NotificationsAdapter{}),
		errs:   core.NewErrorReporter(core.ErrorAdapter{}),
		auth: core.NewAuth(core.AuthAdapter{
			CurrentUserFn: func(*gin.Context) (*core.User, bool) {
				return &core.User{ID: int64(me), Username: "member", Role: roles[int64(me)]}, true
			},
		}),
	}
}

// The bug, at the handler. A plain member with no dm.initiate grant answers a
// thread they are already in.
func TestPlainMemberMayReplyToAnExistingThread(t *testing.T) {
	ctx := context.Background()
	h := replyHandlers(t, 20, map[int64]core.Role{20: core.RoleUser, 21: core.RoleMod})

	// Precondition: this member genuinely cannot START one.
	if canSendDM(ctx, h.ents, user(20, core.RoleUser)) {
		t.Fatal("fixture is wrong — this member should not hold dm.initiate")
	}

	tid, _, err := h.store.EnsureDMThread(ctx, 21, 20) // a mod opened it
	if err != nil {
		t.Fatalf("EnsureDMThread: %v", err)
	}

	c, _ := sendCtx(url.Values{
		"thread_id": {strconv.FormatInt(tid, 10)},
		"body":      {"answering you"},
	})
	h.SendDM(c)

	if loc := c.Writer.Header().Get("Location"); strings.Contains(loc, "err=") {
		t.Fatalf("reply refused: %s", loc)
	}
	msgs, err := h.store.ListDMMessagesForThread(ctx, tid)
	if err != nil {
		t.Fatalf("ListDMMessagesForThread: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Body != "answering you" {
		t.Fatalf("reply not stored: %+v", msgs)
	}
}

// The half that must NOT change: starting a conversation is still gated.
func TestPlainMemberStillCannotStartAConversation(t *testing.T) {
	h := replyHandlers(t, 20, map[int64]core.Role{20: core.RoleUser})

	c, _ := sendCtx(url.Values{
		"recipient": {"someone"},
		"body":      {"hello stranger"},
	})
	h.SendDM(c)

	loc := c.Writer.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Fatalf("an ungranted member started a conversation: %s", loc)
	}
	// It must name what was refused. "You cannot send DMs" was the old
	// wording and it was wrong twice over once replies were ungated: this
	// member CAN send DMs, just not open new threads.
	if !strings.Contains(loc, "start a conversation") {
		t.Errorf("refusal does not say what was refused: %s", loc)
	}
}

// Ungating the reply must not reach a thread the member is not in. The scope
// is GetDMThreadForUser, which was already there — this pins that it is what
// is actually doing the work now that the entitlement is not.
func TestReplyCannotReachSomeoneElsesThread(t *testing.T) {
	ctx := context.Background()
	h := replyHandlers(t, 20, map[int64]core.Role{20: core.RoleUser})

	other, _, err := h.store.EnsureDMThread(ctx, 41, 42) // strangers
	if err != nil {
		t.Fatalf("EnsureDMThread: %v", err)
	}

	c, _ := sendCtx(url.Values{
		"thread_id": {strconv.FormatInt(other, 10)},
		"body":      {"intruding"},
	})
	h.SendDM(c)

	if loc := c.Writer.Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Fatalf("a non-member posted into someone else's thread: %s", loc)
	}
	if msgs, _ := h.store.ListDMMessagesForThread(ctx, other); len(msgs) != 0 {
		t.Fatalf("message landed in a thread the sender is not in: %+v", msgs)
	}
}

// A block still stops the reply, and that check also predates this change.
func TestBlockStillStopsAReply(t *testing.T) {
	ctx := context.Background()
	h := replyHandlers(t, 20, map[int64]core.Role{20: core.RoleUser, 21: core.RoleMod})

	tid, _, err := h.store.EnsureDMThread(ctx, 21, 20)
	if err != nil {
		t.Fatalf("EnsureDMThread: %v", err)
	}
	if err := h.store.CreateDMBlock(ctx, 20, 21); err != nil {
		t.Fatalf("CreateDMBlock: %v", err)
	}

	c, _ := sendCtx(url.Values{
		"thread_id": {strconv.FormatInt(tid, 10)},
		"body":      {"should not land"},
	})
	h.SendDM(c)

	if loc := c.Writer.Header().Get("Location"); !strings.Contains(loc, "blocked") {
		t.Fatalf("a blocked reply was accepted: %s", loc)
	}
	if msgs, _ := h.store.ListDMMessagesForThread(ctx, tid); len(msgs) != 0 {
		t.Fatalf("blocked message stored: %+v", msgs)
	}
}
