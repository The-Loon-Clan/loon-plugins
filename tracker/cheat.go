package tracker

import (
	"fmt"
	"time"
)

// Cheat detection: does the accounting this tracker is keeping describe
// something a real BitTorrent client could have done?
//
// It lives in the tracker rather than in a plugin over it — unlike hit-and-run,
// perks and seedlock, which are POLICY about members. This is about whether the
// tracker's own numbers are true, and the only thing that can answer that is
// the thing that wrote them.
//
// ── What it can and cannot see ──────────────────────────────────────────────
//
// user_stats holds CUMULATIVE totals and a last_seen. There is no per-announce
// history: the announce path computes a delta, applies it, and keeps nothing.
// So a rate can only be measured by SAMPLING — snapshot the totals, snapshot
// them again later, and divide the difference by the elapsed time.
//
// That has a consequence worth stating plainly, because it bounds what any of
// this can claim: a burst faster than the sampling interval is invisible.
// Somebody who reports 40GB and then goes quiet for an hour looks, to a
// sampler, like a member averaging 11MB/s. This catches SUSTAINED impossibility
// and long-run implausibility. It is not, and cannot be, a real-time cheat
// alarm — that would need the announce path to keep a log, which is a write on
// the hottest path the tracker has.
//
// ── What it deliberately does not do ────────────────────────────────────────
//
// Nothing here punishes. A flag is a record for a human to read, and the reason
// is that every rule below has a false positive that is somebody's ordinary
// evening: a seedbox on a 10Gb line, a LAN peer, a member who genuinely seeded
// a popular torrent to fifty people. Automatic bans on inference are how a
// tracker loses members who did nothing wrong, and the cost of being wrong is
// asymmetric — a missed cheat costs some ratio, a wrong ban costs a person.

// CheatPolicy is what counts as suspicious here.
//
// Every threshold is deliberately generous. The question these answer is not
// "is this unusual" but "is this PHYSICALLY unlikely", because the output is
// read by a person with limited time and a list full of maybes is a list
// nobody reads.
type CheatPolicy struct {
	// Enabled switches detection off entirely. Default false, like every other
	// rule in this stack that can end up accusing somebody.
	Enabled bool `json:"enabled"`

	// MaxUploadMBps is the sustained upload rate, per torrent, above which a
	// sample is flagged.
	//
	// 125 MB/s is a gigabit line saturated on ONE torrent, which is not a
	// consumer connection and is uncommon even for a seedbox. Set higher on a
	// site whose members are mostly seedboxes; the number is a property of the
	// membership, not of BitTorrent.
	MaxUploadMBps float64 `json:"max_upload_mbps"`

	// MaxRatioPerTorrent flags a member who has uploaded more than this
	// multiple of a torrent's SIZE.
	//
	// High on purpose. Uploading 50x a torrent is entirely normal for a
	// long-lived seed of something popular; the shape this catches is 500x on
	// a torrent with no swarm to absorb it.
	MaxRatioPerTorrent float64 `json:"max_ratio_per_torrent"`

	// MinSampleSeconds is how far apart two snapshots must be before a rate is
	// computed from them.
	//
	// A short gap makes the divisor tiny and the rate enormous: two snapshots
	// four seconds apart turn one 500MB announce into 125MB/s. This is the
	// guard against the detector inventing its own false positives.
	MinSampleSeconds int64 `json:"min_sample_seconds"`

	// MinBytes is the smallest delta worth judging. Below it, a rate is
	// arithmetic on noise.
	MinBytes int64 `json:"min_bytes"`
}

// DefaultCheatPolicy is off, with thresholds chosen to be hard to argue with.
func DefaultCheatPolicy() CheatPolicy {
	return CheatPolicy{
		Enabled:            false,
		MaxUploadMBps:      125,
		MaxRatioPerTorrent: 500,
		MinSampleSeconds:   300,
		MinBytes:           64 << 20, // 64 MiB
	}
}

