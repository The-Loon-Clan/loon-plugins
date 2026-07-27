package ranks

import (
	"context"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// Stage 2.3 of ENTITLEMENTS.md: membership changes GRANT into core, so readers
// can eventually ask "may this user do X?" without knowing groups exist.
//
// This is write-only and currently inert — nothing reads user_entitlements yet
// — which is the whole point of doing it before Stage 3. The rows can be
// inspected in production for days, against real purchases and real expiries,
// before a single reader depends on them.
//
// The grants are tagged `group:<slug>`, so each group owns exactly its own rows
// and two groups conferring the same key cannot clobber each other; core
// composes them (booleans OR, numerics MAX). Slugs are stable across renames —
// UpdateGroup preserves the stored slug — so the tag survives an admin edit.
//
// NOT transactional with the membership write. core.Entitlements has its own
// store, so a crash between the two leaves entitlements stale. That is
// tolerable because it is recoverable: Reconcile rebuilds every grant from
// group_members at Start, which makes the boot pass the repair path for this
// too. It is also why Grant is used rather than an incremental delta — Grant is
// an idempotent upsert on (user, key, source).
type entSync struct {
	ents  core.EntitlementsService
	store Store
}

// effectiveGrants resolves what a group actually confers, walking the parent
// chain so a child inherits its ancestors' keys. The most generous value wins,
// matching core's own composition rule, so a child can raise a parent's limit
// but never silently lower it.
//
// byID must be the whole catalog; depth is bounded by the schema's CHECK, and
// the seen-set makes a malformed chain terminate rather than spin.
func effectiveGrants(byID map[int]*Group, id int) map[string]int64 {
	out := map[string]int64{}
	seen := map[int]bool{}
	for cur := id; cur != 0; {
		g, ok := byID[cur]
		if !ok || seen[cur] {
			break
		}
		seen[cur] = true
		for k, v := range g.Grants {
			if have, ok := out[k]; !ok || v > have {
				out[k] = v
			}
		}
		if g.ParentID == nil {
			break
		}
		cur = *g.ParentID
	}
	return out
}

// catalog loads the groups once, indexed for parent-chain walks.
func (e *entSync) catalog(ctx context.Context) (map[int]*Group, error) {
	groups, err := e.store.Groups(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[int]*Group, len(groups))
	for i := range groups {
		byID[groups[i].ID] = &groups[i]
	}
	return byID, nil
}

func groupSource(g *Group) string { return "group:" + g.Slug }

// grantMembership writes everything groupID confers to userID, expiring with
// the membership itself so a lapsed grant stops counting even before the expiry
// job runs.
func (e *entSync) grantMembership(ctx context.Context, userID, groupID int, expires *time.Time) error {
	byID, err := e.catalog(ctx)
	if err != nil {
		return err
	}
	g, ok := byID[groupID]
	if !ok {
		return ErrGroupNotFound
	}
	src := groupSource(g)
	for key, val := range effectiveGrants(byID, groupID) {
		if err := e.ents.Grant(ctx, int64(userID), key, int(val), src, expires); err != nil {
			return err
		}
	}
	return nil
}

// revokeMembership removes what the group conferred. keys is what to revoke;
// pass nil to use the group's current effective set.
//
// The explicit list exists because core offers no way to enumerate a user's
// grants, so a key REMOVED from a group can only be revoked by a caller that
// still remembers it — see resyncGroup.
func (e *entSync) revokeMembership(ctx context.Context, userID, groupID int, keys []string) error {
	byID, err := e.catalog(ctx)
	if err != nil {
		return err
	}
	g, ok := byID[groupID]
	if !ok {
		return nil // the group is gone; its grants went with the cascade
	}
	if keys == nil {
		for k := range effectiveGrants(byID, groupID) {
			keys = append(keys, k)
		}
	}
	src := groupSource(g)
	for _, key := range keys {
		if err := e.ents.Revoke(ctx, int64(userID), key, src); err != nil {
			return err
		}
	}
	return nil
}

// resyncGroup re-grants every member of groupID and of its descendants, and
// revokes keys the group no longer confers.
//
// Descendants are included because entitlements inherit: raising a parent's
// download limit has to reach the children's members too, and nothing else
// would notice.
func (e *entSync) resyncGroup(ctx context.Context, groupID int, removedKeys []string) error {
	byID, err := e.catalog(ctx)
	if err != nil {
		return err
	}
	affected := []int{groupID}
	for id := range byID {
		if id != groupID && hasAncestor(byID, id, groupID) {
			affected = append(affected, id)
		}
	}
	members, err := e.store.MembersOfGroups(ctx, affected)
	if err != nil {
		return err
	}
	for _, m := range members {
		g, ok := byID[m.GroupID]
		if !ok {
			continue
		}
		src := groupSource(g)
		for _, key := range removedKeys {
			if err := e.ents.Revoke(ctx, int64(m.UserID), key, src); err != nil {
				return err
			}
		}
		for key, val := range effectiveGrants(byID, m.GroupID) {
			if err := e.ents.Grant(ctx, int64(m.UserID), key, int(val), src, m.ExpiresAt); err != nil {
				return err
			}
		}
	}
	return nil
}

// hasAncestor reports whether want is anywhere up id's parent chain.
func hasAncestor(byID map[int]*Group, id, want int) bool {
	seen := map[int]bool{}
	for cur := id; ; {
		g, ok := byID[cur]
		if !ok || seen[cur] || g.ParentID == nil {
			return false
		}
		seen[cur] = true
		if *g.ParentID == want {
			return true
		}
		cur = *g.ParentID
	}
}

// rebuildAll re-grants from every live membership. Called after Reconcile at
// Start, which makes boot the repair path for the non-transactional gap above:
// Grant is an idempotent upsert, so replaying costs nothing when nothing drifted.
//
// It does not revoke: without enumeration there is no way to find grants whose
// membership vanished while the process was down. The expiry job covers the
// common case, and a grant carries the membership's expiry, so a stale row
// stops counting on its own.
func (e *entSync) rebuildAll(ctx context.Context) (int, error) {
	byID, err := e.catalog(ctx)
	if err != nil {
		return 0, err
	}
	all := make([]int, 0, len(byID))
	for id := range byID {
		all = append(all, id)
	}
	members, err := e.store.MembersOfGroups(ctx, all)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range members {
		g, ok := byID[m.GroupID]
		if !ok {
			continue
		}
		src := groupSource(g)
		for key, val := range effectiveGrants(byID, m.GroupID) {
			if err := e.ents.Grant(ctx, int64(m.UserID), key, int(val), src, m.ExpiresAt); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}
