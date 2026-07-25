package donations

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"testing"
)

const eps = 1e-9

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// ─── DonationPointsConfig.PointsForDollars ─────────────────────────

func TestPointsForDollars(t *testing.T) {
	tests := []struct {
		name    string
		cfg     DonationPointsConfig
		dollars float64
		want    float64
	}{
		{"zero dollars", DonationPointsConfig{PointsPerDollar: 1, MultiplierPer10: 1.2}, 0, 0},
		{"negative dollars", DonationPointsConfig{PointsPerDollar: 1, MultiplierPer10: 1.2}, -5, 0},
		// Pure-linear when multiplier is 1.0: 25 * 2 = 50.
		{"linear mult 1.0", DonationPointsConfig{PointsPerDollar: 2, MultiplierPer10: 1.0}, 25, 50},
		// mult<=0 is coerced to 1.0 (linear), so 10 * 3 = 30.
		{"mult zero coerced linear", DonationPointsConfig{PointsPerDollar: 3, MultiplierPer10: 0}, 10, 30},
		// First brick only, sub-$10: 7 * 1 * 1.2^0 = 7.
		{"partial first brick", DonationPointsConfig{PointsPerDollar: 1, MultiplierPer10: 1.2}, 7, 7},
		// Exactly one full brick: 10 * 1 * 1.2^0 = 10.
		{"one full brick", DonationPointsConfig{PointsPerDollar: 1, MultiplierPer10: 1.2}, 10, 10},
		// Two bricks: 10*1 + 10*1.2 = 22.
		{"two bricks", DonationPointsConfig{PointsPerDollar: 1, MultiplierPer10: 1.2}, 20, 22},
		// The documented $50 example: 10+12+14.4+17.28+20.736 = 74.416.
		{"doc example 50", DonationPointsConfig{PointsPerDollar: 1, MultiplierPer10: 1.2}, 50, 74.416},
		// Fractional carry into second brick: 10*1 + 5*1.2 = 16.
		{"brick and a half", DonationPointsConfig{PointsPerDollar: 1, MultiplierPer10: 1.2}, 15, 16},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.PointsForDollars(tt.dollars)
			if !approx(got, tt.want) {
				t.Errorf("PointsForDollars(%v) = %v, want %v", tt.dollars, got, tt.want)
			}
		})
	}
}

func TestPointsForDollars_Monotonic(t *testing.T) {
	cfg := DonationPointsConfig{PointsPerDollar: 1, MultiplierPer10: 1.2}
	prev := cfg.PointsForDollars(1)
	for d := 2.0; d <= 500; d++ {
		cur := cfg.PointsForDollars(d)
		if cur < prev-eps {
			t.Fatalf("points not monotonic at $%v: %v < %v", d, cur, prev)
		}
		prev = cur
	}
}

// ─── DonationPackageView.Recompute ─────────────────────────────────

func TestPackageViewRecompute(t *testing.T) {
	tests := []struct {
		name          string
		stockTotal    int
		stockUsed     int
		wantRemaining int
		wantFunded    bool
		wantPercent   int
	}{
		{"none used", 10, 0, 10, false, 0},
		{"half used", 10, 5, 5, false, 50},
		{"exactly funded", 4, 4, 0, true, 100},
		{"over-used clamps", 4, 9, 0, true, 100},
		{"single slot used", 1, 1, 0, true, 100},
		{"three of eight rounds down", 8, 3, 5, false, 37}, // 3*100/8 = 37 (int)
		{"zero total no divide", 0, 0, 0, true, 0},         // StockTotal 0 => percent left at 0, remaining 0 => funded
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &DonationPackageView{DonationPackage: DonationPackage{StockTotal: tt.stockTotal}}
			v.Recompute(tt.stockUsed)
			if v.StockUsed != tt.stockUsed {
				t.Errorf("StockUsed = %d, want %d", v.StockUsed, tt.stockUsed)
			}
			if v.StockRemaining != tt.wantRemaining {
				t.Errorf("StockRemaining = %d, want %d", v.StockRemaining, tt.wantRemaining)
			}
			if v.Funded != tt.wantFunded {
				t.Errorf("Funded = %v, want %v", v.Funded, tt.wantFunded)
			}
			if v.PercentRound != tt.wantPercent {
				t.Errorf("PercentRound = %d, want %d", v.PercentRound, tt.wantPercent)
			}
		})
	}
}

