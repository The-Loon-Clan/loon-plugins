package usenet

import (
	"context"
	"html/template"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Filters view: the operator blacklist and the per-rule filter-hit counters.

type blacklistVM struct {
	ID      int64
	Pattern string
	Field   string
	Enabled bool
	Invalid string // set when a stored pattern no longer compiles
}

type filterHitVM struct {
	Kind, Rule, Sample string
	Count              int64
	Pct                float64
	LastSeen           string
}

// sweepVM is one row of the host's stored-catalogue sweep attribution
// (the optional UsenetJunkSweepName capability).
type sweepVM struct {
	Pattern, Sample     string
	Count               int64
	Pct                 float64
	FirstSeen, LastSeen string
}

func (p *Plugin) renderFilters(ctx context.Context, msg, errMsg string) (template.HTML, error) {
	rules, err := p.st.blacklistRules(ctx)
	if err != nil {
		return "", err
	}
	vms := make([]blacklistVM, len(rules))
	for i, r := range rules {
		vm := blacklistVM{ID: r.ID, Pattern: r.Pattern, Field: r.Field, Enabled: r.Enabled}
		// Patterns are validated on the way in, so this only fires for a row
		// edited directly in SQL — exactly the case where a rule looks active,
		// is silently inert, and nothing else would tell the operator.
		if _, cerr := regexp.Compile(r.Pattern); cerr != nil {
			vm.Invalid = cerr.Error()
		}
		vms[i] = vm
	}

	hits, err := p.st.filterHitRows(ctx)
	if err != nil {
		return "", err
	}
	var total int64
	for _, h := range hits {
		total += h.TotalCount
	}
	hvms := make([]filterHitVM, len(hits))
	for i, h := range hits {
		vm := filterHitVM{
			Kind: h.Kind, Rule: h.Rule, Sample: h.LastSample,
			Count: h.TotalCount, LastSeen: fmtTime(h.LastSeen),
		}
		if total > 0 {
			vm.Pct = float64(h.TotalCount) * 100 / float64(total)
		}
		hvms[i] = vm
	}

	// Host sweep attribution (optional capability): which junk rule tagged how
	// many already-indexed releases. Ingest hits above answer "what are we
	// dropping"; this answers "what got past ingest and had to be swept" —
	// together they say whether a rule works at ingest, only in the sweep, or
	// not at all. Absent capability (internal-sink installs) hides the card.
	var svms []sweepVM
	var sweepTotal int64
	if prov, ok := pluginapi.LookupJunkSweepStats(p.core); ok {
		rows, err := prov.JunkSweepStats(ctx)
		if err != nil {
			return "", err
		}
		for _, r := range rows {
			sweepTotal += r.Count
		}
		svms = make([]sweepVM, len(rows))
		for i, r := range rows {
			vm := sweepVM{
				Pattern: r.Pattern, Sample: r.LastSample, Count: r.Count,
				FirstSeen: fmtTime(r.FirstSeen), LastSeen: fmtTime(r.LastSeen),
			}
			if sweepTotal > 0 {
				vm.Pct = float64(r.Count) * 100 / float64(sweepTotal)
			}
			svms[i] = vm
		}
	}

	// Poster watch: the same events as Hits above, indexed by POSTER instead of
	// by rule, plus the successes. "Which rule drops the most" is the right
	// question when tuning rules and the wrong one when an operator says "this
	// poster puts out a hundred a day and I have four".
	watched, err := p.st.posterWatchPatterns(ctx)
	if err != nil {
		return "", err
	}
	phits, err := p.st.posterHitRows(ctx, 200)
	if err != nil {
		return "", err
	}

	// The junk-rule ORDER editor. Hit counts and evaluation order were both
	// already visible — but never together, which is how a rule catching 3.5
	// billion articles came to sit thirteenth, behind one costing 81% of the
	// engine for 0.3% of the catches. Showing them in one table is the whole
	// point; the recommendation is advisory (see junk_order.go).
	jrows, jerr := p.junkOrderRows(ctx)
	if jerr != nil {
		// Non-fatal: the rest of the tab is still worth rendering, and the
		// filter itself is unaffected by this readout failing.
		p.reportErr(ctx, "usenet/junk-rule-stats", jerr)
	}

	return p.frag("filters.html", map[string]any{
		"JunkRules": jrows,
		"Rules":     vms, "Fields": blacklistFields,
		"Hits": hvms, "TotalHits": total,
		"Sweep": svms, "SweepTotal": sweepTotal,
		"Watched": watched, "PosterHits": phits,
		"Msg": msg, "Err": errMsg,
	})
}

func filtersRedirect(gc *gin.Context, key, val string) (template.HTML, error) {
	return redirect(gc, usenetURL+"?"+key+"="+url.QueryEscape(val)+"#filters")
}

func (p *Plugin) actionAddBlacklist(gc *gin.Context) (template.HTML, error) {
	pattern := gc.PostForm("pattern")
	field := gc.PostForm("field")
	if err := p.st.addBlacklistRule(gc.Request.Context(), pattern, field); err != nil {
		return filtersRedirect(gc, "err", err.Error())
	}
	// Apply immediately rather than at the next build pass: an operator adding a
	// rule has just decided they do not want that content, and waiting a cycle
	// means it lands anyway.
	p.reloadBlacklist(gc.Request.Context())
	return filtersRedirect(gc, "msg", "rule added and applied")
}

func (p *Plugin) actionToggleBlacklist(gc *gin.Context) (template.HTML, error) {
	id, _ := strconv.ParseInt(gc.PostForm("id"), 10, 64)
	if err := p.st.toggleBlacklistRule(gc.Request.Context(), id); err != nil {
		return filtersRedirect(gc, "err", err.Error())
	}
	p.reloadBlacklist(gc.Request.Context())
	return filtersRedirect(gc, "msg", "rule toggled")
}

func (p *Plugin) actionDeleteBlacklist(gc *gin.Context) (template.HTML, error) {
	id, _ := strconv.ParseInt(gc.PostForm("id"), 10, 64)
	if err := p.st.deleteBlacklistRule(gc.Request.Context(), id); err != nil {
		return filtersRedirect(gc, "err", err.Error())
	}
	p.reloadBlacklist(gc.Request.Context())
	return filtersRedirect(gc, "msg", "rule deleted")
}

func (p *Plugin) actionResetHits(gc *gin.Context) (template.HTML, error) {
	if err := p.st.resetFilterHits(gc.Request.Context()); err != nil {
		return filtersRedirect(gc, "err", err.Error())
	}
	return filtersRedirect(gc, "msg", "counters cleared")
}

// actionAddPosterWatch starts tracing a poster. Substring, case-insensitive —
// the operator knows the poster, not the exact From header formatting.
func (p *Plugin) actionAddPosterWatch(gc *gin.Context) (template.HTML, error) {
	pat := strings.TrimSpace(gc.PostForm("pattern"))
	if pat == "" {
		return filtersRedirect(gc, "err", "no poster pattern given")
	}
	if len(pat) < 3 {
		// A one- or two-character substring matches most of Usenet, and the
		// watch sits on the per-article ingest path.
		return filtersRedirect(gc, "err", "pattern too short — use at least 3 characters")
	}
	if err := p.st.setPosterWatch(gc.Request.Context(), pat, strings.TrimSpace(gc.PostForm("note")), true); err != nil {
		return filtersRedirect(gc, "err", err.Error())
	}
	return filtersRedirect(gc, "msg", "watching "+pat+" — attribution appears after the next crawl pass")
}

func (p *Plugin) actionDeletePosterWatch(gc *gin.Context) (template.HTML, error) {
	pat := strings.TrimSpace(gc.PostForm("pattern"))
	if pat == "" {
		return filtersRedirect(gc, "err", "no poster pattern given")
	}
	if err := p.st.deletePosterWatch(gc.Request.Context(), pat); err != nil {
		return filtersRedirect(gc, "err", err.Error())
	}
	return filtersRedirect(gc, "msg", "stopped watching "+pat)
}

// actionJunkMove shifts one rule a slot within the band it competes in.
//
// Order is the cheapest lever this page has: `match` returns on the first hit
// and most ingested articles are junk, so a rule's position decides how much
// work every article above it costs.
func (p *Plugin) actionJunkMove(gc *gin.Context) (template.HTML, error) {
	ctx := gc.Request.Context()
	name := strings.TrimSpace(gc.PostForm("name"))
	if name == "" {
		return filtersRedirect(gc, "err", "no rule named")
	}
	rows, err := p.junkOrderRows(ctx)
	if err != nil {
		return filtersRedirect(gc, "err", err.Error())
	}
	order := moveJunkRule(rows, name, gc.PostForm("dir") == "up")
	if err := p.st.setJunkRulePositions(ctx, order); err != nil {
		return filtersRedirect(gc, "err", err.Error())
	}
	p.logAction("junk rule %q moved %s", name, gc.PostForm("dir"))
	p.reloadJunkRules(ctx)
	return filtersRedirect(gc, "msg", "order updated — applies to the next crawl round")
}

// actionJunkApplyOrder adopts the hit-ranked order wholesale.
//
// Type-to-confirm, because it rewrites every position at once and the previous
// order is not recoverable from the page afterwards. The recommendation is
// advisory by design (see junk_order.go): lifetime hit counts describe the
// past, and order also decides which rule is CREDITED when two both match.
func (p *Plugin) actionJunkApplyOrder(gc *gin.Context) (template.HTML, error) {
	ctx := gc.Request.Context()
	if strings.TrimSpace(gc.PostForm("confirm")) != "reorder" {
		return filtersRedirect(gc, "err", `type "reorder" to confirm — this rewrites every rule's position`)
	}
	rows, err := p.junkOrderRows(ctx)
	if err != nil {
		return filtersRedirect(gc, "err", err.Error())
	}
	order := recommendedOrder(rows)
	if err := p.st.setJunkRulePositions(ctx, order); err != nil {
		return filtersRedirect(gc, "err", err.Error())
	}
	p.logAction("junk rules reordered by hit rate (%d rules)", len(order))
	p.reloadJunkRules(ctx)
	return filtersRedirect(gc, "msg", "rules reordered by hit rate — applies to the next crawl round")
}

// actionJunkToggle enables or disables one rule. Disabling retires a rule that
// has stopped earning its cost WITHOUT deleting the row, so its hit history
// survives to justify the decision later.
func (p *Plugin) actionJunkToggle(gc *gin.Context) (template.HTML, error) {
	ctx := gc.Request.Context()
	name := strings.TrimSpace(gc.PostForm("name"))
	if name == "" {
		return filtersRedirect(gc, "err", "no rule named")
	}
	on := gc.PostForm("enabled") == "true"
	if err := p.st.setJunkRuleEnabled(ctx, name, on); err != nil {
		return filtersRedirect(gc, "err", err.Error())
	}
	state := "disabled"
	if on {
		state = "enabled"
	}
	p.logAction("junk rule %q %s", name, state)
	p.reloadJunkRules(ctx)
	return filtersRedirect(gc, "msg", "rule "+state+" — applies to the next crawl round")
}

// junkOrderRows is the shared read: rules in evaluation order, ranked.
func (p *Plugin) junkOrderRows(ctx context.Context) ([]junkOrderRow, error) {
	stats, err := p.st.junkRuleStats(ctx)
	if err != nil {
		return nil, err
	}
	sized := map[string]bool{}
	if specs, serr := p.st.junkRules(ctx); serr == nil {
		for _, sp := range specs {
			if sp.Params.sized() || sp.Params.SizedOnly {
				sized[sp.Name] = true
			}
		}
	}
	return rankJunkRules(stats, sized), nil
}
