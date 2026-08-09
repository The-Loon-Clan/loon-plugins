package hitrun

import (
	"fmt"
	"time"
)

// The hit-and-run decision, kept free of SQL and of the clock so it can be
// tested by stating a situation and reading the verdict.
//
// This is the part worth being pedantic about. Every other file here moves rows
// around; this one decides whether a member is punished, and it is wrong in two
// directions at once: warn someone who did nothing wrong and they leave, miss a
// real offender and the rule means nothing. The thresholds are all
// admin-editable precisely because no single set of them is correct for every
// site.

// Policy is the site's hit-and-run rules. Defaults follow UNIT3D's
// config/hitrun.php, which is the closest thing this space has to a standard —
// with one addition, RatioSatisfies, noted below.
type Policy struct {
	// Enabled switches the whole thing off. Default FALSE, like the tracker
	// itself: a rule that disables downloads is not something a host should
	// acquire by merely compiling the code in.
	Enabled bool `json:"enabled"`

	// Seedtime is how long a member must seed a torrent, in seconds.
	// UNIT3D ships 604800 — seven days. Community trackers commonly run
	// 24-72h, and AlphaRatio scales it with size (3 days under 48GB, 7 over),
	// so this is the number most likely to be retuned.
	Seedtime int `json:"seedtime_seconds"`

	// PrewarnDays is how long a member may be gone before the courtesy notice.
	PrewarnDays int `json:"prewarn_days"`

	// GraceDays is how long AFTER the notice they have to come back.
	GraceDays int `json:"grace_days"`

	// MaxWarnings is how many active warnings before download privileges go.
	MaxWarnings int `json:"max_warnings"`

	// ExpireDays is how long a warning counts for.
	ExpireDays int `json:"expire_days"`

	// BufferPercent is how much of a torrent a member must actually have taken
	// before they can be liable for it, as a percentage of its size.
	//
	// This is the setting that stops a rule from punishing accidents. Someone
	// who starts a 40GB torrent, takes 300MB and cancels has not hit-and-run;
	// they changed their mind. UNIT3D ships 10.
	BufferPercent int `json:"buffer_percent"`

	// RatioSatisfies excuses a short seedtime when the member has already
	// uploaded back at least as much as they took.
	//
	// NOT in UNIT3D's AutoWarning conditions, which key on seedtime alone, but
	// it is what most real trackers do — TorrentLeech treats under-1:1 OR
	// under-minimum-seedtime as the offence, which is the same rule stated as
	// an OR. Someone who returned a full copy has done their share, and
	// punishing them for doing it quickly is a rule with no purpose behind it.
	RatioSatisfies bool `json:"ratio_satisfies"`
}

// DefaultPolicy is UNIT3D's shipped configuration, plus the ratio escape.
func DefaultPolicy() Policy {
	return Policy{
		Enabled:        false,
		Seedtime:       604800, // 7 days
		PrewarnDays:    1,
		GraceDays:      3,
		MaxWarnings:    3,
		ExpireDays:     14,
		BufferPercent:  10,
		RatioSatisfies: true,
	}
}

// normalise replaces nonsense with the default for that field.
//
// A zero here is almost always an unset config key rather than an operator
// asking for "no grace at all", and reading it literally turns a missing line
// in a config file into a site that warns everybody immediately.
func (p Policy) normalise() Policy {
	d := DefaultPolicy()
	if p.Seedtime <= 0 {
		p.Seedtime = d.Seedtime
	}
	if p.PrewarnDays < 0 {
		p.PrewarnDays = d.PrewarnDays
	}
	if p.GraceDays < 0 {
		p.GraceDays = d.GraceDays
	}
	if p.MaxWarnings <= 0 {
		p.MaxWarnings = d.MaxWarnings
	}
	if p.ExpireDays <= 0 {
		p.ExpireDays = d.ExpireDays
	}
	if p.BufferPercent < 0 || p.BufferPercent > 100 {
		p.BufferPercent = d.BufferPercent
	}
	return p
}

// Snatch is one member's standing on one torrent, as the tracker records it.
type Snatch struct {
	UserID      int64
	InfoHash    string
	TorrentSize int64
	// Downloaded is actual bytes taken, which is what the buffer is measured
	// against — not the torrent's size, and not "completed".
	Downloaded int64
	Uploaded   int64
	// Seedtime is accumulated seconds seeding.
	Seedtime int64
	// Seeding is whether they are still connected. Someone still seeding cannot
	// be a hit-and-run however short their clock is — they have not run.
	Seeding bool
	// LastSeen is the last announce. Grace is measured from here.
	LastSeen time.Time
	// Immune exempts this particular snatch (a moderator's decision, or a
	// freeleech grant that the host chose to make count).
	Immune bool
}

