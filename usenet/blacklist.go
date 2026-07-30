package usenet

import (
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Regex blacklist, lifted from the prod site's isBlacklisted (the legacy
// assembler — deleted since; this is the canonical copy now).
//
// Where junk rules are shipped defaults for machine-generated garbage, the
// blacklist is the operator's own policy: a poster they don't want, a group they
// carry but don't index, a title pattern specific to their site. Same runtime
// shape as the junk matcher — rules live in the database, are compiled ONCE into
// an immutable matcher, and the matcher is swapped wholesale via an atomic
// pointer — because this also runs per release on the build path and must never
// touch the database there.
//
// Empty is the correct default. A blacklist ships with no rules at all.

// blacklistFields are the parts of a release a rule can be tested against.
// Anything else never matches: a typo in the field name must fail CLOSED
// (index everything) rather than open (drop everything).
var blacklistFields = []string{"subject", "title", "poster", "group"}

// blacklistRule is one rule as stored.
type blacklistRule struct {
	ID      int64
	Pattern string
	Field   string
	Enabled bool
}

// compiledBlacklistRule is the runtime form: regex already compiled.
type compiledBlacklistRule struct {
	re    *regexp.Regexp
	field string
}

// blacklistMatcher is an immutable compiled rule set. Replace it wholesale;
// never mutate one that is in use.
type blacklistMatcher struct{ rules []compiledBlacklistRule }

// activeBlacklist holds the live matcher. Reads are lock-free.
var activeBlacklist atomic.Pointer[blacklistMatcher]

func init() { activeBlacklist.Store(&blacklistMatcher{}) }

// newBlacklistMatcher compiles the enabled rules, skipping (and reporting) any
// that will not compile. One bad pattern must not disable the whole blacklist —
// that would silently start indexing everything the operator had excluded.
func newBlacklistMatcher(rules []blacklistRule) (*blacklistMatcher, []error) {
	var (
		out  []compiledBlacklistRule
		errs []error
	)
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, compiledBlacklistRule{re: re, field: r.Field})
	}
	return &blacklistMatcher{rules: out}, errs
}

// release is the set of fields a blacklist rule can be tested against.
type release struct {
	Subject string
	Title   string
	Poster  string
	Group   string
}

// whichBlacklistRule returns the pattern of the first rule that matches, or ""
// when the release is clean. Returning the pattern rather than a bool is what
// lets the job log and the hit counters say WHICH rule dropped something —
// without that, a blacklist that eats too much is nearly impossible to debug.
func whichBlacklistRule(r release) string {
	m := activeBlacklist.Load()
	if m == nil || len(m.rules) == 0 {
		return ""
	}
	for _, br := range m.rules {
		var target string
		switch br.field {
		case "subject":
			target = r.Subject
		case "title":
			target = r.Title
		case "poster":
			target = r.Poster
		case "group":
			target = r.Group
		default:
			continue // unknown field: fail closed
		}
		if target != "" && br.re.MatchString(target) {
			return br.re.String()
		}
	}
	return ""
}

// validBlacklistField reports whether the admin form gave us a field we match on.
func validBlacklistField(f string) bool {
	for _, v := range blacklistFields {
		if f == v {
			return true
		}
	}
	return false
}

// ── filter hit counters ─────────────────────────────────────────────

// filterHits accumulates rule hits in memory so the hot path never writes to the
// database. A pass flushes the whole map in one batch at the end; prod does the
// same for its SQL-side junk sweep, and for the same reason — a write per
// dropped article would cost more than the crawl.
type filterHits struct {
	mu   sync.Mutex
	hits map[filterHitKey]*filterHitVal
}

type filterHitKey struct{ kind, rule string }

type filterHitVal struct {
	count  int64
	sample string
}

func newFilterHits() *filterHits {
	return &filterHits{hits: make(map[filterHitKey]*filterHitVal)}
}

// note records one drop. sample is a title the rule matched; the FIRST one of a
// batch is kept rather than the last, purely so the sample stays stable while a
// long pass runs and an admin refreshing the page isn't chasing a moving target.
func (f *filterHits) note(kind, rule, sample string) {
	// nil-safe: pure helpers take this sink optionally so their tests need no
	// plugin, and a missing counter must never change filtering behaviour.
	if f == nil || rule == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	k := filterHitKey{kind, rule}
	v := f.hits[k]
	if v == nil {
		v = &filterHitVal{}
		f.hits[k] = v
	}
	v.count++
	if v.sample == "" {
		v.sample = truncateSample(sample)
	}
}

// noteN is note with an externally-accumulated count — for collectors that
// batch their own tallies (the ungrouped-stem counter) rather than calling
// per event.
func (f *filterHits) noteN(kind, rule string, n int64, sample string) {
	if f == nil || rule == "" || n <= 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	k := filterHitKey{kind, rule}
	v := f.hits[k]
	if v == nil {
		v = &filterHitVal{}
		f.hits[k] = v
	}
	v.count += n
	if v.sample == "" {
		v.sample = truncateSample(sample)
	}
}

// drain returns and clears the accumulated hits, so a failed flush loses one
// pass of counters rather than double-counting them on the next.
func (f *filterHits) drain() map[filterHitKey]*filterHitVal {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.hits) == 0 {
		return nil
	}
	out := f.hits
	f.hits = make(map[filterHitKey]*filterHitVal)
	return out
}

// truncateSample bounds what goes in the sample column. Subjects can be
// kilobytes of base64 in exactly the obfuscated-junk case these counters exist
// to measure, and the page only ever shows a line of it.
func truncateSample(s string) string {
	const max = 200
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	// Trim to a rune boundary so the stored sample is still valid UTF-8.
	cut := max
	for cut > 0 && !isUTF8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func isUTF8Start(b byte) bool { return b&0xC0 != 0x80 }

// sortedHitKeys gives the flush a deterministic order, so concurrent workers
// updating the same rows take locks in the same sequence and cannot deadlock.
func sortedHitKeys(m map[filterHitKey]*filterHitVal) []filterHitKey {
	keys := make([]filterHitKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].kind != keys[j].kind {
			return keys[i].kind < keys[j].kind
		}
		return keys[i].rule < keys[j].rule
	})
	return keys
}
