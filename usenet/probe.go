package usenet

import (
	"context"
	"fmt"
	"strconv"

	"github.com/the-loon-clan/loon/nntp"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Backbone fingerprint probe.
//
// Message-IDs are global — the same article carries the same id on every
// provider in the world — but article NUMBERS (the group ordinals watermarks
// count in) are assigned per backbone. So two providers share a backbone
// exactly when they assign the SAME numbers to the same articles. The probe
// tests that directly: sample recent (number, message-id) pairs from the
// reference provider's overview, STAT the same message-ids on the candidate
// (STAT is metadata-only — no article bodies move), and compare the numbers.
//
// This exists because guessing is expensive: filing one numbering universe's
// watermarks under another's backbone key froze 16 groups as falsely
// "caught up" (2026-07-24, frugal NA/EU + a pre-frugal provider era) — and
// the legacy crawler had been silently stuck the same way for months.

// probeSampleSize is how many recent articles to compare. A single mismatch
// already proves distinct numbering; the sample guards the SAME verdict
// against flukes.
const probeSampleSize = 8

// probeMinCompared is the fewest same-article comparisons that support a
// verdict — below this (poor propagation overlap) the probe is inconclusive.
const probeMinCompared = 3

// probeVerdict is one numbering comparison between two providers.
type probeVerdict struct {
	Group    string // the newsgroup the comparison ran in
	Compared int    // articles present on both servers
	Matched  int    // identical article numbers
	Same     bool   // Compared >= probeMinCompared and every number matched
}

// probeBackbone compares cand's article numbering against ref's, trying a few
// active groups until enough articles overlap. Both connections are one-shot
// dials (operator-initiated, rare) — the crawl pools are never touched.
func (p *Plugin) probeBackbone(ctx context.Context, ref pluginapi.Server, cand pluginapi.Server) (probeVerdict, error) {
	groups, err := p.st.activeGroups(ctx, 5)
	if err != nil {
		return probeVerdict{}, err
	}
	if len(groups) == 0 {
		return probeVerdict{}, fmt.Errorf("no active newsgroups to probe with — enable a group first")
	}

	refConn, err := dialServer(ref)
	if err != nil {
		return probeVerdict{}, fmt.Errorf("reference %s: %w", ref.Host, err)
	}
	defer refConn.Quit()
	candConn, err := dialServer(cand)
	if err != nil {
		return probeVerdict{}, fmt.Errorf("candidate %s: %w", cand.Host, err)
	}
	defer candConn.Quit()

	var last probeVerdict
	for _, g := range groups {
		v, err := probeGroup(refConn, candConn, g.Name)
		if err != nil {
			continue // group missing on one side, empty, etc. — try the next
		}
		if v.Compared >= probeMinCompared {
			return v, nil
		}
		if v.Compared > last.Compared {
			last = v
		}
	}
	if last.Compared == 0 {
		return last, fmt.Errorf("no comparable articles found across %d group(s) — servers may not share content", len(groups))
	}
	return last, fmt.Errorf("only %d comparable article(s) (want ≥%d) — verdict inconclusive", last.Compared, probeMinCompared)
}

// probeGroup runs one group's comparison: recent overview pairs from ref,
// STAT-by-message-id on cand.
func probeGroup(refConn, candConn *nntp.Conn, group string) (probeVerdict, error) {
	v := probeVerdict{Group: group}
	_, _, high, err := refConn.Group(group)
	if err != nil {
		return v, err
	}
	lo := high - 200
	if lo < 1 {
		lo = 1
	}
	ovs, _, err := refConn.Overview(lo, high)
	if err != nil {
		return v, err
	}
	if _, _, _, err := candConn.Group(group); err != nil {
		return v, err
	}
	v = compareNumbering(ovs, func(msgid string) (int, bool) {
		numStr, _, err := candConn.Stat(msgid)
		if err != nil {
			return 0, false // 430 not-there / not yet propagated — proves nothing
		}
		n, err := strconv.Atoi(numStr)
		if err != nil || n <= 0 {
			return 0, false // "0" = server knows the article but not in this group
		}
		return n, true
	})
	v.Group = group
	return v, nil
}

// compareNumbering is the pure half of the probe: walk the reference's
// overview newest-first (the freshest articles are the likeliest to exist on
// both servers already), look each message-id up on the candidate, and count
// number agreement. stat returns (article number, present-in-group).
func compareNumbering(ovs []nntp.MessageOverview, stat func(msgid string) (int, bool)) probeVerdict {
	var v probeVerdict
	for i := len(ovs) - 1; i >= 0 && v.Compared < probeSampleSize; i-- {
		ov := ovs[i]
		if ov.MessageId == "" || ov.MessageNumber <= 0 {
			continue
		}
		n, ok := stat(ov.MessageId)
		if !ok {
			continue
		}
		v.Compared++
		if n == ov.MessageNumber {
			v.Matched++
		}
	}
	v.Same = v.Compared >= probeMinCompared && v.Matched == v.Compared
	return v
}
