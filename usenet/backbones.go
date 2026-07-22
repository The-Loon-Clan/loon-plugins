package usenet

import (
	"strings"
	"sync"
)

// Backbone lookup: map a server hostname to the backbone that issues its article
// numbers, so the wizard can pre-fill a field the operator would otherwise have
// to know from memory.
//
// This is a HINT, never an authority. Backbone deals change — providers switch
// upstream, get acquired, multi-home — so a stale row here must not be able to
// do damage. It therefore only ever pre-fills a visible, editable field; nothing
// reads it at crawl time, and an operator's explicit value always wins.
//
// The list itself is provisional; see seed/backbones.tsv.

const backboneSeedPath = "seed/backbones.tsv"

type backboneEntry struct {
	Domain     string
	Backbone   string
	Confidence string
	Notes      string
}

var (
	backboneOnce sync.Once
	backboneList []backboneEntry
)

func loadBackbones() []backboneEntry {
	backboneOnce.Do(func() {
		recs, err := seedRecords(seedData, backboneSeedPath, 2)
		if err != nil {
			// Non-fatal: without the table the wizard just stops pre-filling.
			// Losing a convenience must never stop the plugin from starting.
			return
		}
		for _, rec := range recs {
			d := strings.ToLower(col(rec, 0))
			b := strings.ToLower(col(rec, 1))
			if d == "" || b == "" {
				continue
			}
			backboneList = append(backboneList, backboneEntry{
				Domain: d, Backbone: b, Confidence: col(rec, 2), Notes: col(rec, 3),
			})
		}
	})
	return backboneList
}

// backboneForHost returns the known backbone for a server hostname, or "" when
// it isn't in the list.
//
// Matching is on the registrable domain: news.eweka.nl matches eweka.nl, but
// notaneweka.nl does not — the boundary must be a real label break, or
// "eweka.nl" would also match "fake-eweka.nl" and mis-key a provider's crawl
// state onto someone else's backbone.
func backboneForHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return ""
	}
	// Tolerate a port or a scheme being pasted in.
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexByte(h, '/'); i >= 0 {
		h = h[:i]
	}
	if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	h = strings.TrimSuffix(h, ".")

	// Longest match wins, so an entry for a subdomain beats its parent domain.
	best, found := "", ""
	for _, e := range loadBackbones() {
		if h != e.Domain && !strings.HasSuffix(h, "."+e.Domain) {
			continue
		}
		if len(e.Domain) > len(best) {
			best, found = e.Domain, e.Backbone
		}
	}
	return found
}
