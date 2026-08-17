package offers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// ── Fakes ───────────────────────────────────────────────────────────────────

// ledgerMove is one recorded points movement.
type ledgerMove struct {
	Kind   string // deduct | award | refund
	UserID int64
	N      int
	Reason string
}

// fakePoints records movements instead of making them, and can be told to
// refuse a debit the way the real service refuses one the member cannot cover.
type fakePoints struct {
	moves    []ledgerMove
	tooPoor  bool
	failNext error
}

func (p *fakePoints) Balance(ctx context.Context, userID int64) (int, error) { return 0, nil }

func (p *fakePoints) Deduct(ctx context.Context, userID int64, n int, reason, detail string, ref int64) (int, error) {
	if p.tooPoor {
		return 0, core.ErrInsufficientPoints
	}
	p.moves = append(p.moves, ledgerMove{"deduct", userID, n, reason})
	return 0, nil
}

func (p *fakePoints) Award(ctx context.Context, userID int64, n int, reason, detail string, ref int64) (int, error) {
	p.moves = append(p.moves, ledgerMove{"award", userID, n, reason})
	return 0, nil
}

func (p *fakePoints) Refund(ctx context.Context, userID int64, n int, reason, detail string, ref int64) (int, error) {
	p.moves = append(p.moves, ledgerMove{"refund", userID, n, reason})
	return 0, nil
}

func (p *fakePoints) History(ctx context.Context, userID int64, limit, offset int) ([]core.LedgerEntry, int, error) {
	return nil, 0, nil
}

// escrowWorld wires the minimum Deps the request path touches, plus the
// recorders each assertion reads.
type escrowWorld struct {
	points   *fakePoints
	created  int  // times CreateOrJoinRequest reached the store
	notified bool // times the offerer fan-out fired
	settled  int
}

func newEscrowWorld(t *testing.T, createErr error, joined bool) *escrowWorld {
	t.Helper()
	w := &escrowWorld{points: &fakePoints{}}
	SetDeps(Deps{
		Viewer: func(c *gin.Context) *Viewer {
			return &Viewer{ID: 7, Username: "tester"}
		},
		JSONOK: func(c *gin.Context, extras gin.H) {
			out := gin.H{"ok": true}
			for k, v := range extras {
				out[k] = v
			}
			c.JSON(http.StatusOK, out)
		},
		JSONError: func(c *gin.Context, code int, msg string) {
			c.JSON(code, gin.H{"ok": false, "error": msg})
		},
		ReportError: func(c *gin.Context, op string, err error) {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "internal"})
		},
		LogError: func(ctx context.Context, op string, err error) {},
		CreateOrJoinRequest: func(ctx context.Context, bucketID, userID, points int, notes string, files []string) (int, bool, error) {
			w.created++
			if createErr != nil {
				return 0, false, createErr
			}
			return 42, joined, nil
		},
		OfferersFor: func(ctx context.Context, bucketID int) ([]int, error) { return []int{9}, nil },
		NotifyRequest: func(ctx context.Context, ids []int, requesterID int, name string, bucketID, requestID int) {
			w.notified = true
		},
		SettleEscrow: func(ctx context.Context, reqID int) (int, []RequestBacker, error) {
			w.settled++
			return 100, []RequestBacker{{UserID: 7, Points: 100}}, nil
		},
	})
	return w
}

func postJSON(t *testing.T, h *Handlers, fn gin.HandlerFunc, body string, params gin.Params) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/offers/request", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	fn(c)
	c.Writer.WriteHeaderNow()
	return rec
}

// ── Tests ───────────────────────────────────────────────────────────────────

// The ordering rule: debit BEFORE the row. Debit first and the worst case is a
// member charged for a request that failed to write — visible and refunded
// below. Write first and the worst case is a pool an offerer can see, act on,
// and never be paid from, discovered only after they spent the bandwidth.
func TestStakeIsDebitedBeforeTheRequestIsWritten(t *testing.T) {
	w := newEscrowWorld(t, nil, false)
	h := &Handlers{core: &core.Core{Points: w.points}}

	rec := postJSON(t, h, h.UserCreateRequest, `{"bucket_id":3,"points":25}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(w.points.moves) != 1 || w.points.moves[0].Kind != "deduct" || w.points.moves[0].N != 25 {
		t.Fatalf("ledger = %+v, want one 25-point deduct", w.points.moves)
	}
	if w.created != 1 {
		t.Errorf("store saw %d writes, want 1", w.created)
	}
}

// A member who cannot cover the stake gets a plain answer and no request. The
// alternative — filing it anyway — is a pool with nothing behind it.
func TestAStakeYouCannotCoverFilesNothing(t *testing.T) {
	w := newEscrowWorld(t, nil, false)
	w.points.tooPoor = true
	h := &Handlers{core: &core.Core{Points: w.points}}

	rec := postJSON(t, h, h.UserCreateRequest, `{"bucket_id":3,"points":9999}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "do not have that many points") {
		t.Errorf("body = %s, want a message the member can act on", rec.Body.String())
	}
	if w.created != 0 {
		t.Errorf("a request was written for a stake that was refused (%d writes)", w.created)
	}
	if len(w.points.moves) != 0 {
		t.Errorf("ledger moved on a refused debit: %+v", w.points.moves)
	}
}

