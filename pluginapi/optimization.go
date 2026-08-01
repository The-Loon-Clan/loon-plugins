package pluginapi

import (
	"context"
	"sort"
	"strings"

	"github.com/the-loon-clan/loon/core"
)

// Optimizations — tunables a plugin can measure, recommend, apply and undo.
//
// The shape comes from a real one. The usenet junk engine returns on the FIRST
// rule that matches, so where a rule sits decides how much CPU every article
// above it costs; production ran with a rule catching 4 BILLION articles at
// position 13, behind one costing 81% of the engine's benchmarked CPU for 0.3%
// of the catches. Nothing surfaced the mismatch, because hit counts and
// evaluation order were never shown together.
//
// Fixing that took: measuring, ranking, generating the change, generating its
// undo, checking the two agreed, and applying. All of it by hand, none of it
// specific to junk rules. This interface is that sequence, so the next tunable
// costs a Recommendation instead of an afternoon.
//
// Three rules the contract enforces rather than documents:
//
//  1. A recommendation carries its EVIDENCE. Without measurements a
//     recommendation is a guess with a UI, and the operator has no way to
//     disagree with it on the merits.
//  2. Apply returns a rollback token captured from state read AT APPLY TIME.
//     A pre-generated undo goes stale: the junk-order rollback script drifted
//     in two days and would have restored positions that were no longer in
//     place, which is an undo that makes things worse.
//  3. Anything not fully reversible is not reachable here. This surface is
//     designed to be driven by automation, including an AI agent, and the
//     blast radius has to be bounded by construction rather than by the
//     caller's good judgement.

// OptimizerSuffix is the extension-name convention. A plugin publishes its
// optimizer as "<plugin>.optimizer" during Provision; the host discovers every
// one of them through core.ExtensionNames, so adding a plugin adds its
// optimizations with no host change.
const OptimizerSuffix = ".optimizer"

// Risk bounds what may be applied through an API rather than by a human.
type Risk string

const (
	// RiskReversible: the rollback token fully restores the prior state.
	RiskReversible Risk = "reversible"
	// RiskAdvisory: nothing is lost, but a judgement is being overwritten —
	// reordering rules rewrites which rule gets CREDITED in the hit counters
	// an operator tunes against, so the numbers mean something new afterwards.
	RiskAdvisory Risk = "advisory"
	// RiskManual: never exposed through the API. Anything that deletes data,
	// or that no token can undo, stays a human action.
	RiskManual Risk = "manual"
)

// APIReachable reports whether an optimization may be applied through the ops
// API. The gate lives on the type so every caller inherits it.
func (r Risk) APIReachable() bool { return r == RiskReversible || r == RiskAdvisory }

// Optimization is one tunable, as listed.
type Optimization struct {
	// ID is "<plugin>.<slug>", stable across releases — it is what an API
	// caller and an audit row both name.
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	// Group buckets the listing: "throughput", "storage", "correctness".
	Group string `json:"group"`
	Risk  Risk   `json:"risk"`
}

// Change is one field moving from one value to another, in terms the operator
// reads rather than the plugin's internals.
type Change struct {
	Key  string `json:"key"`
	From string `json:"from"`
	To   string `json:"to"`
}

// Metric is one piece of the measured basis. Value is pre-formatted: the
// plugin knows whether its number is bytes, a percentage or a rate, and the
// renderer should not have to guess.
type Metric struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Note  string `json:"note,omitempty"`
}

// Recommendation is what an optimization would do, and why.
type Recommendation struct {
	Optimization
	Changes  []Change `json:"changes"`
	Evidence []Metric `json:"evidence"`
	// Impact states the expected effect and its uncertainty. "Unknown until
	// measured" is a valid and preferable answer to a fabricated percentage.
	Impact string `json:"impact"`
	// NoOp is true when the current state is already the recommended one, so a
	// caller can skip Apply instead of writing a no-change transaction.
	NoOp bool `json:"noop"`
}

// Applied is the receipt. Keep RollbackToken: it is the only way back.
type Applied struct {
	ID            string `json:"id"`
	Changed       int    `json:"changed"`
	RollbackToken string `json:"rollback_token"`
	Summary       string `json:"summary"`
}

// Optimizer is what a plugin publishes.
//
// Inspect is always safe and never mutates, so an agent can enumerate and
// reason over every optimization without taking any action. Apply is the only
// mutating call and requires the caller to echo a confirmation phrase, which
// keeps "list everything" and "change something" from being one careless step
// apart.
type Optimizer interface {
	Optimizations(ctx context.Context) ([]Optimization, error)
	Inspect(ctx context.Context, id string) (Recommendation, error)
	// Apply mutates. confirm must equal the optimization's ID; anything else
	// is refused. The phrase is deliberately the ID rather than "yes" so a
	// caller cannot confirm one optimization while meaning another.
	Apply(ctx context.Context, id, confirm string) (Applied, error)
	Rollback(ctx context.Context, token string) error
}

// LookupOptimizers returns every registered optimizer, keyed by extension name.
//
// Discovery is by suffix rather than a fixed list, so a new plugin's
// optimizations appear without the host learning its name.
func LookupOptimizers(c *core.Core) map[string]Optimizer {
	out := map[string]Optimizer{}
	if c == nil {
		return out
	}
	for _, name := range c.ExtensionNames() {
		if !strings.HasSuffix(name, OptimizerSuffix) {
			continue
		}
		if svc, ok := c.Lookup(name); ok {
			if o, ok := svc.(Optimizer); ok {
				out[name] = o
			}
		}
	}
	return out
}

// OptimizerFor finds the optimizer owning an ID, by matching the plugin prefix
// against the extension name. IDs are "<plugin>.<slug>" and extensions are
// "<plugin>.optimizer", so the owner is derivable without asking every plugin
// to enumerate — which would turn one lookup into N round trips.
func OptimizerFor(opts map[string]Optimizer, id string) (Optimizer, bool) {
	plugin, _, ok := strings.Cut(id, ".")
	if !ok || plugin == "" {
		return nil, false
	}
	o, ok := opts[plugin+OptimizerSuffix]
	return o, ok
}

// AllOptimizations gathers and sorts every optimization across plugins.
//
// Sorted by group then ID so the listing is stable: an agent diffing two calls
// should see changes in the data, not in map iteration order.
func AllOptimizations(ctx context.Context, opts map[string]Optimizer) ([]Optimization, error) {
	var out []Optimization
	var firstErr error
	for _, o := range opts {
		list, err := o.Optimizations(ctx)
		if err != nil {
			// One plugin failing must not hide the rest — a broken optimizer
			// is a smaller problem than an empty list that looks healthy.
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out = append(out, list...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].ID < out[j].ID
	})
	return out, firstErr
}
