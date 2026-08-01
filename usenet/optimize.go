package usenet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The usenet plugin's optimizations (pluginapi/optimization.go).
//
// Everything here already existed as admin-page machinery — rankJunkRules,
// recommendedOrder, setJunkRulePositions. This exposes the same computation
// through the contract so it can be enumerated, explained and applied by
// something other than a browser, with an undo the UI has never had.

const (
	optJunkOrder = "usenet.junk-order"
)

// optimizer implements pluginapi.Optimizer for this plugin. A separate type
// rather than methods on Plugin, so the capability surface is exactly this and
// registering it cannot accidentally hand a peer the whole plugin.
type optimizer struct{ p *Plugin }

func (o optimizer) Optimizations(ctx context.Context) ([]pluginapi.Optimization, error) {
	return []pluginapi.Optimization{{
		ID:    optJunkOrder,
		Title: "Junk-rule evaluation order",
		Summary: "Order the junk rules by how much each actually catches. " +
			"`match` returns on the first hit, so every article a late rule " +
			"eventually catches has first paid for every rule above it.",
		Group: "throughput",
		// Advisory, not merely reversible: positions are fully restorable, but
		// order decides which rule is CREDITED in filter_hits where two both
		// match, so the attribution an operator tunes against reads
		// differently afterwards. That is a judgement being overwritten, and
		// the caller should be told so.
		Risk: pluginapi.RiskAdvisory,
	}}, nil
}

func (o optimizer) Inspect(ctx context.Context, id string) (pluginapi.Recommendation, error) {
	if id != optJunkOrder {
		return pluginapi.Recommendation{}, fmt.Errorf("usenet: unknown optimization %q", id)
	}
	rows, err := o.p.junkOrderRows(ctx)
	if err != nil {
		return pluginapi.Recommendation{}, err
	}
	list, _ := o.Optimizations(ctx)
	rec := pluginapi.Recommendation{Optimization: list[0]}

	want := recommendedOrder(rows)
	byName := make(map[string]junkOrderRow, len(rows))
	for _, r := range rows {
		byName[r.Name] = r
	}
	// Ordered by the position each rule would MOVE TO, so the change list reads
	// as the resulting order rather than as an unordered set of edits.
	names := make([]string, 0, len(want))
	for n := range want {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return want[names[i]] < want[names[j]] })
	for _, n := range names {
		if cur, ok := byName[n]; ok && cur.Position != want[n] {
			rec.Changes = append(rec.Changes, pluginapi.Change{
				Key: n, From: strconv.Itoa(cur.Position), To: strconv.Itoa(want[n]),
			})
		}
	}
	rec.NoOp = len(rec.Changes) == 0

	// Evidence: the specific mismatch, not a generic claim. The worst offender
	// is the rule with the most hits sitting furthest from where its catch rate
	// puts it — which is exactly what an operator would look for by hand.
	var worst junkOrderRow
	for _, r := range rows {
		if r.Enabled && r.Drift > worst.Drift {
			worst = r
		}
	}
	var totalHits int64
	for _, r := range rows {
		if r.Enabled {
			totalHits += r.Hits
		}
	}
	rec.Evidence = []pluginapi.Metric{
		{Label: "Rules", Value: strconv.Itoa(len(rows)), Note: "enabled and disabled"},
		{Label: "Lifetime junk hits", Value: fmtComma(totalHits)},
		{Label: "Rules out of position", Value: strconv.Itoa(len(rec.Changes))},
	}
	if worst.Name != "" {
		rec.Evidence = append(rec.Evidence, pluginapi.Metric{
			Label: "Worst placement",
			Value: fmt.Sprintf("%s: %s hits, %d slot(s) too late", worst.Name, worst.HitsFmt, worst.Drift),
			Note:  "positive drift is the expensive direction — every article it catches pays for the rules above it first",
		})
	}
	switch {
	case rec.NoOp:
		rec.Impact = "Already in the recommended order; nothing to apply."
	default:
		rec.Impact = "Reduces junk-engine CPU per article. The size of the win depends on how much " +
			"of the feed is junk (~96% here) and cannot be predicted precisely — measure backfill " +
			"rate before and after."
	}
	return rec, nil
}

func (o optimizer) Apply(ctx context.Context, id, confirm string) (pluginapi.Applied, error) {
	if id != optJunkOrder {
		return pluginapi.Applied{}, fmt.Errorf("usenet: unknown optimization %q", id)
	}
	if confirm != id {
		return pluginapi.Applied{}, fmt.Errorf("usenet: confirm must be %q, got %q", id, confirm)
	}
	rows, err := o.p.junkOrderRows(ctx)
	if err != nil {
		return pluginapi.Applied{}, err
	}
	// The undo is captured HERE, from the state being replaced — not
	// pre-generated. A stale undo restores positions that are no longer the
	// ones in place, which is worse than having none.
	token, err := encodeJunkOrderToken(rows)
	if err != nil {
		return pluginapi.Applied{}, err
	}
	want := recommendedOrder(rows)
	changed := 0
	for _, r := range rows {
		if want[r.Name] != r.Position {
			changed++
		}
	}
	if changed == 0 {
		return pluginapi.Applied{ID: id, Changed: 0, RollbackToken: token,
			Summary: "already in the recommended order"}, nil
	}
	if err := o.p.st.setJunkRulePositions(ctx, want); err != nil {
		return pluginapi.Applied{}, err
	}
	return pluginapi.Applied{
		ID: id, Changed: changed, RollbackToken: token,
		Summary: fmt.Sprintf("reordered %d of %d junk rule(s) by lifetime hit count", changed, len(rows)),
	}, nil
}

func (o optimizer) Rollback(ctx context.Context, token string) error {
	id, order, err := decodeJunkOrderToken(token)
	if err != nil {
		return err
	}
	if id != optJunkOrder {
		return fmt.Errorf("usenet: rollback token is for %q, not an optimization this plugin owns", id)
	}
	return o.p.st.setJunkRulePositions(ctx, order)
}

// junkOrderToken carries the replaced order inside the token itself.
//
// Stateless on purpose: no table, no migration, and nothing to go stale or be
// lost in a restart. The token is only meaningful to a caller already past the
// ops API's IP allowlist and bearer token, and the worst a forged one can do is
// set rule positions — which the next Apply corrects.
type junkOrderToken struct {
	ID    string         `json:"id"`
	Order map[string]int `json:"order"`
}

func encodeJunkOrderToken(rows []junkOrderRow) (string, error) {
	t := junkOrderToken{ID: optJunkOrder, Order: make(map[string]int, len(rows))}
	for _, r := range rows {
		t.Order[r.Name] = r.Position
	}
	b, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeJunkOrderToken(token string) (string, map[string]int, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return "", nil, fmt.Errorf("usenet: malformed rollback token")
	}
	var t junkOrderToken
	if err := json.Unmarshal(b, &t); err != nil {
		return "", nil, fmt.Errorf("usenet: malformed rollback token")
	}
	if len(t.Order) == 0 {
		return "", nil, fmt.Errorf("usenet: rollback token carries no order")
	}
	return t.ID, t.Order, nil
}
