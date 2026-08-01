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
// Both of these were already known problems with measured costs and no way to
// act on them except by hand. Neither needed new logic — junk order reuses
// rankJunkRules/recommendedOrder, the poster watch reuses setPosterWatch — so
// what this file adds is the explanation, the undo, and a machine-readable
// surface for both.

const (
	optJunkOrder   = "usenet.junk-order"
	optPosterWatch = "usenet.poster-watch-idle"
)

// articleLoadMillis is the measured cost of loading one set's staged articles
// from Redis. It prices the poster-watch trade in human terms; it is an
// observed average rather than a guarantee, and the evidence text says so.
const articleLoadMillis = 16

// outcomeWindowDays is how far back the poster-watch evidence looks. A week
// spans a full crawl/backfill cycle without letting a long-retired watch be
// judged on traffic from a different configuration.
const outcomeWindowDays = 7

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
	}, {
		ID:    optPosterWatch,
		Title: "Idle poster watches",
		Summary: "Disable poster watches that are no longer being investigated. " +
			"ANY active watch turns off the builder's title fast path for EVERY " +
			"set, because attribution needs the articles — so a set that could " +
			"have been rejected from its subject in microseconds pays a full " +
			"staged-article load first.",
		Group: "throughput",
		// Reversible in the strict sense: the watches are DISABLED, not
		// deleted, so the rows and their accumulated poster_hits history
		// survive and re-enabling restores them exactly.
		Risk: pluginapi.RiskReversible,
	}}, nil
}

func (o optimizer) listed(ctx context.Context, id string) (pluginapi.Optimization, error) {
	list, err := o.Optimizations(ctx)
	if err != nil {
		return pluginapi.Optimization{}, err
	}
	for _, opt := range list {
		if opt.ID == id {
			return opt, nil
		}
	}
	return pluginapi.Optimization{}, fmt.Errorf("usenet: unknown optimization %q", id)
}

func (o optimizer) Inspect(ctx context.Context, id string) (pluginapi.Recommendation, error) {
	opt, err := o.listed(ctx, id)
	if err != nil {
		return pluginapi.Recommendation{}, err
	}
	switch id {
	case optJunkOrder:
		return o.inspectJunkOrder(ctx, opt)
	case optPosterWatch:
		return o.inspectPosterWatch(ctx, opt)
	}
	return pluginapi.Recommendation{}, fmt.Errorf("usenet: %q has no inspector", id)
}

func (o optimizer) Apply(ctx context.Context, id, confirm string) (pluginapi.Applied, error) {
	if _, err := o.listed(ctx, id); err != nil {
		return pluginapi.Applied{}, err
	}
	if confirm != id {
		return pluginapi.Applied{}, fmt.Errorf("usenet: confirm must be %q, got %q", id, confirm)
	}
	switch id {
	case optJunkOrder:
		return o.applyJunkOrder(ctx, id)
	case optPosterWatch:
		return o.applyPosterWatch(ctx, id)
	}
	return pluginapi.Applied{}, fmt.Errorf("usenet: %q cannot be applied", id)
}