// ─── DonationGoalGroup lock / percent / items ──────────────────────

func TestGoalGroupLocking(t *testing.T) {
	tests := []struct {
		name                             string
		mGoal, mRaised, yGoal, yRaised   float64
		wantMLock, wantYLock, wantFunded bool
	}{
		{"nothing raised", 100, 0, 1000, 0, false, false, false},
		{"monthly met only", 100, 100, 1000, 500, true, false, false},
		{"both met", 100, 120, 1000, 1000, true, true, true},
		{"zero goal never locks", 0, 50, 0, 50, false, false, false},
		{"yearly met monthly not", 100, 10, 1000, 2000, false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &DonationGoalGroup{
				MonthlyGoalUSD: tt.mGoal, MonthlyRaisedUSD: tt.mRaised,
				YearlyGoalUSD: tt.yGoal, YearlyRaisedUSD: tt.yRaised,
			}
			if g.MonthlyLocked() != tt.wantMLock {
				t.Errorf("MonthlyLocked = %v, want %v", g.MonthlyLocked(), tt.wantMLock)
			}
			if g.YearlyLocked() != tt.wantYLock {
				t.Errorf("YearlyLocked = %v, want %v", g.YearlyLocked(), tt.wantYLock)
			}
			if g.FullyFunded() != tt.wantFunded {
				t.Errorf("FullyFunded = %v, want %v", g.FullyFunded(), tt.wantFunded)
			}
		})
	}
}

func TestGoalGroupPercent(t *testing.T) {
	g := &DonationGoalGroup{
		MonthlyGoalUSD: 200, MonthlyRaisedUSD: 50, // 25%
		YearlyGoalUSD: 100, YearlyRaisedUSD: 500, // capped at 100
	}
	if got := g.MonthlyPercent(); !approx(got, 25) {
		t.Errorf("MonthlyPercent = %v, want 25", got)
	}
	if got := g.YearlyPercent(); !approx(got, 100) {
		t.Errorf("YearlyPercent = %v, want 100 (capped)", got)
	}
	// Zero goal => 0%, no divide-by-zero.
	z := &DonationGoalGroup{}
	if got := z.MonthlyPercent(); got != 0 {
		t.Errorf("MonthlyPercent with zero goal = %v, want 0", got)
	}
}

func TestGoalGroupItemsSplit(t *testing.T) {
	g := &DonationGoalGroup{Items: []*SiteCost{
		{Label: "server", Period: "monthly"},
		{Label: "domain", Period: "yearly"},
		{Label: "usenet", Period: "monthly"},
		{Label: "audit", Period: "yearly"},
		{Label: "weird", Period: ""}, // neither bucket
	}}
	m := g.MonthlyItems()
	if len(m) != 2 || m[0].Label != "server" || m[1].Label != "usenet" {
		t.Errorf("MonthlyItems = %+v, want [server usenet]", labels(m))
	}
	y := g.YearlyItems()
	if len(y) != 2 || y[0].Label != "domain" || y[1].Label != "audit" {
		t.Errorf("YearlyItems = %+v, want [domain audit]", labels(y))
	}
}

func labels(cs []*SiteCost) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Label
	}
	return out
}

// ─── verifyBTCPaySig ───────────────────────────────────────────────

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyBTCPaySig(t *testing.T) {
	secret := "topsecret-shared-key"
	body := []byte(`{"type":"InvoiceSettled","invoiceId":"abc"}`)
	good := sign(secret, body)

	tests := []struct {
		name   string
		secret string
		body   []byte
		header string
		want   bool
	}{
		{"valid", secret, body, good, true},
		{"missing prefix", secret, body, hex.EncodeToString([]byte("x")), false},
		{"empty header", secret, body, "", false},
		{"non-hex digest", secret, body, "sha256=zzzz", false},
		{"wrong secret", "other-key", body, good, false},
		{"tampered body", secret, append([]byte(" "), body...), good, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := verifyBTCPaySig(tt.secret, tt.body, tt.header); got != tt.want {
				t.Errorf("verifyBTCPaySig = %v, want %v", got, tt.want)
			}
		})
	}
}
