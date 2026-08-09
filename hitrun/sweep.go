package hitrun

import (
	"context"
	"time"
)

// The sweep: walk the tracker's accounting, apply the policy, act on the
// verdict.
//
// One pass rather than UNIT3D's three commands (AutoPreWarning, AutoWarning,
// AutoDeactivateWarning). The three exist there because each is a separate
// scheduled task; here the evaluator already returns which of the three a
// snatch is due, so splitting them would mean three walks over the same rows
// and three chances for them to disagree about the time.

// Notifier is how a member is told. Both are optional: a host that wires
// neither still gets the accounting, just silently, which is worse for the
// member but not wrong for the site.
type Notifier struct {
	// Prewarn is the courtesy notice. It is the one message that can still
	// change the outcome, so it names the torrent and what is required.
	Prewarn func(ctx context.Context, userID int64, torrentName, reason string)
	// Warn is the punishment notice.
	Warn func(ctx context.Context, userID int64, torrentName, reason string)
	// LimitReached fires when a member's active warnings hit the maximum.
	//
	// This plugin does NOT disable downloads itself. It cannot: the tracker
	// owns the download path and belongs to another plugin, and reaching across
	// to flip a flag in it would put the rule in one place and its enforcement
	// somewhere that never mentions it. The host decides what losing privileges
	// means — revoking the tracker entitlement, a role change, an email — and
	// this is where it finds out.
	LimitReached func(ctx context.Context, userID int64, activeWarnings int)
}

// SweepResult is what one pass did, for the job log.
type SweepResult struct {
	Considered int
	Prewarned  int
	Warned     int
	Expired    int
	Blocked    int
}

// Sweep runs one pass. now is a parameter for the same reason it is one in
// Evaluate: a test should be able to state a moment rather than wait for it.
func Sweep(ctx context.Context, st Store, p Policy, n Notifier, limit int, now time.Time) (SweepResult, error) {
	var res SweepResult

	// Expiry first, and unconditionally.
	//
	// It runs even when the policy is disabled, because turning the rules off
	// must not freeze every warning already on the record in place forever.
	// Somebody who was warned yesterday should still see it lapse.
	expired, err := st.ExpireWarnings(ctx, now)
	if err != nil {
		return res, err
	}
	res.Expired = expired

	if !p.Enabled {
		return res, nil
	}

	cands, err := st.Candidates(ctx, limit)
	if err != nil {
		return res, err
	}
	res.Considered = len(cands)

	// Members whose warning count changed, so the limit is checked once each
	// rather than once per warning — a member warned three times in one pass
	// should hear about losing their downloads once.
	touched := map[int64]bool{}

	for _, c := range cands {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		a := Evaluate(p, c.Snatch, c.PrewarnedAt, now)
		switch a.Verdict {
		case Prewarn:
			if err := st.RecordPrewarning(ctx, c.Snatch.UserID, c.Snatch.InfoHash); err != nil {
				return res, err
			}
			res.Prewarned++
			if n.Prewarn != nil {
				n.Prewarn(ctx, c.Snatch.UserID, c.TorrentName, a.Reason)
			}
		case Warn:
			w := Warning{
				UserID:      c.Snatch.UserID,
				InfoHash:    c.Snatch.InfoHash,
				TorrentName: c.TorrentName,
				Reason:      a.Reason,
				IssuedAt:    now,
				ExpiresAt:   now.Add(time.Duration(p.normalise().ExpireDays) * 24 * time.Hour),
			}
			if err := st.IssueWarning(ctx, w); err != nil {
				return res, err
			}
			res.Warned++
			touched[c.Snatch.UserID] = true
			if n.Warn != nil {
				n.Warn(ctx, c.Snatch.UserID, c.TorrentName, a.Reason)
			}
		}
	}

	// The limit check, once per member who gained a warning.
	for userID := range touched {
		active, err := st.ActiveWarnings(ctx, userID)
		if err != nil {
			return res, err
		}
		if DownloadsBlocked(p, active) {
			res.Blocked++
			if n.LimitReached != nil {
				n.LimitReached(ctx, userID, active)
			}
		}
	}
	return res, nil
}
