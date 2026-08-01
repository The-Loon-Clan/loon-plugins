package usenet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The rollback token has to carry the order it REPLACED, captured at apply
// time. A pre-generated undo goes stale: the junk-order rollback script drifted
// in two days and would have restored positions that were no longer in place.
func TestRollbackTokenCarriesTheReplacedOrder(t *testing.T) {
	rows := rankJunkRules([]junkRuleStat{
		stat("long_alnum_run", 10, 7_000_000_000, true),
		stat("software_warez", 40, 23_000_000, true),
		stat("single_token_20", 130, 4_000_000_000, true),
	}, nil)

	token, err := encodeJunkOrderToken(rows)
	if err != nil {
		t.Fatal(err)
	}
	id, order, err := decodeJunkOrderToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if id != optJunkOrder {
		t.Errorf("token id = %q, want %q", id, optJunkOrder)
	}
	// The PRIOR positions, not the recommended ones — that is the whole point.
	for name, want := range map[string]int{"long_alnum_run": 10, "software_warez": 40, "single_token_20": 130} {
		if order[name] != want {
			t.Errorf("%s: token says %d, want its pre-apply %d", name, order[name], want)
		}
	}
	// And it must differ from what Apply would set, or it is not an undo.
	if rec := recommendedOrder(rows); rec["single_token_20"] == order["single_token_20"] {
		t.Error("the token records the NEW order — rolling back would be a no-op")
	}
}

func TestMalformedTokensAreRefused(t *testing.T) {
	for _, bad := range []string{"", "not-base64!!", "eyJ9", "e30"} {
		if _, _, err := decodeJunkOrderToken(bad); err == nil {
			t.Errorf("accepted malformed token %q", bad)
		}
	}
	// A well-formed token with no order is refused rather than silently
	// applying nothing and reporting success.
	if _, _, err := decodeJunkOrderToken(mustToken(t, junkOrderToken{ID: optJunkOrder})); err == nil {
		t.Error("accepted a token carrying no order")
	}
}

// Apply demands the caller echo the optimization's ID. The phrase is the ID
// rather than "yes" so a caller cannot confirm one optimization while meaning
// another — which matters when the caller is a script.
func TestApplyRefusesAWrongConfirmation(t *testing.T) {
	o := optimizer{p: &Plugin{}}
	for _, confirm := range []string{"", "yes", "YES", "usenet.junk_order", "usenet.poster-watch"} {
		if _, err := o.Apply(context.Background(), optJunkOrder, confirm); err == nil {
			t.Errorf("applied with confirmation %q", confirm)
		} else if !strings.Contains(err.Error(), "confirm") {
			t.Errorf("confirmation %q failed for the wrong reason: %v", confirm, err)
		}
	}
}

func TestUnknownIDsAreRejected(t *testing.T) {
	o := optimizer{p: &Plugin{}}
	ctx := context.Background()
	if _, err := o.Inspect(ctx, "usenet.nope"); err == nil {
		t.Error("inspected an unknown optimization")
	}
	if _, err := o.Apply(ctx, "usenet.nope", "usenet.nope"); err == nil {
		t.Error("applied an unknown optimization")
	}
	// A token minted for another optimization must not be replayed here.
	if err := o.Rollback(ctx, mustToken(t, junkOrderToken{
		ID: "other.thing", Order: map[string]int{"x": 1},
	})); err == nil {
		t.Error("rolled back using another optimization's token")
	}
}

// The listing is what an agent reads before deciding anything, so its
// declarations have to be true.
func TestListedOptimizationIsWellFormed(t *testing.T) {
	list, err := optimizer{p: &Plugin{}}.Optimizations(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("got %d optimization(s), err=%v", len(list), err)
	}
	opt := list[0]
	if !strings.HasPrefix(opt.ID, "usenet.") {
		t.Errorf("ID %q must be <plugin>.<slug> — the host routes on that prefix", opt.ID)
	}
	if opt.Group == "" || opt.Title == "" || opt.Summary == "" {
		t.Errorf("listing is missing fields an operator needs: %+v", opt)
	}
	// Advisory rather than plain reversible: positions restore exactly, but
	// order changes which rule is CREDITED in filter_hits, and the caller is
	// entitled to know a judgement is being overwritten.
	if opt.Risk != pluginapi.RiskAdvisory {
		t.Errorf("risk = %q, want advisory", opt.Risk)
	}
	if !opt.Risk.APIReachable() {
		t.Error("advisory must be reachable through the API, or the endpoint is dead")
	}
}

func mustToken(t *testing.T, tok junkOrderToken) string {
	t.Helper()
	rows := make([]junkOrderRow, 0, len(tok.Order))
	for n, p := range tok.Order {
		rows = append(rows, junkOrderRow{junkRuleStat: junkRuleStat{Name: n, Position: p}})
	}
	s, err := encodeJunkOrderToken(rows)
	if err != nil {
		t.Fatal(err)
	}
	if tok.ID == optJunkOrder || tok.ID == "" {
		return s
	}
	// Re-encode with a foreign ID, for the replay test.
	return mustEncode(t, tok)
}

func mustEncode(t *testing.T, tok junkOrderToken) string {
	t.Helper()
	b, err := json.Marshal(tok)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
