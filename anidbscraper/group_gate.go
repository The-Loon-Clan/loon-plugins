package anidbscraper

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The newsgroup gate for the NZB scanner.
//
// MIRROR: this is the 1:1 port of ameNZB's pkg/services/anidb_scan_group_gate.go
// (mode constants, pattern language, and the allow/verdict decisions have the
// same names on both sides so a diff is readable). The host copy gates the
// production scanner; this one gates the extracted plugin's runScan. When the
// decision changes, change BOTH — the two ran divergently for exactly as long
// as it took someone to notice, which is the failure this comment exists to
// prevent. The host file names this one back.
//
// Why the gate exists, in the host's numbers. The scanner answers "which anime
// is this release" for every untagged row, with a title matcher that has no way
// to say "this isn't anime at all". Measured on ameNZB over the 14 days to
// 2026-08-31 (12,247 scanner tags):
//
//	alt.binaries.multimedia.anime.highspeed   7,945 tags   83% strict precision
//	every other newsgroup                     4,302 tags   ~15% strict precision
//
// Off the anime groups the matcher is not wrong at the margin, it is guessing:
// "Adobe Audition 2026" reached an anime romanized "O-di-syeon", "Any Video
// Downloader Pro" reached one called "Downloader", "The Bear S03" reached "Mori
// no Kuma-san". Any catalogue of this size holds entries whose English title is
// an ordinary phrase, and a group carrying western TV, music and software keeps
// producing releases that spell those phrases.
//
// So the gate is about JURISDICTION, not confidence: in a group whose traffic
// is not anime, the scanner has no business guessing by title at all.
//
// THE DEFAULT DIFFERS FROM THE HOST'S, DELIBERATELY. ameNZB ships the allowlist
// "*anime*" because it measured its own 62 crawled groups. This plugin ships an
// EMPTY allowlist, which means "no gate": a host upgrading past this file gets
// byte-identical scanner behaviour until it configures the gate itself. The
// measurement above is ameNZB's traffic, not a fact about newsgroups, and a
// plugin that silently stopped tagging on somebody else's site because of
// somebody else's data would be a worse bug than the one it fixes.
//
// Configure it in config.yml under the plugin's own section, with the same two
// keys the host's job-config form uses:
//
//	plugins:
//	  anidbscraper:
//	    group_allowlist: "*anime*"   # empty (the default) = no gate
//	    group_gate_mode: refuse      # refuse | exact | report | off
//
// One difference from the host worth knowing: ameNZB re-reads these from its
// job-settings table once per 5,000-row batch, so an operator can widen the
// allowlist mid-run. loon's core.Job carries no config vars, so the plugin
// reads config.yml once in Provision — a change here needs a worker restart.

// Gate modes. Same strings as the host's "group_gate_mode" job config var.
const (
	// gateModeRefuse: off-allowlist releases get no title matching at all.
	// Any deterministic id-based resolution the host does around the plugin
	// is untouched. This is what an unrecognised mode falls back to.
	gateModeRefuse = "refuse"
	// gateModeExact: off-allowlist releases may only be tagged by an exact
	// whole-title index hit — no prefix walk, no containment. Requires the
	// injected Matcher to implement pluginapi.ExactTitleMatcher.
	//
	// ameNZB measured this as the weaker setting, not the safer one: of its
	// 4,302 off-allowlist tags only 328 came from an exact hit, and hand-
	// judging 30 of those gave 4 right — the same precision as the fuzzy
	// ones they would replace, because an exact hit on an ordinary English
	// phrase is the same evidence spelled completely. It is kept as a
	// middle setting for a host whose off-group traffic looks different.
	gateModeExact = "exact"
	// gateModeReport: change nothing, but count and log what enforcing would
	// have blocked. Run this first when tuning an allowlist.
	gateModeReport = "report"
	// gateModeOff: no gate, even with an allowlist or an AllowTitleGuess
	// wired. The explicit kill switch.
	gateModeOff = "off"
)

// groupGate is the parsed, boot-time form of the two config keys plus the
// optional host override. The zero value is inert: allows() answers true for
// everything, which is the behaviour of every host that configures nothing.
type groupGate struct {
	mode     string
	patterns []string
	// allow is Deps.AllowTitleGuess. When wired it REPLACES the pattern
	// test — the host decides jurisdiction its own way (a tracker category,
	// a source flag, a release's origin) — while mode still decides what an
	// out-of-jurisdiction row may be tagged by.
	allow func(pluginapi.NzbRow) bool
}

