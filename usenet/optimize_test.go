package usenet

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The rollback token has to carry the state it REPLACED, captured at apply
// time. A pre-generated undo goes stale: the junk-order rollback script drifted
// in two days and would have restored positions that were no longer in place.
func TestRollbackTokenCarriesTheReplacedState(t *testing.T) {
	rows := rankJunkRules([]junkRuleStat{
		stat("long_alnum_run", 10, 7_000_000_000, true),
		stat("software_warez", 40, 23_000_000, true),
		stat("single_token_20", 130, 4_000_000_000, true),
	}, nil)

	prior := map[string]int{}
	for _, r := range rows {
		prior[r.Name] = r.Position
	}
	token, err := encodeToken(optJunkOrder, prior)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := decodeToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if tok.ID != optJunkOrder {
		t.Errorf("token id = %q, want %q", tok.ID, optJunkOrder)
	}
	// The PRIOR positions, not the recommended ones — that is the whole point.
	if rec := recommendedOrder(rows); rec["single_token_20"] == prior["single_token_20"] {
		t.Error("the token records the NEW order — rolling back would be a no-op")
	}
	if prior["single_token_20"] != 130 {
		t.Errorf("token says %d, want its pre-apply 130", prior["single_token_20"])
	}
}

func TestMalformedTokensAreRefused(t *testing.T) {
	for _, bad := range []string{"", "not-base64!!", "eyJ9", "e30"} {
		if _, err := decodeToken(bad); err == nil {
			t.Errorf("accepted malformed token %q", bad)
		}
	}
	// An envelope with no payload is refused rather than silently restoring
	// nothing and reporting success.
	empty, _ := encodeToken("", nil)
	if _, err := decodeToken(empty); err == nil {
		t.Error("accepted a token with no id")
	}
}

// Apply demands the caller echo the optimization's ID. The phrase is the ID
// rather than "yes" so a caller cannot confirm one optimization while meaning
// another — which matters when the caller is a script.
func TestApplyRefusesAWrongConfirmation(t *testing.T) {
	o := optimizer{p: &Plugin{}}
	for _, id := range []string{optJunkOrder, optPosterWatch} {
		for _, confirm := range []string{"", "yes", "YES", optJunkOrder + "x", "usenet.other"} {
			if confirm == id {
				continue
			}
			if _, err := o.Apply(context.Background(), id, confirm); err == nil {
				t.Errorf("%s: applied with confirmation %q", id, confirm)
			} else if !strings.Contains(err.Error(), "confirm") {
				t.Errorf("%s: confirmation %q failed for the wrong reason: %v", id, confirm, err)
			}
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
	foreign, _ := encodeToken("other.thing", map[string]int{"x": 1})
	if err := o.Rollback(ctx, foreign); err == nil {
		t.Error("rolled back using another optimization's token")
	}
}

// The listing is what an agent reads before deciding anything, so its
// declarations have to be true.
func TestListedOptimizationsAreWellFormed(t *testing.T) {
	list, err := optimizer{p: &Plugin{}}.Optimizations(context.Background())
	if err != nil || len(list) != 2 {
		t.Fatalf("got %d optimization(s), err=%v", len(list), err)
	}
	seen := map[string]bool{}
	for _, opt := range list {
		if !strings.HasPrefix(opt.ID, "usenet.") {
			t.Errorf("ID %q must be <plugin>.<slug> — the host routes on that prefix", opt.ID)
		}
		if seen[opt.ID] {
			t.Errorf("duplicate ID %q", opt.ID)
		}
		seen[opt.ID] = true
		if opt.Group == "" || opt.Title == "" || opt.Summary == "" {
			t.Errorf("%s: listing is missing fields an operator needs: %+v", opt.ID, opt)
		}
		if !opt.Risk.APIReachable() {
			t.Errorf("%s: risk %q is not API-reachable, so the endpoint is dead", opt.ID, opt.Risk)
		}
	}
	// The junk order is ADVISORY rather than plain reversible: positions
	// restore exactly, but order changes which rule is CREDITED in filter_hits,
	// and the caller is entitled to know a judgement is being overwritten.
	for _, opt := range list {
		if opt.ID == optJunkOrder && opt.Risk != pluginapi.RiskAdvisory {
			t.Errorf("junk order risk = %q, want advisory", opt.Risk)
		}
		// The poster watch is fully reversible BECAUSE it disables rather than
		// deletes — deleting would take the hit history with it.
		if opt.ID == optPosterWatch && opt.Risk != pluginapi.RiskReversible {
			t.Errorf("poster watch risk = %q, want reversible", opt.Risk)
		}
	}
}

// The poster-watch token must carry EVERY row, not just the ones changed, so a
// rollback restores the table as it was rather than as it is minus edits.
func TestPosterWatchTokenRoundTripsEveryRow(t *testing.T) {
	rows := []posterWatchRow{
		{Pattern: "aninzb", Note: "checking uploads", Enabled: true},
		{Pattern: "retired", Note: "done with this one", Enabled: false},
	}
	token, err := encodeToken(optPosterWatch, rows)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := decodeToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if tok.ID != optPosterWatch {
		t.Fatalf("token id = %q", tok.ID)
	}
	var back []posterWatchRow
	if err := json.Unmarshal(tok.Payload, &back); err != nil {
		t.Fatal(err)
	}
	if len(back) != 2 {
		t.Fatalf("token carries %d row(s), want both — including the already-disabled one", len(back))
	}
	for i, w := range back {
		if w != rows[i] {
			t.Errorf("row %d round-tripped as %+v, want %+v", i, w, rows[i])
		}
	}
}
