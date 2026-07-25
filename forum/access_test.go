package forum

import (
	"testing"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// The gate model: role axis OR tier axis, "all" admits anonymous, tier 0
// disables the tier axis, unknown role names fail closed to admin.
func TestViewerCapsPasses(t *testing.T) {
	anon := viewerCaps{}
	user := viewerCaps{loggedIn: true, role: core.RoleUser}
	mod := viewerCaps{loggedIn: true, role: core.RoleMod}
	admin := viewerCaps{loggedIn: true, role: core.RoleAdmin}
	trusted := viewerCaps{loggedIn: true, role: core.RoleUser, tier: 1}
	archivist := viewerCaps{loggedIn: true, role: core.RoleUser, tier: 3}

	cases := []struct {
		name string
		v    viewerCaps
		role string
		tier int
		want bool
	}{
		{"all admits anonymous", anon, "all", 0, true},
		{"empty means all (pre-gating rows)", anon, "", 0, true},
		{"user gate blocks anonymous", anon, "user", 0, false},
		{"user gate admits any account", user, "user", 0, true},
		{"mod gate blocks user", user, "mod", 0, false},
		{"mod gate admits admin", admin, "mod", 0, true},
		{"tier axis rescues a plain user", trusted, "mod", 1, true},
		{"tier below requirement fails", trusted, "mod", 2, false},
		{"tier 0 grants nothing extra", user, "mod", 0, false},
		{"tier requirement without role path", archivist, "admin", 3, true},
		{"unknown role fails closed to admin", mod, "superuser", 0, false},
		{"unknown role still open to admin", admin, "superuser", 0, true},
		{"anonymous never passes the tier axis", anon, "mod", 1, false},
	}
	for _, c := range cases {
		if got := c.v.passes(c.role, c.tier); got != c.want {
			t.Errorf("%s: passes(%q,%d) = %v, want %v", c.name, c.role, c.tier, got, c.want)
		}
	}
}

// Gate fields round-trip through the store and drive the listing filter +
// the public-only spotlight.
func TestCategoryGatesFilterListingAndSpotlight(t *testing.T) {
	repo := NewMemStore()
	open := seedCategory(repo, t, "open", 1)
	if err := repo.UpdateForumCategory(t.Context(), open.ID, CategoryParams{
		Name: "open", SeeRole: "all", ReadRole: "all", WriteRole: "user"}); err != nil {
		t.Fatal(err)
	}
	secret := seedCategory(repo, t, "secret", 2)
	if err := repo.UpdateForumCategory(t.Context(), secret.ID, CategoryParams{
		Name: "secret", SeeRole: "mod", ReadRole: "mod", WriteRole: "mod", SeeTier: 2}); err != nil {
		t.Fatal(err)
	}
	seedThreadForCategory(repo, t, open.ID, "public thread", time.Now())
	seedThreadForCategory(repo, t, secret.ID, "hidden thread", time.Now())

	cats, _ := repo.GetForumCategories(t.Context())
	if got := len(visibleCategories(cats, viewerCaps{})); got != 1 {
		t.Errorf("anonymous sees %d categories, want 1", got)
	}
	cats, _ = repo.GetForumCategories(t.Context())
	if got := len(visibleCategories(cats, viewerCaps{loggedIn: true, role: core.RoleMod})); got != 2 {
		t.Errorf("mod sees %d categories, want 2", got)
	}
	// Tier axis: a rank-2 plain user sees the mod-gated category too.
	cats, _ = repo.GetForumCategories(t.Context())
	if got := len(visibleCategories(cats, viewerCaps{loggedIn: true, role: core.RoleUser, tier: 2})); got != 2 {
		t.Errorf("tier-2 user sees %d categories, want 2", got)
	}

	// Spotlight is viewerless — only the open category's thread appears.
	threads, err := repo.GetRecentForumThreads(t.Context(), 10)
	if err != nil || len(threads) != 1 || threads[0].Title != "public thread" {
		t.Errorf("spotlight leaked gated content: %v %v", threads, err)
	}
}