// newGroupGate parses the config into a gate.
//
// It returns a warning string rather than an error for a mode it does not
// recognise: a typo in one config string must not keep a worker from booting,
// but it must not silently turn the gate off either (that is the failure the
// gate exists to prevent), so an unknown mode becomes the strictest one and
// says so in the job log. A genuine wiring contradiction — two ways of
// deciding jurisdiction at once — is an error, because the plugin would have
// to ignore one of them and no answer to that is right.
func newGroupGate(cfg Config, allow func(pluginapi.NzbRow) bool) (groupGate, string, error) {
	g := groupGate{
		mode:     strings.ToLower(strings.TrimSpace(cfg.GroupGateMode)),
		patterns: parseGroupPatterns(cfg.GroupAllowlist),
		allow:    allow,
	}
	if g.allow != nil && len(g.patterns) > 0 {
		return groupGate{}, "", errors.New(
			"anidbscraper: Deps.AllowTitleGuess and plugins.anidbscraper.group_allowlist are both set — " +
				"the allowlist would be ignored; wire one or the other")
	}
	var warn string
	switch g.mode {
	case gateModeRefuse, gateModeExact, gateModeReport, gateModeOff:
	case "":
		// Unset means the host's shipped default, which is what the host's
		// DeclareConfig serves for a blank value. It only bites when an
		// allowlist (or an override) is present — with neither, the gate is
		// inert whatever the mode says.
		g.mode = gateModeRefuse
	default:
		warn = "group_gate_mode " + strconv.Quote(cfg.GroupGateMode) +
			" is not one of refuse/exact/report/off — falling back to refuse"
		g.mode = gateModeRefuse
	}
	return g, warn, nil
}

// parseGroupPatterns splits the config value into lowercase patterns.
// Newline- or comma-separated; blank lines and #-comments ignored.
func parseGroupPatterns(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ','
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ToLower(strings.TrimSpace(f))
		if f == "" || strings.HasPrefix(f, "#") {
			continue
		}
		out = append(out, f)
	}
	return out
}

// globMatch reports whether s matches a pattern whose only metacharacter is
// '*' (any run of any characters, including none).
//
// Hand-rolled rather than path/filepath.Match because that one refuses to let
// '*' cross the path separator — which is '\' on Windows and '/' elsewhere, so
// the same pattern would decide differently on a developer's machine than in
// the container. Newsgroup names contain neither, but a gate that decides what
// gets tagged should not depend on that staying true.
func globMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, mid := range parts[1 : len(parts)-1] {
		if mid == "" {
			continue
		}
		i := strings.Index(s, mid)
		if i < 0 {
			return false
		}
		s = s[i+len(mid):]
	}
	if last == "" {
		return true
	}
	return strings.HasSuffix(s, last)
}

// active reports whether the gate does anything.
//
// An empty allowlist with no override disables it: a gate with nothing on its
// allowlist would refuse every release on the site, so "nothing configured"
// has to mean "no gate" rather than "block everything". That is also what
// makes this port a no-op for every host that upgrades without configuring it.
func (g groupGate) active() bool {
	if g.mode == gateModeOff {
		return false
	}
	return g.allow != nil || len(g.patterns) > 0
}

// allows reports whether the scanner may guess this release's anime by title.
//
// A release is allowed if ANY newsgroup it was seen in matches ANY pattern — a
// crosspost carried in an anime group is evidence about the posting even when
// it is not the row's primary group.
//
// A release with NO newsgroup at all is allowed. On the host that means a
// member upload or a scraped release rather than a crawled posting, and this
// gate reasons about what a newsgroup's traffic says; refusing on absent
// evidence would silently stop tagging uploads. It is also what makes an
// un-populated NzbRow.Groups safe (see the run-level warning in runScan).
func (g groupGate) allows(row pluginapi.NzbRow) bool {
	if !g.active() {
		return true
	}
	if g.allow != nil {
		return g.allow(row)
	}
	known := false
	for _, grp := range row.Groups {
		grp = strings.ToLower(strings.TrimSpace(grp))
		if grp == "" {
			continue
		}
		known = true
		for _, p := range g.patterns {
			if globMatch(p, grp) {
				return true
			}
		}
	}
	return !known
}

// describe is the one line the scan logs about its own gate, so an operator
// reading /admin/jobs can tell an inert gate from an enforcing one without
// going to look at config.yml.
func (g groupGate) describe() string {
	if !g.active() {
		return "inert (no allowlist and no AllowTitleGuess wired — nothing is gated)"
	}
	if g.allow != nil {
		return "mode=" + g.mode + ", jurisdiction decided by the host's AllowTitleGuess"
	}
	return "mode=" + g.mode + ", " + strconv.Itoa(len(g.patterns)) + " allowlist pattern(s)"
}

// gateVerdict is what the gate decides for one row.
//
// The host's copy carries a fourth field, `anilist`, permitting its last-
// resort AniList search on allowed rows only. This port has no such step yet
// (runScan is title-match only), so the field would have no reader; it comes
// back with the AniList fallback when that body is extracted.
type gateVerdict struct {
	// match is the title lookup to use, or nil to refuse title matching.
	match func(string) (int, bool)
	// offGate is true when the release is outside the gate's jurisdiction.
	offGate bool
	// report is true in report mode: nothing is blocked, but matches on
	// off-allowlist rows are counted so the log can say what enforcing
	// would have cost.
	report bool
}

