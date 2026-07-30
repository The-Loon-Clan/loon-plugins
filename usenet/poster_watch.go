package usenet

import (
	"strings"
	"sync"
)

// Poster watch: per-poster attribution of why releases do not appear.
//
// filter_hits answers "which rule is dropping the most", which is the right
// question when tuning rules. It is the wrong one when an operator says "this
// poster puts out a hundred releases a day and I have four of them" — that
// needs the same events indexed by poster instead of by rule, and needs the
// SUCCESSES recorded too, because "nothing at all for this poster" and "all of
// it junked at ingest" look identical from the outside and have opposite fixes.

// posterWatch decides whether a From header is being traced. Empty means the
// feature is off and costs one length check per article, which matters: this is
// consulted on the per-article ingest path, millions of times a pass.
type posterWatch struct {
	patterns []string // already lowercased
}

// newPosterWatch compiles the operator's patterns. Matching is
// case-insensitive substring, so "tsukihime" catches
// "TsukiHime <usenet.bot@tsukihime.org>" without the operator reproducing the
// exact formatting of a header they may never have seen.
func newPosterWatch(patterns []string) *posterWatch {
	w := &posterWatch{}
	for _, p := range patterns {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			w.patterns = append(w.patterns, p)
		}
	}
	return w
}

// active reports whether any poster is being traced.
//
// The builder consults this to decide whether it may skip loading a set's
// articles for a title that is obviously junk. Attribution is the entire point
// of the watch, so when one is active the slower path is taken and nothing is
// traded away; when the list is empty — the normal state — there is nothing to
// attribute and the read can be skipped outright.
func (w *posterWatch) active() bool { return w != nil && len(w.patterns) > 0 }

// watched reports whether this From header is being traced, and returns the
// PATTERN rather than the raw header as the key.
//
// Keying on the pattern is deliberate: a poster's From header varies (display
// name added, address reformatted, a bot appending a counter), and keying on
// the raw value would scatter one poster's history across rows that never add
// up. The operator asked about a poster, so the answer is filed under what they
// asked.
func (w *posterWatch) watched(from string) (string, bool) {
	if w == nil || len(w.patterns) == 0 || from == "" {
		return "", false
	}
	lower := strings.ToLower(from)
	for _, p := range w.patterns {
		if strings.Contains(lower, p) {
			return p, true
		}
	}
	return "", false
}

// posterHits accumulates per-poster outcomes in memory, flushed in one batch at
// the end of a pass — the same arrangement as filterHits, for the same reason:
// a watched poster in a busy group generates tens of thousands of events an
// hour and a write each would cost more than the crawl.
type posterHits struct {
	mu   sync.Mutex
	hits map[posterHitKey]*posterHitVal
}

type posterHitKey struct{ poster, stage, reason string }

type posterHitVal struct {
	count  int64
	sample string
}

func newPosterHits() *posterHits {
	return &posterHits{hits: make(map[posterHitKey]*posterHitVal)}
}

// note records one outcome for a watched poster.
//
// nil-safe and no-ops on an empty reason, so the pure helpers that call it can
// be tested without a plugin and a missing counter can never alter filtering
// behaviour — an observability feature that changes what gets indexed would be
// worse than none.
func (p *posterHits) note(poster, stage, reason, sample string) {
	if p == nil || poster == "" || reason == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	k := posterHitKey{poster, stage, reason}
	v := p.hits[k]
	if v == nil {
		// Keep the FIRST sample of a pass, matching filterHits: a stable sample
		// means an operator refreshing the page is not chasing a moving target.
		p.hits[k] = &posterHitVal{count: 1, sample: sample}
		return
	}
	v.count++
	if v.sample == "" {
		v.sample = sample
	}
}

// drain returns the accumulated tallies and resets, so a flush cannot
// double-count if it overlaps the next pass.
func (p *posterHits) drain() map[posterHitKey]*posterHitVal {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.hits) == 0 {
		return nil
	}
	out := p.hits
	p.hits = make(map[posterHitKey]*posterHitVal)
	return out
}