// Verdict is what the evaluator decided, and why.
type Verdict int

const (
	// Satisfied — the member met the requirement, or was never liable.
	Satisfied Verdict = iota
	// Prewarn — they are away, past the notice threshold, and should be told.
	Prewarn
	// Warn — they were told, the grace has run out, and they are still away.
	Warn
)

func (v Verdict) String() string {
	switch v {
	case Prewarn:
		return "prewarn"
	case Warn:
		return "warn"
	default:
		return "satisfied"
	}
}

// Assessment is the evaluator's full answer: the verdict plus the sentence a
// member should be shown, which is the only honest way to explain a punishment.
type Assessment struct {
	Verdict Verdict
	Reason  string
}

// Evaluate decides one snatch against the policy at a given moment.
//
// prewarnedAt is when the courtesy notice was sent, or the zero time if it has
// not been. now is passed rather than read so a test can state a situation
// instead of sleeping through one.
//
// The order of the checks is the whole design, and each early return is a
// reason NOT to punish somebody:
//
//  1. the rules are off;
//  2. the snatch is exempt by hand;
//  3. they never really took it (the buffer);
//  4. they are still seeding — they have not run;
//  5. they met the seedtime, or returned a full copy;
//  6. they have not been gone long enough to notice;
//  7. they were noticed, and the grace has run out.
func Evaluate(p Policy, s Snatch, prewarnedAt time.Time, now time.Time) Assessment {
	p = p.normalise()

	if !p.Enabled {
		return Assessment{Satisfied, ""}
	}
	if s.Immune {
		return Assessment{Satisfied, "exempt"}
	}
	// The buffer. Measured against actual bytes taken, so a cancelled download
	// is not an offence — see Policy.BufferPercent.
	if s.TorrentSize > 0 {
		threshold := s.TorrentSize / 100 * int64(p.BufferPercent)
		if s.Downloaded <= threshold {
			return Assessment{Satisfied, "below the download threshold"}
		}
	}
	// Still connected. Whatever their clock says, they have not run.
	if s.Seeding {
		return Assessment{Satisfied, "still seeding"}
	}
	if s.Seedtime >= int64(p.Seedtime) {
		return Assessment{Satisfied, "seedtime met"}
	}
	// The ratio escape: a full copy returned is a share done.
	if p.RatioSatisfies && s.Downloaded > 0 && s.Uploaded >= s.Downloaded {
		return Assessment{Satisfied, "ratio met"}
	}

	away := now.Sub(s.LastSeen)
	if prewarnedAt.IsZero() {
		if away < time.Duration(p.PrewarnDays)*24*time.Hour {
			return Assessment{Satisfied, "within the notice period"}
		}
		return Assessment{Prewarn, fmt.Sprintf(
			"not seeding for %s, and %s of seeding is required",
			roundDuration(away), roundDuration(time.Duration(p.Seedtime)*time.Second))}
	}
	// TWO clocks, and both must have run out. The notice has to have aged, AND
	// they have to still be gone.
	//
	// Measuring only from the notice warns somebody who was told a month ago,
	// came back, seeded, and left again an hour before the job ran — their
	// notice is ancient but they are not the person the rule is for. UNIT3D
	// requires the same pair: prewarned_at past the prewarn threshold AND
	// updated_at (the last announce) past the grace one.
	grace := time.Duration(p.GraceDays) * 24 * time.Hour
	if now.Sub(prewarnedAt) < grace || away < grace {
		return Assessment{Satisfied, "within the grace period"}
	}
	return Assessment{Warn, fmt.Sprintf(
		"seeded for %s of the %s required, and did not return within %d day(s) of being notified",
		roundDuration(time.Duration(s.Seedtime)*time.Second),
		roundDuration(time.Duration(p.Seedtime)*time.Second),
		p.GraceDays)}
}

// DownloadsBlocked reports whether a member's active warnings have reached the
// limit. Separate from Evaluate because it is a question about the MEMBER, not
// about one torrent, and the two are asked at different times.
func DownloadsBlocked(p Policy, activeWarnings int) bool {
	p = p.normalise()
	if !p.Enabled {
		return false
	}
	return activeWarnings >= p.MaxWarnings
}

// roundDuration renders a span the way a member reads it: whole days once it is
// days, whole hours below that. "168h0m0s" is technically the seedtime
// requirement and tells nobody anything.
func roundDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= 24*time.Hour:
		return "1 day"
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	case d >= time.Hour:
		return "1 hour"
	default:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
}