func (p CheatPolicy) normalise() CheatPolicy {
	if p.MaxUploadMBps <= 0 {
		p.MaxUploadMBps = 125
	}
	if p.MaxRatioPerTorrent <= 0 {
		p.MaxRatioPerTorrent = 500
	}
	if p.MinSampleSeconds <= 0 {
		p.MinSampleSeconds = 300
	}
	if p.MinBytes <= 0 {
		p.MinBytes = 64 << 20
	}
	return p
}

// CheatKind names what was noticed.
type CheatKind string

const (
	// CheatImpossibleRate: more bytes reported than the line could carry.
	CheatImpossibleRate CheatKind = "impossible-rate"
	// CheatRatioImplausible: uploaded far more of a torrent than a swarm this
	// size could have taken.
	CheatRatioImplausible CheatKind = "ratio-implausible"
)

// CheatSample is one member's counters for one torrent at two points in time.
//
// Prev is the earlier snapshot and Cur the later one. TorrentSize is carried
// because the ratio rule is about the torrent, not about the member.
type CheatSample struct {
	UserID      int64
	InfoHash    string
	PrevAt      time.Time
	CurAt       time.Time
	PrevUp      int64
	CurUp       int64
	TorrentSize int64
	// Peers is the swarm the torrent had over the window: seeders + leechers.
	// Used only to soften the ratio rule, because a torrent with a real swarm
	// can legitimately absorb a great deal of upload.
	Peers int
}

// CheatFinding is one thing worth a human looking at.
type CheatFinding struct {
	UserID   int64
	InfoHash string
	Kind     CheatKind
	// Detail is written for the person reading the queue, not for a machine:
	// it says what was measured and against what, so the reader can judge it
	// without re-deriving the arithmetic.
	Detail string
}

// EvaluateCheat judges one sample. ok=false means nothing to report.
//
// Pure, and separate from any storage, for the same reason hitrun's Evaluate
// is: the thresholds are the part worth arguing about, and they should be
// testable without a database, a tracker, or a clock.
func EvaluateCheat(p CheatPolicy, s CheatSample) (CheatFinding, bool) {
	p = p.normalise()
	if !p.Enabled {
		return CheatFinding{}, false
	}
	delta := s.CurUp - s.PrevUp
	// A counter that went BACKWARDS is not a cheat, it is a reset — a torrent
	// removed and re-added, or the row rebuilt. Flagging it would fill the
	// queue with the tracker's own housekeeping.
	if delta <= 0 || delta < p.MinBytes {
		return CheatFinding{}, false
	}
	secs := int64(s.CurAt.Sub(s.PrevAt).Seconds())
	if secs < p.MinSampleSeconds {
		// Too close together to divide by. Saying nothing is right: the next
		// sweep will have a wider window over the same bytes.
		return CheatFinding{}, false
	}

	// ── impossible rate ─────────────────────────────────────────────────────
	mbps := float64(delta) / float64(secs) / (1 << 20)
	if mbps > p.MaxUploadMBps {
		return CheatFinding{
			UserID: s.UserID, InfoHash: s.InfoHash, Kind: CheatImpossibleRate,
			Detail: fmt.Sprintf("%.0f MB/s sustained over %s (limit %.0f)",
				mbps, time.Duration(secs)*time.Second, p.MaxUploadMBps),
		}, true
	}

	// ── implausible ratio ───────────────────────────────────────────────────
	//
	// Skipped when the torrent's size is unknown: a ratio against zero is a
	// division this refuses to do rather than a very large number it reports.
	if s.TorrentSize > 0 {
		ratio := float64(s.CurUp) / float64(s.TorrentSize)
		// A swarm absorbs upload legitimately, so the allowance grows with the
		// peers actually present. One peer is the floor, so a torrent nobody is
		// on gets the bare threshold rather than zero allowance.
		peers := s.Peers
		if peers < 1 {
			peers = 1
		}
		allowed := p.MaxRatioPerTorrent * float64(peers)
		if ratio > allowed {
			return CheatFinding{
				UserID: s.UserID, InfoHash: s.InfoHash, Kind: CheatRatioImplausible,
				Detail: fmt.Sprintf("uploaded %.0fx the torrent size to a swarm of %d (allowance %.0fx)",
					ratio, s.Peers, allowed),
			}, true
		}
	}
	return CheatFinding{}, false
}
