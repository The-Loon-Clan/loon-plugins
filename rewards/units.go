package rewards

import (
	"context"
	"errors"
	"fmt"
)

// Per-unit sources.
//
// A per_unit reward pays for a countable thing — completed years of
// membership, grabs on your uploads, gigabytes seeded — and this plugin
// deliberately knows what none of those are. The host counts; the engine owns
// "how far have I already paid", which is the half that gets written wrong.
//
// A source is registered as "rewards.units.<reward slug>", so adding a new
// per_unit reward is a reward row plus one registration, with no change here.

// UnitSourcePrefix + a reward's slug is the registry key its counter is
// published under.
const UnitSourcePrefix = "rewards.units."

// UnitSource supplies current counts for one per_unit reward.
type UnitSource interface {
	// Marks returns userID -> the counter's current value.
	//
	// A source may return every member or only those whose count could have
	// moved; the engine grants nothing for an unchanged one either way, so
	// narrowing is an optimisation rather than a correctness requirement.
	Marks(ctx context.Context) (map[int64]int64, error)
}

// GrantUnits pays everyone a per_unit reward owes, and is the whole reason
// per_unit rewards do not each need their own job.
//
// Two queries plus one grant per member who actually moved. The obvious
// per-member shape — load the reward, read its previous mark, decide — is
// three round trips per member, and at a few thousand members with almost no
// deltas that is thousands of queries to discover that nothing happened.
func (e *Engine) GrantUnits(ctx context.Context, rewardSlug string, src UnitSource) (granted int, err error) {
	r, err := e.store.RewardBySlug(ctx, rewardSlug)
	if err != nil {
		return 0, err
	}
	if r == nil || !r.Enabled {
		return 0, nil // not configured, or deliberately off
	}
	if r.Kind != KindPerUnit {
		return 0, fmt.Errorf("reward %q is %s, not per_unit", rewardSlug, r.Kind)
	}

	marks, err := src.Marks(ctx)
	if err != nil {
		return 0, fmt.Errorf("read marks for %q: %w", rewardSlug, err)
	}
	if len(marks) == 0 {
		return 0, nil
	}
	ids := make([]int64, 0, len(marks))
	for id := range marks {
		ids = append(ids, id)
	}
	previous, err := e.store.PreviousMarks(ctx, r.ID, ids)
	if err != nil {
		return 0, err
	}

	for userID, mark := range marks {
		if mark <= previous[userID] {
			// The overwhelmingly common case, and the reason this is a filter
			// rather than a call: nothing moved.
			continue
		}
		if _, err := e.GrantPerUnit(ctx, userID, r.ID, mark); err != nil {
			if errors.Is(err, ErrNothingOwed) || errors.Is(err, ErrAlreadyGranted) {
				// Another worker got there between the batch read and now.
				continue
			}
			// One member's failure must not cost everyone else theirs — a
			// missing medal handler would otherwise stop the whole run.
			e.logf("rewards: %s for user %d: %v", rewardSlug, userID, err)
			continue
		}
		granted++
	}
	return granted, nil
}