func (o optimizer) Rollback(ctx context.Context, token string) error {
	t, err := decodeToken(token)
	if err != nil {
		return err
	}
	switch t.ID {
	case optJunkOrder:
		var order map[string]int
		if err := json.Unmarshal(t.Payload, &order); err != nil || len(order) == 0 {
			return fmt.Errorf("usenet: rollback token carries no order")
		}
		return o.p.st.setJunkRulePositions(ctx, order)
	case optPosterWatch:
		var watches []posterWatchRow
		if err := json.Unmarshal(t.Payload, &watches); err != nil || len(watches) == 0 {
			return fmt.Errorf("usenet: rollback token carries no watches")
		}
		for _, w := range watches {
			if err := o.p.st.setPosterWatch(ctx, w.Pattern, w.Note, w.Enabled); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("usenet: rollback token is for %q, not an optimization this plugin owns", t.ID)
}

// ── junk order ──────────────────────────────────────────────────────

func (o optimizer) inspectJunkOrder(ctx context.Context, opt pluginapi.Optimization) (pluginapi.Recommendation, error) {
	rows, err := o.p.junkOrderRows(ctx)
	if err != nil {
		return pluginapi.Recommendation{}, err
	}
	rec := pluginapi.Recommendation{Optimization: opt}

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

	// Evidence names the specific mismatch rather than asserting a general
	// principle. The worst offender is the rule sitting furthest from where its
	// catch rate puts it — exactly what an operator would look for by hand.
	var worst junkOrderRow
	var totalHits int64
	for _, r := range rows {
		if !r.Enabled {
			continue
		}
		totalHits += r.Hits
		if r.Drift > worst.Drift {
			worst = r
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
	if rec.NoOp {
		rec.Impact = "Already in the recommended order; nothing to apply."
	} else {
		rec.Impact = "Reduces junk-engine CPU per article. The size of the win depends on how much " +
			"of the feed is junk (~96% here) and cannot be predicted precisely — measure backfill " +
			"rate before and after."
	}
	return rec, nil
}

func (o optimizer) applyJunkOrder(ctx context.Context, id string) (pluginapi.Applied, error) {
	rows, err := o.p.junkOrderRows(ctx)
	if err != nil {
		return pluginapi.Applied{}, err
	}
	// The undo is captured HERE, from the state being replaced — not
	// pre-generated. A stale undo restores positions that are no longer the
	// ones in place, which is worse than having none.
	prior := make(map[string]int, len(rows))
	for _, r := range rows {
		prior[r.Name] = r.Position
	}
	token, err := encodeToken(id, prior)
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
		return pluginapi.Applied{ID: id, RollbackToken: token,
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

// ── idle poster watches ─────────────────────────────────────────────

func (o optimizer) inspectPosterWatch(ctx context.Context, opt pluginapi.Optimization) (pluginapi.Recommendation, error) {
	rows, err := o.p.st.posterWatchRows(ctx)
	if err != nil {
		return pluginapi.Recommendation{}, err
	}
	rec := pluginapi.Recommendation{Optimization: opt}
	var active []posterWatchRow
	for _, w := range rows {
		if w.Enabled {
			active = append(active, w)
			rec.Changes = append(rec.Changes, pluginapi.Change{
				Key: w.Pattern, From: "watching", To: "disabled",
			})
		}
	}
	rec.NoOp = len(active) == 0

	rejected, err := o.p.st.titleRejectableTotals(ctx, outcomeWindowDays)
	if err != nil {
		return pluginapi.Recommendation{}, err
	}
	rec.Evidence = []pluginapi.Metric{
		{Label: "Active watches", Value: strconv.Itoa(len(active)),
			Note: "one is enough to disable the fast path for every set"},
		{Label: fmt.Sprintf("Title-rejectable sets (%dd)", outcomeWindowDays), Value: fmtComma(rejected),
			Note: "dropped at build for a reason the subject alone decides — each paid a staged-article load first"},
	}
	if rejected > 0 && len(active) > 0 {
		hours := float64(rejected) * articleLoadMillis / 1000 / 3600
		rec.Evidence = append(rec.Evidence, pluginapi.Metric{
			Label: "Load time spent on them",
			Value: fmt.Sprintf("~%.1f h", hours),
			Note: fmt.Sprintf("at a measured ~%d ms per set; an average, not a guarantee, and "+
				"wall-clock rather than a single core", articleLoadMillis),
		})
	}

	switch {
	case rec.NoOp:
		rec.Impact = "No active watches; the title fast path is already on."
	default:
		rec.Impact = fmt.Sprintf("Restores the title fast path for every candidate set. "+
			"Disabling keeps the %d watch row(s) and their recorded hits — re-enable when "+
			"you next need per-poster attribution.", len(active))
	}
	return rec, nil
}

func (o optimizer) applyPosterWatch(ctx context.Context, id string) (pluginapi.Applied, error) {
	rows, err := o.p.st.posterWatchRows(ctx)
	if err != nil {
		return pluginapi.Applied{}, err
	}
	// The token carries every row's prior state, not just the ones changed, so
	// a rollback restores the table as it was rather than as it is minus edits.
	token, err := encodeToken(id, rows)
	if err != nil {
		return pluginapi.Applied{}, err
	}
	changed := 0
	for _, w := range rows {
		if !w.Enabled {
			continue
		}
		// Disabled, never deleted: deleting would take the accumulated
		// poster_hits history with it, and then no token could put it back.
		if err := o.p.st.setPosterWatch(ctx, w.Pattern, w.Note, false); err != nil {
			return pluginapi.Applied{}, err
		}
		changed++
	}
	if changed == 0 {
		return pluginapi.Applied{ID: id, RollbackToken: token,
			Summary: "no active poster watches"}, nil
	}
	return pluginapi.Applied{
		ID: id, Changed: changed, RollbackToken: token,
		Summary: fmt.Sprintf("disabled %d poster watch(es); the title fast path is on again", changed),
	}, nil
}

// ── rollback tokens ─────────────────────────────────────────────────

// optToken is the rollback envelope: which optimization, and the state it
// replaced, as a payload only that optimization understands.
//
// Stateless on purpose: no table, no migration, nothing to go stale or be lost
// in a restart. It is only meaningful to a caller already past the ops API's
// IP allowlist and bearer token, and the worst a forged one can do is restore
// settings the next Apply corrects.
type optToken struct {
	ID      string          `json:"id"`
	Payload json.RawMessage `json:"payload"`
}

func encodeToken(id string, payload any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(optToken{ID: id, Payload: raw})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeToken(token string) (optToken, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return optToken{}, fmt.Errorf("usenet: malformed rollback token")
	}
	var t optToken
	if err := json.Unmarshal(b, &t); err != nil || t.ID == "" || len(t.Payload) == 0 {
		return optToken{}, fmt.Errorf("usenet: malformed rollback token")
	}
	return t, nil
}
