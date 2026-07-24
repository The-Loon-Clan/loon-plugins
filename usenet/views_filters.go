package usenet

import (
	"context"
	"html/template"
	"net/url"
	"regexp"
	"strconv"

	"github.com/gin-gonic/gin"
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

	return p.frag("filters.html", map[string]any{
		"Rules": vms, "Fields": blacklistFields,
		"Hits": hvms, "TotalHits": total,
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
