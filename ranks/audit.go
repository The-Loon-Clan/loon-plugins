package ranks

import (
	"context"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The audit half of the groups model: what happened to a user's memberships.
// Published separately from GroupDisplay rather than as another method on it,
// because the two have different audiences — every page asks for a badge, only
// an admin screen asks for a history — and GroupDisplay is kept narrow so that
// wanting the first does not come with the second.
//
// This is what lets the host's admin user page stop reading the legacy
// user_rank_history mirror (ENTITLEMENTS.md Stage 3.4).
type groupAudit struct {
	store Store
}

var _ pluginapi.GroupAudit = (*groupAudit)(nil)

// deletedGroupLabel stands in for a group that has been removed since the row
// was written. The store returns an empty name in that case (the history FK is
// ON DELETE SET NULL, so the audit trail survives the catalog), and the host's
// legacy query used exactly this word — keeping it means the admin table reads
// the same after the cutover.
const deletedGroupLabel = "Deleted"

func (a *groupAudit) HistoryFor(ctx context.Context, userID int64, limit int) ([]pluginapi.AuditEntry, error) {
	rows, err := a.store.MemberHistory(ctx, int(userID), limit)
	if err != nil {
		return nil, err
	}
	out := make([]pluginapi.AuditEntry, 0, len(rows))
	for _, r := range rows {
		name := r.GroupName
		if name == "" {
			name = deletedGroupLabel
		}
		out = append(out, pluginapi.AuditEntry{
			At:        r.At,
			Action:    r.Action,
			Group:     name,
			GroupSlug: r.GroupSlug,
			Details:   r.Details,
		})
	}
	return out, nil
}