// The refund path exists because a member charged for a request that does not
// exist has no way to notice and no way to ask.
func TestAFailedWriteGivesTheStakeBack(t *testing.T) {
	w := newEscrowWorld(t, errors.New("db down"), false)
	h := &Handlers{core: &core.Core{Points: w.points}}

	postJSON(t, h, h.UserCreateRequest, `{"bucket_id":3,"points":25}`, nil)

	if len(w.points.moves) != 2 {
		t.Fatalf("ledger = %+v, want a deduct and a refund", w.points.moves)
	}
	if w.points.moves[1].Kind != "refund" || w.points.moves[1].N != 25 {
		t.Errorf("second move = %+v, want a 25-point refund", w.points.moves[1])
	}
}

// Zero points is the default and must stay a one-click path: no debit, no
// refund, no ledger noise for the members who do not care about staking.
func TestAFreeRequestTouchesNoLedger(t *testing.T) {
	w := newEscrowWorld(t, nil, false)
	h := &Handlers{core: &core.Core{Points: w.points}}

	rec := postJSON(t, h, h.UserCreateRequest, `{"bucket_id":3,"points":0}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(w.points.moves) != 0 {
		t.Errorf("ledger moved on a free request: %+v", w.points.moves)
	}
}

// Joining must not re-notify. A request with twenty backers would otherwise
// ping every offerer twenty times about one file.
func TestJoiningDoesNotReNotifyTheOfferers(t *testing.T) {
	w := newEscrowWorld(t, nil, true)
	h := &Handlers{core: &core.Core{Points: w.points}}

	rec := postJSON(t, h, h.UserCreateRequest, `{"bucket_id":3,"points":0}`, nil)
	if !strings.Contains(rec.Body.String(), `"joined":true`) {
		t.Errorf("body = %s, want joined:true so the UI can say so", rec.Body.String())
	}
	if w.notified {
		t.Error("joining an existing request re-notified every offerer")
	}
}

// Delivery pays the pool, and it pays it to the offerer.
func TestDeliveryPaysThePoolToTheOfferer(t *testing.T) {
	w := newEscrowWorld(t, nil, false)
	h := &Handlers{core: &core.Core{Points: w.points}}

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/api/offers/requests/42/deliver", nil)
	h.payEscrow(c, 42, 9)

	if w.settled != 1 {
		t.Fatalf("settled %d times, want 1", w.settled)
	}
	if len(w.points.moves) != 1 || w.points.moves[0].Kind != "award" {
		t.Fatalf("ledger = %+v, want one award", w.points.moves)
	}
	if w.points.moves[0].UserID != 9 || w.points.moves[0].N != 100 {
		t.Errorf("award = %+v, want 100 points to user 9", w.points.moves[0])
	}
}

// Withdrawing is the other half of escrow: the stake left the balance, so
// there has to be a way back that is not "wait forever".
func TestWithdrawRefundsTheStake(t *testing.T) {
	w := newEscrowWorld(t, nil, false)
	d := *deps
	d.WithdrawBacking = func(ctx context.Context, reqID, userID int) (int, bool, error) {
		return 30, true, nil
	}
	SetDeps(d)
	h := &Handlers{core: &core.Core{Points: w.points}}

	rec := postJSON(t, h, h.UserWithdrawRequest, `{}`, gin.Params{{Key: "id", Value: "42"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(w.points.moves) != 1 || w.points.moves[0].Kind != "refund" || w.points.moves[0].N != 30 {
		t.Fatalf("ledger = %+v, want one 30-point refund", w.points.moves)
	}
	if !strings.Contains(rec.Body.String(), `"cancelled":true`) {
		t.Errorf("body = %s, want the cancellation reported", rec.Body.String())
	}
}

// Nothing staked means nothing to give back — and no ledger entry inventing one.
func TestWithdrawingNothingMovesNothing(t *testing.T) {
	w := newEscrowWorld(t, nil, false)
	d := *deps
	d.WithdrawBacking = func(ctx context.Context, reqID, userID int) (int, bool, error) {
		return 0, false, nil
	}
	SetDeps(d)
	h := &Handlers{core: &core.Core{Points: w.points}}

	postJSON(t, h, h.UserWithdrawRequest, `{}`, gin.Params{{Key: "id", Value: "42"}})
	if len(w.points.moves) != 0 {
		t.Errorf("ledger = %+v, want nothing", w.points.moves)
	}
}
