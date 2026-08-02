package pluginapi

import (
	"context"
	"sort"
	"strings"

	"github.com/the-loon-clan/loon/core"
)

// The AI surface — what an agent may do to a running site.
//
// Design and the per-plugin review: docs/AI-SURFACE.md in the Indexer repo.
//
// Three tiers, because the risks are different in KIND and not merely in
// degree:
//
//	inspect   read. Always safe, always direct.
//	apply     reversible internal state. Optimizer, with a rollback token.
//	propose   outward-facing, or lands on a person. DRAFT ONLY.
//
// The third tier has no apply verb at all, and that is the point. A published
// announcement cannot be unpublished from the people who already read it; a
// moderation action lands on a member whether or not it is reverted thirty
// seconds later. No rollback token repairs either, so the agent's only move is
// to put something in front of a human.
//
// The tier is declared by the plugin and enforced at the API boundary. A
// plugin marking an action `propose` must not depend on every caller
// remembering to check — the ops handler refuses an apply on it regardless.
// That is a property of the plumbing rather than a promise about the prompt,
// which is the only kind worth having when the caller is a language model.

// ProposerSuffix is the extension-name convention, matching OptimizerSuffix. A
// plugin publishes its proposal surface as "<plugin>.proposer".
const ProposerSuffix = ".proposer"

// Tier is how much authority an action carries.
type Tier string

const (
	TierInspect Tier = "inspect"
	TierApply   Tier = "apply"
	TierPropose Tier = "propose"
)

// Action is one thing an agent can ask a plugin to do.
type Action struct {
	// ID is "<plugin>.<slug>", stable across releases — what an API caller and
	// an audit row both name.
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Tier    Tier   `json:"tier"`
	// InputHint describes what Draft expects, in prose. Deliberately not a
	// JSON Schema: the consumer is a language model, and a sentence saying
	// "the week's date range, and any themes to emphasise" is more use to it
	// than a type.
	InputHint string `json:"input_hint,omitempty"`
}

// Proposal is a draft awaiting a human.
//
// It is INERT by construction: whatever the plugin wrote is unreachable by
// normal site paths until someone approves it. A draft that is visible is not
// a draft.
type Proposal struct {
	// ID identifies the draft to the approver. Plugin-scoped; the review
	// record is the host's.
	ID     string `json:"id"`
	Action string `json:"action"`
	Title  string `json:"title"`
	// Preview is what the human will be agreeing to, rendered as they would
	// see it. Truncated by the caller for a listing, never by the plugin —
	// the approver is entitled to the whole thing.
	Preview string `json:"preview"`
	// Evidence is why this was drafted, on the same principle as
	// Recommendation.Evidence: a proposal without its reasoning asks the
	// reviewer to rubber-stamp rather than to judge.
	Evidence []Metric `json:"evidence,omitempty"`
	// Affects names who or what the proposal touches — a member, a thread, a
	// release. It is the field a reviewer scans first when the queue is long,
	// and the reason moderation proposals cannot be reviewed as a faceless
	// batch.
	Affects string `json:"affects,omitempty"`
	// ReviewURL is where a human goes to act on it.
	ReviewURL string `json:"review_url,omitempty"`
}

// Proposer is what a plugin publishes to offer drafting.
//
// Note the absence: there is no Approve, Publish or Send. Approval is a human
// action in the site's own UI, and the system applies on approval. An agent
// that could approve its own proposal would be an agent with the apply tier by
// a longer route.
type Proposer interface {
	// Actions lists what this plugin can be asked to draft or inspect.
	Actions(ctx context.Context) ([]Action, error)
	// Inspect answers a read-only question. It may return a REDACTED view by
	// design — `messages` reports that a thread was reported without
	// disclosing what anyone wrote — and redaction is the plugin's decision,
	// never the caller's.
	Inspect(ctx context.Context, id string, input map[string]string) ([]Metric, error)
	// Draft prepares a proposal and returns it. It must leave the drafted
	// thing inert.
	Draft(ctx context.Context, id string, input map[string]string) (Proposal, error)
}

// Draftable reports whether an action may be sent to Draft.
func (t Tier) Draftable() bool { return t == TierPropose }

// Appliable reports whether an action may be applied directly. Only the apply
// tier qualifies; propose actions are refused at the boundary even when a
// caller asks nicely.
func (t Tier) Appliable() bool { return t == TierApply }

// LookupProposers returns every registered proposer, keyed by extension name.
func LookupProposers(c *core.Core) map[string]Proposer {
	out := map[string]Proposer{}
	if c == nil {
		return out
	}
	for _, name := range c.ExtensionNames() {
		if !strings.HasSuffix(name, ProposerSuffix) {
			continue
		}
		if svc, ok := c.Lookup(name); ok {
			if p, ok := svc.(Proposer); ok {
				out[name] = p
			}
		}
	}
	return out
}

// ProposerFor finds the proposer owning an action ID, by plugin prefix — the
// same derivation OptimizerFor uses, so one lookup rather than N round trips.
func ProposerFor(ps map[string]Proposer, id string) (Proposer, bool) {
	plugin, _, ok := strings.Cut(id, ".")
	if !ok || plugin == "" {
		return nil, false
	}
	p, ok := ps[plugin+ProposerSuffix]
	return p, ok
}

// AllActions gathers every action across plugins, sorted by tier then ID.
//
// Tier first so a listing reads least-authority-first: an agent scanning it
// meets everything it can do freely before anything it must ask about.
func AllActions(ctx context.Context, ps map[string]Proposer) ([]Action, error) {
	var out []Action
	var firstErr error
	for _, p := range ps {
		list, err := p.Actions(ctx)
		if err != nil {
			// One plugin failing must not hide the rest — a broken proposer is
			// a smaller problem than an empty list that looks healthy.
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out = append(out, list...)
	}
	rank := map[Tier]int{TierInspect: 0, TierApply: 1, TierPropose: 2}
	sort.Slice(out, func(i, j int) bool {
		if rank[out[i].Tier] != rank[out[j].Tier] {
			return rank[out[i].Tier] < rank[out[j].Tier]
		}
		return out[i].ID < out[j].ID
	})
	return out, firstErr
}