// verdict resolves the gate for one row. p.exactMatcher is non-nil whenever
// the mode is exact — Provision refuses to boot otherwise.
func (p *Plugin) verdict(g groupGate, row pluginapi.NzbRow) gateVerdict {
	if g.allows(row) {
		return gateVerdict{match: deps.Matcher.Find}
	}
	switch g.mode {
	case gateModeExact:
		return gateVerdict{match: p.exactMatcher.FindExact, offGate: true}
	case gateModeReport:
		return gateVerdict{match: deps.Matcher.Find, offGate: true, report: true}
	default:
		// refuse, and anything unrecognised that got this far. A mode the
		// plugin does not understand must not silently turn the gate off.
		return gateVerdict{offGate: true}
	}
}

// primaryGroup is the group name to attribute a gated row to in the per-group
// tally: the first one it was seen in. A tally keyed on every group of every
// crosspost would double-count and make the numbers useless for tuning.
func primaryGroup(groups []string) string {
	for _, g := range groups {
		if g = strings.TrimSpace(g); g != "" {
			return g
		}
	}
	return ""
}

// topGatedGroups renders the busiest gated groups for the job log, so the
// operator can see what the allowlist is turning away and widen it if a real
// anime group is missing from it.
func topGatedGroups(counts map[string]int, n int) string {
	type kv struct {
		group string
		count int
	}
	pairs := make([]kv, 0, len(counts))
	for g, c := range counts {
		pairs = append(pairs, kv{g, c})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].group < pairs[j].group
	})
	if len(pairs) > n {
		pairs = pairs[:n]
	}
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, p.group+"="+strconv.Itoa(p.count))
	}
	return strings.Join(parts, " ")
}

// gateOutcomeLine is what the scan says about its gate when the run ends, or
// "" when there is nothing to report. Returned rather than logged so the
// decision is testable without a scheduler.
//
// The last branch is the one that matters most: a host can configure the
// allowlist and get NOTHING, because NzbRow.Groups is optional and its own
// NzbTagSink.UntaggedBatch may not populate it. Every row then reads as "no
// newsgroup", which the gate allows, so an operator who believes the scanner
// is gated is watching it guess exactly as before. That failure is invisible
// without this line, so a configured pattern gate that saw no newsgroup on a
// single row of a non-empty scan says so.
func gateOutcomeLine(g groupGate, scanned, gatedRows, gatedTagged, rowsWithGroups int, byGroup map[string]int) string {
	if !g.active() {
		return ""
	}
	switch {
	case gatedRows > 0 && g.mode == gateModeReport:
		return fmt.Sprintf("Newsgroup gate (report only, nothing blocked): %d rows are out of jurisdiction; "+
			"enforcing would have prevented %d tags. Top groups: %s",
			gatedRows, gatedTagged, topGatedGroups(byGroup, 8))
	case gatedRows > 0 && g.mode == gateModeExact:
		return fmt.Sprintf("Newsgroup gate (exact-match only out of jurisdiction): %d rows gated, %d still tagged. "+
			"Top groups: %s", gatedRows, gatedTagged, topGatedGroups(byGroup, 8))
	case gatedRows > 0:
		return fmt.Sprintf("Newsgroup gate: %d rows refused title matching. Top groups: %s",
			gatedRows, topGatedGroups(byGroup, 8))
	case g.allow == nil && scanned > 0 && rowsWithGroups == 0:
		return "Newsgroup gate is configured but INERT: not one scanned row carried a newsgroup, " +
			"so nothing could be gated. The host's NzbTagSink.UntaggedBatch is not populating " +
			"NzbRow.Groups - populate it, or wire Deps.AllowTitleGuess instead."
	}
	return ""
}

// resolveExactMatcher returns the step-1-only lookup gate mode "exact" needs,
// or an error naming what to wire.
//
// It fails at boot rather than at scan time, and it fails rather than falling
// back: a host that asked for exact-only and silently got the fuzzy matcher —
// prefix walk, substring containment, the steps the mode exists to switch off
// — would be worse off than with no gate at all, because it would believe it
// had one. Every other mode needs nothing here and gets nil.
func resolveExactMatcher(g groupGate, m pluginapi.TitleMatcher) (pluginapi.ExactTitleMatcher, error) {
	if !g.active() || g.mode != gateModeExact {
		return nil, nil
	}
	em, ok := m.(pluginapi.ExactTitleMatcher)
	if !ok {
		return nil, errors.New("anidbscraper: group_gate_mode=exact needs the injected Matcher to implement " +
			"pluginapi.ExactTitleMatcher (FindExact); wire it, or set group_gate_mode: refuse")
	}
	return em, nil
}
